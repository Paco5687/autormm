package capture

import "testing"

// nal builds a NAL: start code + header byte + n zero payload bytes.
func nal(hdr byte, n int) []byte {
	return append([]byte{0, 0, 1, hdr}, make([]byte, n)...)
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestAUSplitterAndKeyframe(t *testing.T) {
	// AU1: AUD(9) + SPS(0x67) + PPS(0x68) + IDR(0x65) -> keyframe
	au1 := cat(nal(0x09, 1), nal(0x67, 4), nal(0x68, 2), nal(0x65, 10))
	// AU2/AU3: AUD + non-IDR slice(0x41) -> delta
	au2 := cat(nal(0x09, 1), nal(0x41, 8))
	au3 := cat(nal(0x09, 1), nal(0x41, 8))
	stream := cat(au1, au2, au3)

	s := &auSplitter{}
	var got [][]byte
	mid := len(stream) / 2 // feed in two chunks to exercise cross-read buffering
	got = append(got, s.push(stream[:mid])...)
	got = append(got, s.push(stream[mid:])...)
	if f := s.flush(); f != nil {
		got = append(got, f)
	}

	if len(got) != 3 {
		t.Fatalf("want 3 access units, got %d", len(got))
	}
	if !auIsKeyframe(got[0]) {
		t.Error("AU1 (SPS+IDR) should be a keyframe")
	}
	if auIsKeyframe(got[1]) || auIsKeyframe(got[2]) {
		t.Error("AU2/AU3 (non-IDR) should be delta")
	}
}
