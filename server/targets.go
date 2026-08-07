package server

import (
	"net/http"
	"strings"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Target selectors for scripts and bulk actions.
//
// A target is either one agent id, or a selector: "all", "tag:linux",
// "os:windows". Everything that runs against hosts took a single agent_id
// before, which made "patch every Linux box" a matter of clicking through the
// fleet by hand — and Tags were recorded at enrolment and then never used by
// anything.
const (
	targetAll = "all"
	tagPrefix = "tag:"
	osPrefix  = "os:"
)

// IsSelector reports whether target picks a set of hosts rather than naming one.
func IsSelector(target string) bool {
	return target == targetAll ||
		strings.HasPrefix(target, tagPrefix) ||
		strings.HasPrefix(target, osPrefix)
}

// resolveTarget expands a target into the agent ids it refers to.
//
// Only online hosts are returned: a script cannot run on a machine that is not
// connected, and silently counting offline hosts as "targeted" would report
// successes that never happened.
func resolveTarget(target string, views []protocol.HostView) []string {
	if !IsSelector(target) {
		return []string{target} // a plain agent id, passed through unchanged
	}
	var out []string
	for _, v := range views {
		if !v.Online {
			continue
		}
		if matchesTarget(target, v) {
			out = append(out, v.AgentID)
		}
	}
	return out
}

// matchesTarget reports whether one host satisfies a selector.
func matchesTarget(target string, v protocol.HostView) bool {
	switch {
	case target == targetAll:
		return true
	case strings.HasPrefix(target, osPrefix):
		return strings.EqualFold(v.OS, strings.TrimPrefix(target, osPrefix))
	case strings.HasPrefix(target, tagPrefix):
		return hasTag(v.Tags, strings.TrimPrefix(target, tagPrefix))
	}
	return false
}

// hasTag reports whether a host's tag string contains tag.
//
// Tags are a free-text field entered at enrolment, so they arrive as whatever
// someone typed: "prod, linux" or "prod linux" or "Prod,Linux". Split on both
// separators and compare case-insensitively, because the alternative is a
// selector that silently matches nothing and looks like the host was skipped.
func hasTag(tags, tag string) bool {
	tag = strings.TrimSpace(strings.ToLower(tag))
	if tag == "" {
		return false
	}
	for _, f := range strings.FieldsFunc(strings.ToLower(tags), func(r rune) bool {
		return r == ',' || r == ' ' || r == ';'
	}) {
		if strings.TrimSpace(f) == tag {
			return true
		}
	}
	return false
}

// knownTags lists every distinct tag across the fleet, for the dashboard's
// filter.
func knownTags(views []protocol.HostView) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range views {
		for _, f := range strings.FieldsFunc(v.Tags, func(r rune) bool {
			return r == ',' || r == ' ' || r == ';'
		}) {
			f = strings.TrimSpace(f)
			if f == "" || seen[strings.ToLower(f)] {
				continue
			}
			seen[strings.ToLower(f)] = true
			out = append(out, f)
		}
	}
	return out
}

// handleTags lists the tags in use across the fleet, so the dashboard can offer
// them rather than making someone remember what they typed at enrolment.
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, knownTags(s.store.views()))
}
