package vpl

import (
	"fmt"
	"math"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/vpl/sys"
)

const (
	// defaultQP is the constant quantiser used when neither a bitrate nor
	// a quality was requested.
	defaultQP = 26
	// refDistWithBFrames is mfxInfoMFX.GopRefDist for WithBFrames: two
	// consecutive B-frames between reference pictures.
	refDistWithBFrames = 3
	// maxDimension is the largest picture side mfxFrameInfo can describe
	// after alignment.
	maxDimension = 65535 / surfaceAlign * surfaceAlign
)

func codecID(c codec.Codec) (uint32, bool) {
	switch c {
	case codec.H264:
		return sys.CodecAVC, true
	case codec.HEVC:
		return sys.CodecHEVC, true
	}
	return 0, false
}

// frameRateFraction turns fps into a numerator/denominator pair; 30/1 when
// the rate is unknown.
func frameRateFraction(fps float64) (num, den uint32) {
	switch {
	case fps <= 0:
		return 30, 1
	case fps == math.Trunc(fps) && fps < 1<<24:
		return uint32(fps), 1
	}
	if n := math.Round(fps * 1001); n < 1<<30 {
		return uint32(n), 1001
	}
	return uint32(math.Round(fps)), 1
}

// encodeParams is the encoder configuration in VPL terms: the video
// parameters and the coding-option extension buffers attached to them.
type encodeParams struct {
	par  sys.VideoParam
	co   sys.ExtCodingOption
	co2  sys.ExtCodingOption2
	co3  sys.ExtCodingOption3
	ext  [3]unsafe.Pointer // mfxExtBuffer *[], referenced from par.ExtParam
	next int               // number of buffers in ext
}

// attach links the extension buffers into par. The structure must not be
// copied afterwards.
func (p *encodeParams) attach() {
	p.ext[0] = unsafe.Pointer(&p.co)
	p.ext[1] = unsafe.Pointer(&p.co2)
	p.next = 2
	if p.co3.Header.BufferID != 0 {
		// Last, so that it is the first one to go.
		p.ext[2] = unsafe.Pointer(&p.co3)
		p.next = 3
	}
	p.par.ExtParam = unsafe.Pointer(&p.ext[0])
	p.par.NumExtParam = uint16(p.next)
}

// detach removes the extension buffers.
func (p *encodeParams) detach() {
	p.par.ExtParam = nil
	p.par.NumExtParam = 0
}

// reduce drops extension buffers for a runtime that rejected the
// configuration: first mfxExtCodingOption3 alone, then all of them. It
// reports false when nothing is left to drop.
func (p *encodeParams) reduce() bool {
	switch {
	case p.par.NumExtParam > 2:
		p.par.NumExtParam = 2
	case p.par.NumExtParam > 0:
		p.detach()
	default:
		return false
	}
	return true
}

// fill maps the public encoder controls to mfxVideoParam. It reports
// ErrUnsupported for combinations VPL cannot express.
func (p *encodeParams) fill(cfg codec.EncoderConfig) error {
	fail := func(reason string) error { return unsupportedEncode(cfg.Codec, reason) }
	id, ok := codecID(cfg.Codec)
	if !ok {
		return fail("only h264 and hevc encoding are implemented on the vpl backend")
	}
	if cfg.InputFormat != codec.NV12 {
		return fail("input format " + cfg.InputFormat.String() + " is not available on the vpl backend yet; use NV12")
	}
	if cfg.Width%2 != 0 || cfg.Height%2 != 0 {
		return fail(fmt.Sprintf("picture size %dx%d: 4:2:0 encoding needs even dimensions", cfg.Width, cfg.Height))
	}
	if cfg.Width > maxDimension || cfg.Height > maxDimension {
		return fail(fmt.Sprintf("picture size %dx%d is out of range", cfg.Width, cfg.Height))
	}

	*p = encodeParams{}
	p.par.AsyncDepth = 1 // every frame is synchronised before the next one
	p.par.IOPattern = sys.IOPatternInSystemMem
	m := &p.par.MFX
	m.CodecID = id
	m.TargetUsage = sys.TargetUsageBalanced

	bframes := cfg.BFrames && !cfg.LowLatency
	switch cfg.Codec {
	case codec.H264:
		switch cfg.Profile {
		case codec.ProfileBaseline:
			// Hardware encoders implement the constrained subset.
			m.CodecProfile = sys.ProfileAVCConstrainedBaseline
			bframes = false
		case codec.ProfileMain:
			m.CodecProfile = sys.ProfileAVCMain
		default:
			m.CodecProfile = sys.ProfileAVCHigh
		}
		// 0: every I-frame is an IDR picture.
		m.IdrInterval = 0
	case codec.HEVC:
		switch cfg.Profile {
		case codec.ProfileDefault, codec.ProfileMain:
			m.CodecProfile = sys.ProfileHEVCMain
		default:
			return fail("profile " + cfg.Profile.String() + " is not defined for hevc")
		}
		// For HEVC 0 means "only the first frame is IDR" and 1 means
		// "every I-frame is IDR".
		m.IdrInterval = 1
	}

	fi := &m.FrameInfo
	fi.FourCC = sys.FourCCNV12
	fi.ChromaFormat = sys.ChromaFormatYUV420
	fi.PicStruct = sys.PicStructProgressive
	fi.Width = uint16(alignUp(cfg.Width, surfaceAlign))
	fi.Height = uint16(alignUp(cfg.Height, surfaceAlign))
	fi.CropW = uint16(cfg.Width)
	fi.CropH = uint16(cfg.Height)
	fi.FrameRateExtN, fi.FrameRateExtD = frameRateFraction(cfg.FrameRate)

	gop := cfg.KeyframeInterval
	switch {
	case gop > 0:
	case cfg.FrameRate > 0:
		gop = int(cfg.FrameRate*2 + 0.5)
	default:
		gop = 60
	}
	m.GopPicSize = uint16(min(gop, math.MaxUint16))
	m.GopOptFlag = sys.GopClosed
	m.GopRefDist = 1
	if bframes {
		m.GopRefDist = refDistWithBFrames
	}

	switch {
	case cfg.Quality > 0:
		setConstQP(m, min(max(int(math.Round(51-45*cfg.Quality)), 1), 51))
	case cfg.Bitrate > 0:
		target := max((cfg.Bitrate+500)/1000, 1)
		peak := target
		m.RateControlMethod = sys.RateControlCBR
		if cfg.RateControl != codec.CBR {
			m.RateControlMethod = sys.RateControlVBR
			peak = 2 * target
		}
		// The rate fields are 16 bits wide; larger rates are expressed
		// with a common multiplier.
		mult := (peak + math.MaxUint16 - 1) / math.MaxUint16
		if mult > math.MaxUint16 {
			return fail(fmt.Sprintf("bitrate %d is out of range", cfg.Bitrate))
		}
		m.BRCParamMultiplier = uint16(mult)
		m.TargetKbps = uint16(max(target/mult, 1))
		m.MaxKbps = uint16(max(peak/mult, 1))
		if cfg.RateControl == codec.CBR {
			m.MaxKbps = m.TargetKbps
		}
	default:
		setConstQP(m, defaultQP)
	}

	p.co.Header = sys.ExtBuffer{BufferID: sys.ExtBuffCodingOption, BufferSz: uint32(unsafe.Sizeof(p.co))}
	p.co.AUDelimiter = sys.CodingOptionOff
	p.co2.Header = sys.ExtBuffer{BufferID: sys.ExtBuffCodingOption2, BufferSz: uint32(unsafe.Sizeof(p.co2))}
	// Keyframes are placed by GopPicSize and Frame.ForceKeyframe only, and
	// the parameter sets belong in front of keyframes, not every frame.
	p.co2.AdaptiveI = sys.CodingOptionOff
	p.co2.AdaptiveB = sys.CodingOptionOff
	p.co2.RepeatPPS = sys.CodingOptionOff
	if bframes {
		// Plain B-frames: with a B-pyramid the reorder depth (and so the
		// DTS offset) would depend on the runtime's choice.
		p.co2.BRefType = sys.BRefOff
	}
	if cfg.Codec == codec.HEVC {
		// HEVC encoders code "P" pictures as generalised B pictures by
		// default; plain P pictures keep "no B-frames" literally true.
		p.co3.Header = sys.ExtBuffer{BufferID: sys.ExtBuffCodingOption3, BufferSz: uint32(unsafe.Sizeof(p.co3))}
		p.co3.GPB = sys.CodingOptionOff
	}
	p.attach()
	return nil
}

// setConstQP selects constant-quantiser rate control. QPI, QPP and QPB
// share their storage with the bitrate fields.
func setConstQP(m *sys.InfoMFX, qp int) {
	m.RateControlMethod = sys.RateControlCQP
	m.InitialDelayInKB = uint16(qp) // QPI
	m.TargetKbps = uint16(qp)       // QPP
	m.MaxKbps = uint16(qp)          // QPB
}

// bitstreamCapacity is the size of the encoder's output buffer: the size
// the runtime reports for one coded frame, and never less than a raw frame.
func bitstreamCapacity(got *sys.InfoMFX) int {
	n := int(got.BufferSizeInKB) * 1000 * max(int(got.BRCParamMultiplier), 1)
	raw := int(got.FrameInfo.Width) * int(got.FrameInfo.Height) * 3 / 2
	return max(n, raw, 1<<16)
}

// withStartCode returns a parameter-set NAL unit as an Annex-B unit; the
// runtime returns them with a start code, older ones without.
func withStartCode(nal []byte) []byte {
	if len(nal) == 0 {
		return nil
	}
	if len(nal) >= 4 && nal[0] == 0 && nal[1] == 0 && (nal[2] == 1 || (nal[2] == 0 && nal[3] == 1)) {
		return nal
	}
	return append([]byte{0, 0, 0, 1}, nal...)
}
