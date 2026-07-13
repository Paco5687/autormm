package capture

import (
	"bytes"
	"hash/fnv"
	"image"
	"image/jpeg"
	"sync"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Streamer is the JPEG-tile Encoder: it sends only the tiles that changed since
// the previous frame (delta encoding), as codec-0 media messages.
type Streamer struct {
	tile int
	prev map[uint32]uint64 // tileKey -> content hash
	w, h int

	qmu     sync.Mutex
	quality int
}

// NewStreamer builds a JPEG-tile encoder. tile is the square tile size in px;
// quality is JPEG quality (1-100).
func NewStreamer(tile, quality int) *Streamer {
	if tile <= 0 {
		tile = 128
	}
	if quality <= 0 || quality > 100 {
		quality = 60
	}
	return &Streamer{tile: tile, quality: quality, prev: map[uint32]uint64{}}
}

// Close satisfies the Encoder interface.
func (s *Streamer) Close() error { return nil }

// SetQuality adjusts JPEG quality for subsequent frames. Safe to call from a
// different goroutine than Next.
func (s *Streamer) SetQuality(q int) {
	if q >= 1 && q <= 100 {
		s.qmu.Lock()
		s.quality = q
		s.qmu.Unlock()
	}
}

func (s *Streamer) currentQuality() int {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	return s.quality
}

// Encode turns a captured frame into a JPEG-tile media message (codec 0),
// sending only changed tiles. Returns nil when nothing changed. force (or a
// resolution change) makes every tile a keyframe.
func (s *Streamer) Encode(img *image.RGBA, force bool) ([][]byte, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w != s.w || h != s.h {
		s.prev = map[uint32]uint64{}
		s.w, s.h = w, h
		force = true
	}

	q := s.currentQuality()
	var tiles []protocol.Tile
	cols := (w + s.tile - 1) / s.tile
	rows := (h + s.tile - 1) / s.tile
	for ty := 0; ty < rows; ty++ {
		for tx := 0; tx < cols; tx++ {
			x0 := b.Min.X + tx*s.tile
			y0 := b.Min.Y + ty*s.tile
			x1 := min(x0+s.tile, b.Max.X)
			y1 := min(y0+s.tile, b.Max.Y)
			rect := image.Rect(x0, y0, x1, y1)
			key := uint32(ty)<<16 | uint32(tx)
			hash := hashRegion(img, rect)
			if !force && s.prev[key] == hash {
				continue
			}
			s.prev[key] = hash
			jpg, err := encodeJPEG(img, rect, q)
			if err != nil {
				return nil, err
			}
			tiles = append(tiles, protocol.Tile{TX: uint16(tx), TY: uint16(ty), JPEG: jpg})
		}
	}
	if !force && len(tiles) == 0 {
		return nil, nil // nothing changed; caller skips this tick
	}
	frame := protocol.EncodeFrame(force, uint16(w), uint16(h), uint16(s.tile), tiles)
	return [][]byte{protocol.WrapMedia(protocol.MediaJPEGTile, frame)}, nil
}

func encodeJPEG(img *image.RGBA, rect image.Rectangle, q int) ([]byte, error) {
	sub := img.SubImage(rect)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, sub, &jpeg.Options{Quality: q}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func hashRegion(img *image.RGBA, rect image.Rectangle) uint64 {
	h := fnv.New64a()
	rowLen := (rect.Max.X - rect.Min.X) * 4
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		off := img.PixOffset(rect.Min.X, y)
		h.Write(img.Pix[off : off+rowLen])
	}
	return h.Sum64()
}
