//go:build linux

package vpl

import (
	"context"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/vpl/sys"
)

// Backend is the Intel VPL backend.
type Backend struct{}

// Name implements codec.Backend.
func (Backend) Name() string { return Name }

// probeCodecs are the codecs with both a decode and an encode path here;
// decodeCodecs adds AV1, which is decoded only.
var (
	probeCodecs  = []codec.Codec{codec.H264, codec.HEVC}
	decodeCodecs = []codec.Codec{codec.H264, codec.HEVC, codec.AV1}
)

// decodeCodecID returns the MFX codec identifier of a codec the backend
// decodes.
func decodeCodecID(c codec.Codec) (uint32, bool) {
	if c == codec.AV1 {
		return sys.CodecAV1, true
	}
	return codecID(c)
}

// probeWidth and probeHeight are the picture size the capability queries
// are made with (1080p, coded height aligned to 16).
const (
	probeWidth       = 1920
	probeHeight      = 1080
	probeCodedHeight = 1088
)

func probeProfile(c codec.Codec) uint16 {
	switch c {
	case codec.HEVC:
		return sys.ProfileHEVCMain
	case codec.AV1:
		return sys.ProfileAV1Main
	}
	return sys.ProfileAVCHigh
}

// supported interprets the status of a Query call: errors mean the codec is
// not available, and partial acceleration means it is not done in hardware.
func supported(st int32) bool {
	return st >= 0 && st != sys.WrnPartialAcceleration
}

// canDecode asks the runtime whether it decodes 8-bit 4:2:0 streams of c in
// hardware.
func (s *session) canDecode(c codec.Codec) bool {
	id, _ := decodeCodecID(c)
	var in, out sys.VideoParam
	in.IOPattern = sys.IOPatternOutSystemMem
	in.MFX.CodecID = id
	in.MFX.CodecProfile = probeProfile(c)
	fi := &in.MFX.FrameInfo
	fi.FourCC = sys.FourCCNV12
	fi.ChromaFormat = sys.ChromaFormatYUV420
	fi.PicStruct = sys.PicStructProgressive
	fi.Width, fi.Height = probeWidth, probeCodedHeight
	fi.CropW, fi.CropH = probeWidth, probeHeight
	// The runtime selects the codec by the output structure's id.
	out.MFX.CodecID = id
	return supported(sys.DecodeQuery(s.ses, &in, &out))
}

// canEncode asks the runtime whether it encodes c in hardware.
func (s *session) canEncode(c codec.Codec) bool {
	var p encodeParams
	if err := p.fill(codec.EncoderConfig{Codec: c, Width: probeWidth, Height: probeHeight, InputFormat: codec.NV12, TimeScale: 90000, FrameRate: 30}); err != nil {
		return false
	}
	p.detach()
	var out sys.VideoParam
	out.MFX.CodecID = p.par.MFX.CodecID
	return supported(sys.EncodeQuery(s.ses, &p.par, &out))
}

// Probe implements codec.Backend. It reports hardware decode support for
// H.264, HEVC and AV1 (Tiger Lake and newer GPUs decode AV1) and encode
// support for H.264 and HEVC. Without libvpl, libva, an Intel GPU or a GPU
// runtime for it, it yields (nil, nil).
func (Backend) Probe(ctx context.Context) ([]codec.Capability, error) {
	s, err := openSession()
	if err != nil {
		if notAvailable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer s.close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var caps []codec.Capability
	for _, c := range decodeCodecs {
		if s.canDecode(c) {
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Decode, Hardware: true})
		}
	}
	for _, c := range probeCodecs {
		if s.canEncode(c) {
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Encode, Hardware: true})
		}
	}
	return caps, nil
}

// NewDecoder implements codec.Backend.
func (Backend) NewDecoder(ctx context.Context, cfg codec.DecoderConfig) (codec.Decoder, error) {
	id, ok := decodeCodecID(cfg.Codec)
	if !ok {
		return nil, unsupported(cfg.Codec, "only h264, hevc and av1 decoding are implemented on the vpl backend")
	}
	if cfg.OutputFormat != codec.NV12 {
		return nil, unsupported(cfg.Codec, "output format "+cfg.OutputFormat.String()+" is not available on the vpl backend yet; use NV12")
	}
	s, err := openSession()
	if err != nil {
		if notAvailable(err) {
			return nil, unsupported(cfg.Codec, err.Error())
		}
		return nil, err
	}
	if !s.canDecode(cfg.Codec) {
		desc := s.describe()
		s.close()
		return nil, unsupported(cfg.Codec, "the GPU runtime ("+desc+") has no hardware decoder for "+cfg.Codec.String())
	}
	if err := ctx.Err(); err != nil {
		s.close()
		return nil, err
	}
	return newDecoder(cfg, s, id), nil
}

// NewEncoder implements codec.Backend.
func (Backend) NewEncoder(ctx context.Context, cfg codec.EncoderConfig) (codec.Encoder, error) {
	// Reject what VPL cannot express before any library is touched.
	var p encodeParams
	if err := p.fill(cfg); err != nil {
		return nil, err
	}
	s, err := openSession()
	if err != nil {
		if notAvailable(err) {
			return nil, unsupportedEncode(cfg.Codec, err.Error())
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		s.close()
		return nil, err
	}
	e, err := newEncoder(cfg, s)
	if err != nil {
		s.close()
		return nil, err
	}
	return e, nil
}
