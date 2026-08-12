package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/Paco5687/autormm/internal/protocol"
	"github.com/Paco5687/autormm/internal/wol"
)

// wolRelays picks the online hosts that should broadcast the wake packet for
// target: any that share an IPv4 network with it, judged at /24.
//
// The real masks are unknowable here — facts record addresses, not prefixes —
// and /24 is the practical answer for the networks this runs on. Judging too
// narrowly costs nothing worse than an extra broadcast from an unrelated
// segment, which is harmless; judging too loosely would miss the one peer that
// could actually deliver the frame.
func wolRelays(target protocol.HostView, all []protocol.HostView) []string {
	nets := map[string]bool{}
	for _, ip := range target.Facts.IPs {
		if k := slash24(ip); k != "" {
			nets[k] = true
		}
	}
	var relays []string
	for _, h := range all {
		if h.AgentID == target.AgentID || !h.Online {
			continue
		}
		for _, ip := range h.Facts.IPs {
			if nets[slash24(ip)] {
				relays = append(relays, h.AgentID)
				break
			}
		}
	}
	return relays
}

// slash24 keys an IPv4 address by its /24, or "" for anything else.
func slash24(s string) string {
	ip := net.ParseIP(s)
	if ip == nil {
		return ""
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return ""
	}
	return ip4.Mask(net.CIDRMask(24, 32)).String()
}

// handleWOL wakes an offline host by asking its LAN peers to broadcast
// Wake-on-LAN packets for it.
//
// The target is off; nothing can be sent *to* it, only shouted near it. Every
// matching peer is asked rather than just one — redundant broadcasts are free,
// and the peer chosen by a cleverer scheme might be the one machine whose NIC
// driver eats broadcasts. The hub also broadcasts locally as a fallback, which
// helps exactly when hub and target share a segment and is harmless otherwise.
//
// Success here means "packets were sent", not "the machine is booting": WoL is
// fire-and-forget by design, and the only real confirmation is the host
// enrolling a minute later. The dashboard already shows that.
func (s *Server) handleWOL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	all := s.store.views()
	var target *protocol.HostView
	for i := range all {
		if all[i].AgentID == req.AgentID {
			target = &all[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "unknown host", http.StatusNotFound)
		return
	}
	if len(target.Facts.MACs) == 0 {
		http.Error(w, "no MAC address recorded for this host — it must have connected at least once", http.StatusConflict)
		return
	}

	relays := wolRelays(*target, all)
	for _, id := range relays {
		if c := s.store.connFor(id); c != nil {
			c.sendJSON(protocol.WOLRequest{Type: protocol.TypeWOL, MACs: target.Facts.MACs})
		}
	}
	// The hub's own broadcast: the fallback when no agent shares the segment,
	// and a second voice when one does.
	hubErr := wol.Send(target.Facts.MACs)

	s.audit(r, "wake", req.AgentID,
		fmt.Sprintf("macs=%v relays=%v", target.Facts.MACs, relays), "ok")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     len(relays) > 0 || hubErr == nil,
		"relays": len(relays),
	})
}
