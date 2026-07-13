package server

import "github.com/Paco5687/autormm/internal/protocol"

// pickCodec chooses the best video codec both the viewer (clientCaps) and the
// agent (agentCaps) support. Preference order is most-capable first; JPEG-tile
// is always the guaranteed fallback.
func pickCodec(clientCaps, agentCaps []string) string {
	for _, pref := range []string{protocol.CapH264} {
		if contains(clientCaps, pref) && contains(agentCaps, pref) {
			return pref
		}
	}
	return protocol.CapJPEGTile
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
