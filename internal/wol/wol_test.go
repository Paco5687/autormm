package wol

import (
	"bytes"
	"testing"
)

func TestMagicPacket(t *testing.T) {
	pkt, err := MagicPacket("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkt) != 102 {
		t.Fatalf("packet is %d bytes, want 102", len(pkt))
	}
	if !bytes.Equal(pkt[:6], bytes.Repeat([]byte{0xFF}, 6)) {
		t.Error("packet does not start with six 0xFF bytes")
	}
	mac := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	for i := 0; i < 16; i++ {
		if !bytes.Equal(pkt[6+i*6:12+i*6], mac) {
			t.Fatalf("MAC repetition %d is wrong", i)
		}
	}
}

// The MACs come from HostFacts, which records whatever the OS reported — so
// every common textual form must work, and junk must not panic.
func TestMagicPacketFormats(t *testing.T) {
	for _, ok := range []string{"aa:bb:cc:dd:ee:ff", "AA-BB-CC-DD-EE-FF", "aabb.ccdd.eeff"} {
		if _, err := MagicPacket(ok); err != nil {
			t.Errorf("MagicPacket(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "not-a-mac", "aa:bb:cc:dd:ee", "aa:bb:cc:dd:ee:ff:00:11"} {
		if _, err := MagicPacket(bad); err == nil {
			t.Errorf("MagicPacket(%q) accepted junk", bad)
		}
	}
}
