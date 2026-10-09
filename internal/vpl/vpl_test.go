package vpl

import (
	"bytes"
	"errors"
	"testing"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/bitstream/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/vpl/sys"
)

func baseConfig(c codec.Codec) codec.EncoderConfig {
	return codec.EncoderConfig{Codec: c, Width: 1920, Height: 1080, InputFormat: codec.NV12, TimeScale: 90000, RateControl: codec.VBR}
}

func TestEncodeParamsDefaults(t *testing.T) {
	var p encodeParams
	if err := p.fill(baseConfig(codec.H264)); err != nil {
		t.Fatal(err)
	}
	m := &p.par.MFX
	fi := &m.FrameInfo
	if m.CodecID != sys.CodecAVC || m.CodecProfile != sys.ProfileAVCHigh {
		t.Errorf("codec %#x profile %d", m.CodecID, m.CodecProfile)
	}
	if fi.Width != 1920 || fi.Height != 1088 || fi.CropW != 1920 || fi.CropH != 1080 {
		t.Errorf("coded size %dx%d crop %dx%d, want 1920x1088 and 1920x1080", fi.Width, fi.Height, fi.CropW, fi.CropH)
	}
	if fi.FourCC != sys.FourCCNV12 || fi.ChromaFormat != sys.ChromaFormatYUV420 || fi.PicStruct != sys.PicStructProgressive {
		t.Errorf("frame format %#x chroma %d picstruct %d", fi.FourCC, fi.ChromaFormat, fi.PicStruct)
	}
	if fi.FrameRateExtN != 30 || fi.FrameRateExtD != 1 {
		t.Errorf("default frame rate %d/%d", fi.FrameRateExtN, fi.FrameRateExtD)
	}
	if m.GopPicSize != 60 || m.GopRefDist != 1 || m.IdrInterval != 0 {
		t.Errorf("gop %d refdist %d idr interval %d", m.GopPicSize, m.GopRefDist, m.IdrInterval)
	}
	// Constant QP 26 in the fields shared with the bitrate controls.
	if m.RateControlMethod != sys.RateControlCQP || m.InitialDelayInKB != 26 || m.TargetKbps != 26 || m.MaxKbps != 26 {
		t.Errorf("rate control %d qp %d/%d/%d", m.RateControlMethod, m.InitialDelayInKB, m.TargetKbps, m.MaxKbps)
	}
	if p.par.IOPattern != sys.IOPatternInSystemMem || p.par.AsyncDepth != 1 {
		t.Errorf("io pattern %#x async depth %d", p.par.IOPattern, p.par.AsyncDepth)
	}
	if p.par.NumExtParam != 2 || p.par.ExtParam != unsafe.Pointer(&p.ext[0]) || p.ext[0] != unsafe.Pointer(&p.co) || p.ext[1] != unsafe.Pointer(&p.co2) {
		t.Error("extension buffers are not attached")
	}
	if p.co3.Header.BufferID != 0 {
		t.Error("H.264 needs no mfxExtCodingOption3")
	}
	if p.co.Header.BufferID != sys.ExtBuffCodingOption || p.co.Header.BufferSz != 64 || p.co2.Header.BufferID != sys.ExtBuffCodingOption2 || p.co2.Header.BufferSz != 68 {
		t.Errorf("extension buffer headers %+v %+v", p.co.Header, p.co2.Header)
	}
	if p.co2.AdaptiveI != sys.CodingOptionOff || p.co2.BRefType != 0 {
		t.Errorf("AdaptiveI %#x BRefType %d", p.co2.AdaptiveI, p.co2.BRefType)
	}
	if !p.reduce() || p.par.NumExtParam != 0 || p.par.ExtParam != nil {
		t.Error("reduce left the extension buffers attached")
	}
	if p.reduce() {
		t.Error("reduce reports progress with nothing left to drop")
	}
}

func TestEncodeParamsControls(t *testing.T) {
	cfg := baseConfig(codec.HEVC)
	cfg.FrameRate = 29.97
	cfg.KeyframeInterval = 48
	cfg.BFrames = true
	cfg.Bitrate = 4_000_000
	var p encodeParams
	if err := p.fill(cfg); err != nil {
		t.Fatal(err)
	}
	m := &p.par.MFX
	if m.CodecID != sys.CodecHEVC || m.CodecProfile != sys.ProfileHEVCMain {
		t.Errorf("codec %#x profile %d", m.CodecID, m.CodecProfile)
	}
	if m.IdrInterval != 1 {
		t.Errorf("HEVC IdrInterval %d, want 1 (every I-frame is IDR)", m.IdrInterval)
	}
	// Plain P pictures instead of generalised B, as the third and last
	// extension buffer so that it is dropped first.
	if p.par.NumExtParam != 3 || p.ext[2] != unsafe.Pointer(&p.co3) || p.co3.GPB != sys.CodingOptionOff ||
		p.co3.Header.BufferID != sys.ExtBuffCodingOption3 || p.co3.Header.BufferSz != 512 {
		t.Errorf("mfxExtCodingOption3: %d buffers, header %+v, GPB %#x", p.par.NumExtParam, p.co3.Header, p.co3.GPB)
	}
	if m.FrameInfo.FrameRateExtN != 30000 || m.FrameInfo.FrameRateExtD != 1001 {
		t.Errorf("frame rate %d/%d", m.FrameInfo.FrameRateExtN, m.FrameInfo.FrameRateExtD)
	}
	if m.GopPicSize != 48 || m.GopRefDist != 3 || p.co2.BRefType != sys.BRefOff {
		t.Errorf("gop %d refdist %d bref %d", m.GopPicSize, m.GopRefDist, p.co2.BRefType)
	}
	if m.RateControlMethod != sys.RateControlVBR || m.BRCParamMultiplier != 1 || m.TargetKbps != 4000 || m.MaxKbps != 8000 {
		t.Errorf("VBR: method %d mult %d target %d max %d", m.RateControlMethod, m.BRCParamMultiplier, m.TargetKbps, m.MaxKbps)
	}

	cfg.RateControl = codec.CBR
	cfg.LowLatency = true
	if err := p.fill(cfg); err != nil {
		t.Fatal(err)
	}
	if m.RateControlMethod != sys.RateControlCBR || m.TargetKbps != 4000 || m.MaxKbps != 4000 {
		t.Errorf("CBR: method %d target %d max %d", m.RateControlMethod, m.TargetKbps, m.MaxKbps)
	}
	if m.GopRefDist != 1 {
		t.Errorf("low latency kept B-frames (GopRefDist %d)", m.GopRefDist)
	}

	// Rates beyond 16 bits of kbit/s use the multiplier.
	cfg.RateControl = codec.VBR
	cfg.Bitrate = 100_000_000
	if err := p.fill(cfg); err != nil {
		t.Fatal(err)
	}
	mult := int(m.BRCParamMultiplier)
	if mult != 4 || int(m.TargetKbps)*mult != 100_000 || int(m.MaxKbps)*mult != 200_000 {
		t.Errorf("100 Mbit/s: mult %d target %d max %d", mult, m.TargetKbps, m.MaxKbps)
	}

	cfg = baseConfig(codec.H264)
	cfg.Quality = 0.8
	cfg.Profile = codec.ProfileBaseline
	cfg.BFrames = true
	if err := p.fill(cfg); err != nil {
		t.Fatal(err)
	}
	if m.RateControlMethod != sys.RateControlCQP || m.TargetKbps != 15 || m.InitialDelayInKB != 15 || m.MaxKbps != 15 {
		t.Errorf("quality 0.8: method %d qp %d, want CQP 15", m.RateControlMethod, m.TargetKbps)
	}
	if m.CodecProfile != sys.ProfileAVCConstrainedBaseline || m.GopRefDist != 1 {
		t.Errorf("baseline: profile %d refdist %d", m.CodecProfile, m.GopRefDist)
	}
}

func TestEncodeParamsUnsupported(t *testing.T) {
	cases := map[string]func(*codec.EncoderConfig){
		"av1":           func(c *codec.EncoderConfig) { c.Codec = codec.AV1 },
		"rgba input":    func(c *codec.EncoderConfig) { c.InputFormat = codec.RGBA },
		"odd width":     func(c *codec.EncoderConfig) { c.Width = 641 },
		"huge":          func(c *codec.EncoderConfig) { c.Width = 70000 },
		"hevc baseline": func(c *codec.EncoderConfig) { c.Codec = codec.HEVC; c.Profile = codec.ProfileBaseline },
		"hevc high":     func(c *codec.EncoderConfig) { c.Codec = codec.HEVC; c.Profile = codec.ProfileHigh },
	}
	for name, mod := range cases {
		cfg := baseConfig(codec.H264)
		mod(&cfg)
		var p encodeParams
		err := p.fill(cfg)
		var ue *codec.UnsupportedError
		if !errors.Is(err, codec.ErrUnsupported) || !errors.As(err, &ue) || ue.Backend != Name || ue.Direction != codec.Encode {
			t.Errorf("%s: got %v, want an encode UnsupportedError from %s", name, err, Name)
		}
	}
}

func TestBitstreamCapacity(t *testing.T) {
	var m sys.InfoMFX
	m.FrameInfo.Width, m.FrameInfo.Height = 320, 240
	if got := bitstreamCapacity(&m); got != 320*240*3/2 {
		t.Errorf("raw frame floor: %d", got)
	}
	m.BufferSizeInKB, m.BRCParamMultiplier = 500, 2
	if got := bitstreamCapacity(&m); got != 1_000_000 {
		t.Errorf("runtime size: %d", got)
	}
	m = sys.InfoMFX{}
	if got := bitstreamCapacity(&m); got != 1<<16 {
		t.Errorf("minimum: %d", got)
	}
}

func TestWithStartCode(t *testing.T) {
	if got := withStartCode([]byte{0x67, 1, 2}); !bytes.Equal(got, []byte{0, 0, 0, 1, 0x67, 1, 2}) {
		t.Errorf("bare NAL: % x", got)
	}
	for _, in := range [][]byte{{0, 0, 0, 1, 0x67}, {0, 0, 1, 0x67, 9}} {
		if got := withStartCode(in); !bytes.Equal(got, in) {
			t.Errorf("% x changed to % x", in, got)
		}
	}
	if withStartCode(nil) != nil {
		t.Error("empty NAL should stay empty")
	}
}

// TestSurfaceUpload checks the copy into an aligned surface: the picture in
// the top-left corner and its edges replicated into the padding.
func TestSurfaceUpload(t *testing.T) {
	const w, h = 18, 6
	var info sys.FrameInfo
	info.Width, info.Height = 32, 16
	s := newSurface(info)
	if s.pitch != 64 || s.width != 32 || s.height != 16 || len(s.buf) != 64*16*3/2 {
		t.Fatalf("surface %dx%d pitch %d, %d bytes", s.width, s.height, s.pitch, len(s.buf))
	}
	if s.s.Data.Y != uintptr(unsafe.Pointer(&s.buf[0])) || s.s.Data.UV != s.s.Data.Y+64*16 || s.s.Data.Pitch != 64 {
		t.Fatalf("surface pointers: Y %#x UV %#x pitch %d", s.s.Data.Y, s.s.Data.UV, s.s.Data.Pitch)
	}
	const lumaStride, chromaStride = 20, 22
	luma := make([]byte, lumaStride*h)
	chroma := make([]byte, chromaStride*h/2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			luma[y*lumaStride+x] = byte(1 + y*w + x)
		}
	}
	for y := 0; y < h/2; y++ {
		for x := 0; x < w; x++ {
			chroma[y*chromaStride+x] = byte(128 + y*w + x)
		}
	}
	s.upload(luma, lumaStride, chroma, chromaStride, w, h)
	for y := 0; y < s.height; y++ {
		for x := 0; x < s.width; x++ {
			want := luma[min(y, h-1)*lumaStride+min(x, w-1)]
			if got := s.buf[y*s.pitch+x]; got != want {
				t.Fatalf("luma (%d,%d) = %d, want %d", x, y, got, want)
			}
		}
	}
	for y := 0; y < s.height/2; y++ {
		for x := 0; x < s.width; x++ {
			sx := x
			if sx >= w {
				sx = w - 2 + x%2 // the last CbCr pair, component preserved
			}
			want := chroma[min(y, h/2-1)*chromaStride+sx]
			if got := s.buf[s.pitch*s.height+y*s.pitch+x]; got != want {
				t.Fatalf("chroma (%d,%d) = %d, want %d", x, y, got, want)
			}
		}
	}

	// The decoder's side: the visible rectangle comes back tightly packed.
	s.s.Info.CropX, s.s.Info.CropY, s.s.Info.CropW, s.s.Info.CropH = 2, 2, 14, 4
	x, y, cw, ch := s.cropRect()
	if x != 2 || y != 2 || cw != 14 || ch != 4 {
		t.Fatalf("cropRect = %d,%d %dx%d", x, y, cw, ch)
	}
	outLuma := make([]byte, cw*ch)
	outChroma := make([]byte, cw*ch/2)
	s.copyOut(outLuma, outChroma, x, y, cw, ch, cw, ch/2)
	for r := 0; r < ch; r++ {
		if !bytes.Equal(outLuma[r*cw:(r+1)*cw], luma[(y+r)*lumaStride+x:(y+r)*lumaStride+x+cw]) {
			t.Fatalf("cropped luma row %d differs", r)
		}
	}
	for r := 0; r < ch/2; r++ {
		if !bytes.Equal(outChroma[r*cw:(r+1)*cw], chroma[(y/2+r)*chromaStride+x:(y/2+r)*chromaStride+x+cw]) {
			t.Fatalf("cropped chroma row %d differs", r)
		}
	}
	// A rectangle the runtime did not fill in means the whole surface.
	s.s.Info.CropW, s.s.Info.CropH = 0, 0
	if x, y, cw, ch = s.cropRect(); x != 0 || y != 0 || cw != 32 || ch != 16 {
		t.Errorf("cropRect without crop = %d,%d %dx%d", x, y, cw, ch)
	}
	if s.locked() {
		t.Error("a fresh surface is locked")
	}
	s.s.Data.Locked = 2
	if !s.locked() {
		t.Error("Locked != 0 is not reported")
	}
}

func nalu(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(append(out, 0, 0, 0, 1), p...)
	}
	return out
}

// TestParamSetsInspect checks the packet classification and the cache that
// re-primes the decoder at a keyframe without in-band parameter sets.
func TestParamSetsInspect(t *testing.T) {
	// Minimal H.264 NAL units: SPS id 0, PPS id 0 (both ue(v) "1" bits),
	// an IDR slice and a non-IDR slice.
	sps := []byte{0x67, 66, 0, 30, 0x80}
	pps := []byte{0x68, 0x80}
	idr := []byte{0x65, 0x88, 0x84}
	slice := []byte{0x41, 0x9a, 0x02}

	ps := newParamSets(codec.H264)
	if info := ps.inspect([]byte("not a bitstream")); info.nals != 0 {
		t.Errorf("garbage yielded %d NAL units", info.nals)
	}
	info := ps.inspect(nalu(sps, pps, idr))
	if info.nals != 3 || !info.vcl || !info.keyframe || !info.kinds[annexb.ParamSPS] || !info.kinds[annexb.ParamPPS] {
		t.Errorf("keyframe with parameter sets: %+v", info)
	}
	if !ps.covers(info.kinds) {
		t.Error("a packet with SPS and PPS should be self-contained")
	}
	info = ps.inspect(nalu(slice))
	if !info.vcl || info.keyframe {
		t.Errorf("non-IDR slice: %+v", info)
	}
	info = ps.inspect(nalu(idr))
	if !info.keyframe || ps.covers(info.kinds) {
		t.Errorf("bare IDR: %+v", info)
	}
	if got, want := ps.annexB(), nalu(sps, pps); !bytes.Equal(got, want) {
		t.Errorf("cached sets % x, want % x", got, want)
	}
	// A newer SPS with the same id replaces the old one.
	sps2 := []byte{0x67, 100, 0, 31, 0x80}
	ps.inspect(nalu(sps2))
	if got, want := ps.annexB(), nalu(sps2, pps); !bytes.Equal(got, want) {
		t.Errorf("after update % x, want % x", got, want)
	}
	if newParamSets(codec.HEVC).covers([3]bool{false, true, true}) {
		t.Error("an HEVC packet without VPS is not self-contained")
	}
}

// TestStamp checks the PTS encoding that travels through the runtime: exact
// both ways, order preserving, and never the "no time stamp" value for a
// PTS a caller would use.
func TestStamp(t *testing.T) {
	prev := uint64(0)
	for i, pts := range []int64{-90000, -1, 0, 1, 3000, 1 << 45} {
		ts := toStamp(pts)
		if ts == sys.TimestampUnknown || ts >= 1<<52 {
			t.Errorf("toStamp(%d) = %#x", pts, ts)
		}
		if i > 0 && ts <= prev {
			t.Errorf("toStamp(%d) does not preserve the order", pts)
		}
		prev = ts
		if got, ok := fromStamp(ts); !ok || got != pts {
			t.Errorf("fromStamp(toStamp(%d)) = %d, %v", pts, got, ok)
		}
		// The decoders carry time stamps as seconds in a double.
		if back := uint64(float64(ts)/90000*90000 + 0.5); back != ts {
			t.Errorf("stamp %#x of PTS %d does not survive the double round trip (%#x)", ts, pts, back)
		}
	}
	if _, ok := fromStamp(sys.TimestampUnknown); ok {
		t.Error("MFX_TIMESTAMP_UNKNOWN decoded as a PTS")
	}
}
