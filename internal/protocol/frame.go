package protocol

import (
	"encoding/binary"
	"errors"
)

// Binary screen-frame wire format (agent -> viewer), big-endian:
//
//	u8  magic = 0xAA
//	u8  kind  : FrameKeyframe | FrameDelta
//	u16 width
//	u16 height
//	u16 tile  (tile size in pixels)
//	u16 count (number of tiles that follow)
//	count x {
//	  u16 tx   (tile column)
//	  u16 ty   (tile row)
//	  u32 len  (JPEG byte length)
//	  len bytes JPEG
//	}
const (
	FrameMagic    = 0xAA
	FrameKeyframe = 1
	FrameDelta    = 2
)

// Tile is one JPEG-encoded square of the screen at grid position (TX, TY).
type Tile struct {
	TX, TY uint16
	JPEG   []byte
}

// EncodeFrame serialises a frame to a single binary WebSocket message.
func EncodeFrame(keyframe bool, width, height, tile uint16, tiles []Tile) []byte {
	size := 10
	for _, t := range tiles {
		size += 8 + len(t.JPEG)
	}
	buf := make([]byte, 0, size)
	kind := byte(FrameDelta)
	if keyframe {
		kind = FrameKeyframe
	}
	buf = append(buf, FrameMagic, kind)
	buf = be16(buf, width)
	buf = be16(buf, height)
	buf = be16(buf, tile)
	buf = be16(buf, uint16(len(tiles)))
	for _, t := range tiles {
		buf = be16(buf, t.TX)
		buf = be16(buf, t.TY)
		buf = be32(buf, uint32(len(t.JPEG)))
		buf = append(buf, t.JPEG...)
	}
	return buf
}

// DecodedFrame is the parsed form (used by tests; the browser viewer parses in JS).
type DecodedFrame struct {
	Keyframe bool
	Width    uint16
	Height   uint16
	Tile     uint16
	Tiles    []Tile
}

// DecodeFrame parses a binary frame message.
func DecodeFrame(b []byte) (*DecodedFrame, error) {
	if len(b) < 10 || b[0] != FrameMagic {
		return nil, errors.New("protocol: bad frame header")
	}
	f := &DecodedFrame{
		Keyframe: b[1] == FrameKeyframe,
		Width:    binary.BigEndian.Uint16(b[2:]),
		Height:   binary.BigEndian.Uint16(b[4:]),
		Tile:     binary.BigEndian.Uint16(b[6:]),
	}
	count := int(binary.BigEndian.Uint16(b[8:]))
	off := 10
	for i := 0; i < count; i++ {
		if off+8 > len(b) {
			return nil, errors.New("protocol: truncated tile header")
		}
		tx := binary.BigEndian.Uint16(b[off:])
		ty := binary.BigEndian.Uint16(b[off+2:])
		n := int(binary.BigEndian.Uint32(b[off+4:]))
		off += 8
		if off+n > len(b) {
			return nil, errors.New("protocol: truncated tile data")
		}
		f.Tiles = append(f.Tiles, Tile{TX: tx, TY: ty, JPEG: append([]byte(nil), b[off:off+n]...)})
		off += n
	}
	return f, nil
}

func be16(b []byte, v uint16) []byte { return append(b, byte(v>>8), byte(v)) }
func be32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}
