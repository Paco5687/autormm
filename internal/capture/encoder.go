package capture

import (
	"image"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Encoder turns captured frames into codec-tagged media messages for the viewer.
// Implementations: the JPEG-tile Streamer (always available) and, when the host
// has ffmpeg, an H.264 encoder. Encode returns zero or more ready-to-send media
// messages (each already prefixed with its MediaCodec byte); the H.264 pipeline
// is asynchronous, so a call may return messages for earlier frames.
type Encoder interface {
	Encode(img *image.RGBA, force bool) ([][]byte, error)
	SetQuality(q int) // JPEG quality / codec quality hint
	Close() error
}

// EncoderCaps lists the codecs this host can produce (JPEG-tile always; H.264
// when ffmpeg is available).
func EncoderCaps() []string {
	caps := []string{protocol.CapJPEGTile}
	if ffmpegPath() != "" {
		caps = append(caps, protocol.CapH264)
	}
	return caps
}

// NewEncoder builds the encoder for a codec, falling back to JPEG-tile.
func NewEncoder(codec string, tile, quality, fps int) (Encoder, error) {
	if codec == protocol.CapH264 {
		if enc, err := newH264Encoder(quality, fps); err == nil {
			return enc, nil
		}
		// fall through to JPEG-tile if H.264 can't start
	}
	return NewStreamer(tile, quality), nil
}
