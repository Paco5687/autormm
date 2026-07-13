package protocol

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	tiles := []Tile{
		{TX: 0, TY: 0, JPEG: []byte("first-tile-bytes")},
		{TX: 3, TY: 7, JPEG: []byte{0xff, 0xd8, 0xff, 0x00, 0x11}},
	}
	enc := EncodeFrame(true, 1920, 1080, 128, tiles)

	f, err := DecodeFrame(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !f.Keyframe || f.Width != 1920 || f.Height != 1080 || f.Tile != 128 {
		t.Fatalf("bad header: %+v", f)
	}
	if len(f.Tiles) != 2 {
		t.Fatalf("want 2 tiles, got %d", len(f.Tiles))
	}
	if f.Tiles[1].TX != 3 || f.Tiles[1].TY != 7 || !bytes.Equal(f.Tiles[1].JPEG, tiles[1].JPEG) {
		t.Fatalf("tile mismatch: %+v", f.Tiles[1])
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := DecodeFrame([]byte{0x00, 0x01}); err == nil {
		t.Fatal("expected error on short/garbage input")
	}
	if _, err := DecodeFrame([]byte{FrameMagic, FrameDelta, 0, 1, 0, 1, 0, 1, 0, 5}); err == nil {
		t.Fatal("expected error on truncated tile")
	}
}
