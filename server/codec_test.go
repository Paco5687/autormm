package server

import (
	"testing"

	"github.com/Paco5687/autormm/internal/protocol"
)

func TestPickCodec(t *testing.T) {
	cases := []struct {
		name          string
		client, agent []string
		want          string
	}{
		{"both h264", []string{"webcodecs-h264", "jpeg-tile"}, []string{"webcodecs-h264", "jpeg-tile"}, protocol.CapH264},
		{"client jpeg only", []string{"jpeg-tile"}, []string{"webcodecs-h264", "jpeg-tile"}, protocol.CapJPEGTile},
		{"agent jpeg only", []string{"webcodecs-h264", "jpeg-tile"}, []string{"jpeg-tile"}, protocol.CapJPEGTile},
		{"no client caps", nil, []string{"jpeg-tile"}, protocol.CapJPEGTile},
		{"empty both", nil, nil, protocol.CapJPEGTile},
	}
	for _, c := range cases {
		if got := pickCodec(c.client, c.agent); got != c.want {
			t.Errorf("%s: pickCodec(%v,%v)=%q want %q", c.name, c.client, c.agent, got, c.want)
		}
	}
}
