package mediafoundation

import (
	"fmt"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// codecFrame is the raw frame type; aliased so the pure-Go helpers read well.
type codecFrame = codec.Frame

// checkFrame validates a frame against the encoder configuration.
func checkFrame(cfg *codec.EncoderConfig, f *codec.Frame) error {
	switch {
	case f == nil:
		return fmt.Errorf("%w: nil frame", codec.ErrInvalidData)
	case f.Format != cfg.InputFormat:
		return fmt.Errorf("%w: frame format %s, encoder expects %s", codec.ErrInvalidData, f.Format, cfg.InputFormat)
	case f.Width != cfg.Width || f.Height != cfg.Height:
		return fmt.Errorf("%w: frame size %dx%d, encoder expects %dx%d", codec.ErrInvalidData, f.Width, f.Height, cfg.Width, cfg.Height)
	case len(f.Planes) != 2 || len(f.Strides) != 2:
		return fmt.Errorf("%w: NV12 frame needs 2 planes and 2 strides, got %d and %d", codec.ErrInvalidData, len(f.Planes), len(f.Strides))
	}
	rows := [2]int{f.Height, (f.Height + 1) / 2}
	rowBytes := [2]int{f.Width, (f.Width + 1) / 2 * 2}
	for i := range f.Planes {
		if f.Strides[i] < rowBytes[i] {
			return fmt.Errorf("%w: plane %d stride %d is smaller than its row of %d bytes", codec.ErrInvalidData, i, f.Strides[i], rowBytes[i])
		}
		if need := (rows[i]-1)*f.Strides[i] + rowBytes[i]; len(f.Planes[i]) < need {
			return fmt.Errorf("%w: plane %d holds %d bytes, need %d", codec.ErrInvalidData, i, len(f.Planes[i]), need)
		}
	}
	return nil
}

// finishPacket turns an Annex-B access unit produced by the encoder into a
// Packet: it records the parameter sets it carries, marks keyframes from the
// NAL unit types, and prepends the stored VPS/SPS/PPS to keyframes that lack
// them so every keyframe is self-contained.
func finishPacket(c codec.Codec, params *paramSets, data []byte, pts, dts int64) codec.Packet {
	var kinds [3]bool
	keyframe := false
	for _, nal := range annexb.Split(data) {
		t := annexb.NALUnitType(c, nal)
		switch {
		case t < 0:
		case annexb.IsParameterSet(c, t):
			if kind, ok := params.add(t, nal); ok {
				kinds[kind] = true
			}
		case annexb.IsVCL(c, t) && annexb.IsKeyframe(c, t):
			keyframe = true
		}
	}
	if keyframe && !params.covers(kinds) && !params.empty() {
		data = append(params.annexB(), data...)
	}
	return codec.Packet{Data: data, PTS: pts, DTS: dts, Keyframe: keyframe}
}

// defaultBitrate is used when neither a bitrate nor a quality was requested:
// 0.1 bit per pixel per frame, at least 200 kbit/s.
func defaultBitrate(width, height int, fps float64) int {
	if fps <= 0 {
		fps = 30
	}
	b := int(float64(width*height) * fps * 0.1)
	if b < 200_000 {
		b = 200_000
	}
	return b
}
