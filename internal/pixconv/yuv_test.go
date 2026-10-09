package pixconv

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
)

var allColors = []Color{
	{BT601, false}, {BT601, true}, {BT709, false}, {BT709, true}, {BT2020, false}, {BT2020, true},
}

// referenceRGB converts NV12 with the YCbCr equations in floating point,
// every chroma sample colouring its 2x2 block.
func referenceRGB(y []byte, yStride int, uv []byte, uvStride, width, height int, c Color, bgr bool) []byte {
	k := map[Matrix][2]float64{BT601: {0.299, 0.114}, BT709: {0.2126, 0.0722}, BT2020: {0.2627, 0.0593}}[c.Matrix]
	kr, kb := k[0], k[1]
	kg := 1 - kr - kb
	clamp := func(v float64) byte { return byte(math.Round(min(max(v, 0), 255))) }
	out := make([]byte, width*height*4)
	for py := 0; py < height; py++ {
		for px := 0; px < width; px++ {
			l := float64(y[py*yStride+px])
			cb := float64(uv[(py/2)*uvStride+px/2*2]) - 128
			cr := float64(uv[(py/2)*uvStride+px/2*2+1]) - 128
			if c.FullRange {
				cb, cr = cb/255, cr/255
				l /= 255
			} else {
				cb, cr = cb/224, cr/224
				l = (l - 16) / 219
			}
			r := l + 2*(1-kr)*cr
			b := l + 2*(1-kb)*cb
			g := (l - kr*r - kb*b) / kg
			p := out[(py*width+px)*4:]
			p[0], p[1], p[2], p[3] = clamp(255*r), clamp(255*g), clamp(255*b), 255
			if bgr {
				p[0], p[2] = p[2], p[0]
			}
		}
	}
	return out
}

func toRGB(y []byte, yStride int, uv []byte, uvStride, width, height int, c Color, bgr bool) []byte {
	out := make([]byte, width*height*4)
	NV12ToRGB(out, width*4, y, yStride, uv, uvStride, width, height, c, bgr)
	return out
}

// TestNV12ToRGBMatchesReference compares the table conversion with the
// floating-point equations for every matrix and range, even and odd sizes,
// padded rows and pictures tall enough to be split across goroutines.
func TestNV12ToRGBMatchesReference(t *testing.T) {
	for _, size := range [][2]int{{64, 48}, {33, 17}, {1, 1}, {2, 5}, {96, 600}, {321, 513}} {
		width, height := size[0], size[1]
		yStride, uvStride := width+5, (width+1)/2*2+6
		y := randomPicture(int64(width), yStride, height)
		uv := randomPicture(int64(height), uvStride, (height+1)/2)
		for _, c := range allColors {
			for _, bgr := range []bool{false, true} {
				got := toRGB(y, yStride, uv, uvStride, width, height, c, bgr)
				want := referenceRGB(y, yStride, uv, uvStride, width, height, c, bgr)
				if d := maxDiff(got, want); d > 1 {
					t.Errorf("%dx%d %+v bgr=%v: differs from the reference by up to %d", width, height, c, bgr, d)
				}
			}
		}
	}
}

// TestNV12ToRGBRange checks the ends of both ranges and the BT.709 and
// BT.601 primaries.
func TestNV12ToRGBRange(t *testing.T) {
	cases := []struct {
		c         Color
		y, cb, cr byte
		r, g, b   byte
	}{
		{Color{BT709, false}, 16, 128, 128, 0, 0, 0},
		{Color{BT709, false}, 235, 128, 128, 255, 255, 255},
		{Color{BT709, false}, 0, 128, 128, 0, 0, 0},         // below black
		{Color{BT709, false}, 255, 128, 128, 255, 255, 255}, // above white
		{Color{BT709, false}, 63, 102, 240, 255, 0, 0},
		{Color{BT709, false}, 173, 42, 26, 0, 255, 0},
		{Color{BT709, false}, 32, 240, 118, 0, 0, 255},
		{Color{BT601, false}, 81, 90, 240, 255, 0, 0},
		{Color{BT601, false}, 145, 54, 34, 0, 255, 0},
		{Color{BT601, false}, 41, 240, 110, 0, 0, 255},
		{Color{BT709, true}, 0, 128, 128, 0, 0, 0},
		{Color{BT709, true}, 255, 128, 128, 255, 255, 255},
		{Color{BT601, true}, 76, 85, 255, 254, 0, 0},
	}
	for _, c := range cases {
		got := toRGB([]byte{c.y}, 1, []byte{c.cb, c.cr}, 2, 1, 1, c.c, false)
		for i, want := range []byte{c.r, c.g, c.b, 255} {
			if d := int(got[i]) - int(want); d < -1 || d > 1 {
				t.Errorf("%+v Y %d Cb %d Cr %d: got %v, want %v", c.c, c.y, c.cb, c.cr, got, []byte{c.r, c.g, c.b, 255})
				break
			}
		}
	}
}

// TestRoundTrip converts flat colours to NV12 and back with BT.709: the
// two directions have to agree with each other.
func TestRoundTrip(t *testing.T) {
	const width, height = 16, 8
	for _, rgb := range [][3]byte{{0, 0, 0}, {255, 255, 255}, {200, 30, 90}, {12, 240, 100}, {90, 90, 250}, {128, 128, 128}} {
		src := bytes.Repeat([]byte{rgb[0], rgb[1], rgb[2], 255}, width*height)
		for _, bgr := range []bool{false, true} {
			y, uv := convert(src, width*4, width, height, bgr)
			got := toRGB(y, width, uv, width, width, height, Color{BT709, false}, bgr)
			if d := maxDiff(got, src); d > 2 {
				t.Errorf("rgb %v bgr=%v: round trip is off by up to %d", rgb, bgr, d)
			}
		}
	}
}

func TestNV12ToRGBLeavesPaddingAlone(t *testing.T) {
	const width, height, stride = 5, 3, 27
	y := randomPicture(1, width, height)
	uv := randomPicture(2, 6, 2)
	dst := bytes.Repeat([]byte{0xAA}, stride*height)
	NV12ToRGB(dst, stride, y, width, uv, 6, width, height, Color{}, false)
	want := toRGB(y, width, uv, 6, width, height, Color{}, false)
	for r := 0; r < height; r++ {
		if !bytes.Equal(dst[r*stride:r*stride+width*4], want[r*width*4:(r+1)*width*4]) {
			t.Errorf("row %d differs from the tightly packed conversion", r)
		}
		if !bytes.Equal(dst[r*stride+width*4:(r+1)*stride], bytes.Repeat([]byte{0xAA}, stride-width*4)) {
			t.Errorf("row %d: padding was overwritten", r)
		}
	}
}

func TestColorFor(t *testing.T) {
	cases := []struct {
		matrix        uint8
		full          bool
		width, height int
		want          Color
	}{
		{1, false, 320, 240, Color{BT709, false}},
		{5, false, 1920, 1080, Color{BT601, false}},
		{6, true, 1920, 1080, Color{BT601, true}},
		{9, false, 3840, 2160, Color{BT2020, false}},
		// No matrix named: the picture size decides, as on VideoToolbox.
		{2, false, 704, 576, Color{BT601, false}},
		{2, false, 706, 480, Color{BT709, false}},
		{2, false, 320, 578, Color{BT709, false}},
		{2, true, 1280, 720, Color{BT709, true}},
		{0, false, 640, 480, Color{BT601, false}}, // GBR is not expected in 4:2:0
		{200, false, 1280, 720, Color{BT709, false}},
	}
	for _, c := range cases {
		if got := ColorFor(c.matrix, c.full, c.width, c.height); got != c.want {
			t.Errorf("ColorFor(%d, %v, %d, %d) = %+v, want %+v", c.matrix, c.full, c.width, c.height, got, c.want)
		}
	}
}

// source is an NV12 decoder that returns one grey-ish frame per packet and
// counts the frames given back.
type source struct {
	width, height int
	pending       []int64
	released      int
	flushed       bool
	closed        int
}

func (s *source) Send(_ context.Context, p codec.Packet) error {
	s.pending = append(s.pending, p.PTS)
	return nil
}

func (s *source) Receive(context.Context) (*codec.Frame, error) {
	if len(s.pending) == 0 {
		if s.flushed {
			return nil, io.EOF
		}
		return nil, codec.ErrAgain
	}
	pts := s.pending[0]
	s.pending = s.pending[1:]
	// Padded rows of zeroes, as backends that hand out their own
	// surfaces have.
	yStride, uvStride := s.width+3, (s.width+1)/2*2+2
	cRows, cBytes := codec.NV12.PlaneLayout(1, s.width, s.height)
	luma := make([]byte, yStride*s.height)
	chroma := make([]byte, uvStride*cRows)
	for r := 0; r < s.height; r++ {
		copy(luma[r*yStride:], bytes.Repeat([]byte{81}, s.width))
	}
	for r := 0; r < cRows; r++ {
		copy(chroma[r*uvStride:], bytes.Repeat([]byte{90, 240}, cBytes/2))
	}
	f := &codec.Frame{
		Width: s.width, Height: s.height, Format: codec.NV12, PTS: pts,
		Planes: [][]byte{luma, chroma}, Strides: []int{yStride, uvStride},
	}
	codec.SetFrameOrder(f, codec.Order{POC: int32(pts)})
	codec.SetRelease(f, func() { s.released++ })
	return f, nil
}

func (s *source) Flush(context.Context) error { s.flushed = true; return nil }
func (s *source) Close() error                { s.closed++; return nil }

func h264SPS(vui h264.VUI) []byte {
	return append([]byte{0, 0, 0, 1}, h264.WriteSPS(&h264.SPS{
		ProfileIDC: 100, LevelIDC: 31, ChromaFormatIDC: 1, BitDepthLuma: 8, BitDepthChroma: 8,
		Log2MaxFrameNum: 8, Log2MaxPicOrderCntLsb: 8, MaxNumRefFrames: 1,
		PicWidthInMbs: 20, PicHeightInMapUnits: 15, FrameMbsOnly: true, Direct8x8Inference: true,
		VUIPresent: true, VUI: vui,
	})...)
}

func hevcSPS(vui hevc.VUI) []byte {
	prev := hevc.ShortTermRPS{NumNegativePics: 1}
	prev.DeltaPocS0[0] = -1
	prev.UsedByCurrPicS0[0] = true
	return append([]byte{0, 0, 0, 1}, hevc.WriteSPS(&hevc.SPS{
		TemporalIDNesting: true,
		PTL: hevc.ProfileTierLevel{ProfileIDC: hevc.ProfileMain, CompatibilityFlags: 3 << 29,
			ProgressiveSource: true, NonPackedConstraint: true, FrameOnlyConstraint: true, LevelIDC: 93},
		ChromaFormatIDC: 1, Width: 320, Height: 240, BitDepthLuma: 8, BitDepthChroma: 8, Log2MaxPOCLsb: 12,
		MaxDecPicBufferingMinus1: []uint32{1}, MaxNumReorderPics: []uint32{0}, MaxLatencyIncreasePlus1: []uint32{0},
		Log2DiffMaxMinLumaCodingBlockSize: 3, Log2DiffMaxMinLumaTransformBlockSize: 3,
		ShortTermRPS: []hevc.ShortTermRPS{prev}, VUIPresent: true, VUI: vui,
	})...)
}

// av1SequenceHeader returns a sequence header OBU for a 320x240 8-bit
// stream: the one SVT-AV1 writes, up to color_description_present_flag,
// followed by the given end (the flag, the three colour codes when it is
// set, color_range and the rest of the header).
func av1SequenceHeader(end ...byte) []byte {
	payload := append([]byte{0x02, 0x00, 0x00, 0x05, 0x21, 0xe7, 0xfd, 0xe2, 0x57, 0xc8}, end...)
	return av1.AppendOBU(nil, av1.OBUSequenceHeader, payload)
}

// TestDecoderFollowsStreamColor sends parameter sets with different colour
// descriptions and checks which conversion the frames get. The source frame
// is pure red in BT.601 video range.
func TestDecoderFollowsStreamColor(t *testing.T) {
	ctx := context.Background()
	slice := []byte{0, 0, 1, 0x65, 0x88} // stands in for the picture
	red := func(c Color) []byte { return toRGB([]byte{81}, 1, []byte{90, 240}, 2, 1, 1, c, false) }
	type step struct {
		name string
		sps  []byte
		want Color
	}
	for _, tc := range []struct {
		codec         codec.Codec
		width, height int
		steps         []step
	}{
		{codec.H264, 320, 240, []step{
			{"no parameter set yet", nil, Color{BT601, false}},
			{"no video signal", h264SPS(h264.VUI{}), Color{BT601, false}},
			{"bt709", h264SPS(h264.VUI{VideoSignalTypePresent: true, VideoFormat: 5, ColourDescriptionPresent: true, ColourPrimaries: 1, TransferCharacteristics: 1, MatrixCoefficients: 1}), Color{BT709, false}},
			{"same parameter set again", nil, Color{BT709, false}},
			{"full range without a matrix", h264SPS(h264.VUI{VideoSignalTypePresent: true, VideoFormat: 5, VideoFullRange: true}), Color{BT601, true}},
			{"bt2020", h264SPS(h264.VUI{VideoSignalTypePresent: true, VideoFormat: 5, ColourDescriptionPresent: true, ColourPrimaries: 9, TransferCharacteristics: 14, MatrixCoefficients: 9}), Color{BT2020, false}},
			{"broken parameter set", []byte{0, 0, 0, 1, 0x67, 0xff}, Color{BT2020, false}},
		}},
		{codec.H264, 1280, 720, []step{
			{"no video signal, high definition", h264SPS(h264.VUI{}), Color{BT709, false}},
			{"smpte170m", h264SPS(h264.VUI{VideoSignalTypePresent: true, VideoFormat: 5, ColourDescriptionPresent: true, ColourPrimaries: 6, TransferCharacteristics: 6, MatrixCoefficients: 6}), Color{BT601, false}},
		}},
		{codec.HEVC, 320, 240, []step{
			{"no video signal", hevcSPS(hevc.VUI{}), Color{BT601, false}},
			{"bt709 full range", hevcSPS(hevc.VUI{VideoSignalTypePresent: true, VideoFormat: 5, VideoFullRange: true, ColourDescriptionPresent: true, ColourPrimaries: 1, TransferCharacteristics: 1, MatrixCoeffs: 1}), Color{BT709, true}},
		}},
		{codec.AV1, 320, 240, []step{
			{"no colour description", av1SequenceHeader(0x02), Color{BT601, false}},
			{"bt709", av1SequenceHeader(0x80, 0x80, 0x80, 0x82), Color{BT709, false}},
			{"bt709 full range", av1SequenceHeader(0x80, 0x80, 0x80, 0xc2), Color{BT709, true}},
			{"bt2020", av1SequenceHeader(0x84, 0x84, 0x84, 0x82), Color{BT2020, false}},
			{"broken sequence header", []byte{0x0a, 0x01, 0xff}, Color{BT2020, false}},
		}},
		{codec.AV1, 1280, 720, []step{
			{"no colour description, high definition", av1SequenceHeader(0x02), Color{BT709, false}},
			{"smpte170m", av1SequenceHeader(0x83, 0x03, 0x03, 0x02), Color{BT601, false}},
		}},
		{codec.Codec(99), 1280, 720, []step{
			{"other codec", h264SPS(h264.VUI{VideoSignalTypePresent: true, ColourDescriptionPresent: true, MatrixCoefficients: 6}), Color{BT709, false}},
		}},
	} {
		src := &source{width: tc.width, height: tc.height}
		dec := WrapDecoder(src, tc.codec, codec.RGBA)
		for i, s := range tc.steps {
			data := append([]byte{}, s.sps...)
			if tc.codec != codec.AV1 {
				data = append(data, slice...)
			}
			if err := dec.Send(ctx, codec.Packet{Data: data, PTS: int64(i)}); err != nil {
				t.Fatal(err)
			}
			f, err := dec.Receive(ctx)
			if err != nil {
				t.Fatalf("%s %s: Receive: %v", tc.codec, s.name, err)
			}
			if got, want := f.Planes[0][:4], red(s.want); !bytes.Equal(got, want) {
				t.Errorf("%s %dx%d, %s: pixel %v, want %v (%+v)", tc.codec, tc.width, tc.height, s.name, got, want, s.want)
			}
			f.Release()
		}
	}
}

func TestDecoderConvertsFrames(t *testing.T) {
	const width, height = 34, 17
	ctx := context.Background()
	for _, format := range []codec.PixelFormat{codec.RGBA, codec.BGRA} {
		src := &source{width: width, height: height}
		dec := WrapDecoder(src, codec.H264, format)
		if dec.Unwrap() != codec.Decoder(src) || dec.OutputsDisplayOrder() {
			t.Error("Unwrap or OutputsDisplayOrder does not describe the decoder underneath")
		}
		if _, err := dec.Receive(ctx); !errors.Is(err, codec.ErrAgain) {
			t.Errorf("Receive on an empty decoder = %v, want ErrAgain", err)
		}
		var frames []*codec.Frame
		for i := 0; i < 3; i++ {
			if err := dec.Send(ctx, codec.Packet{PTS: int64(10 + i)}); err != nil {
				t.Fatal(err)
			}
			f, err := dec.Receive(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if f.Format != format || f.Width != width || f.Height != height || f.PTS != int64(10+i) || codec.FrameOrder(f).POC != int32(10+i) {
				t.Errorf("%s frame %d: %s %dx%d pts %d order %+v", format, i, f.Format, f.Width, f.Height, f.PTS, codec.FrameOrder(f))
			}
			if len(f.Planes) != 1 || len(f.Planes[0]) != width*height*4 || f.Strides[0] != width*4 {
				t.Fatalf("%s frame %d: %d planes, %d bytes, strides %v", format, i, len(f.Planes), len(f.Planes[0]), f.Strides)
			}
			// Pure red in BT.601 video range, whatever the padding holds.
			want := toRGB([]byte{81}, 1, []byte{90, 240}, 2, 1, 1, Color{BT601, false}, format == codec.BGRA)
			if !bytes.Equal(f.Planes[0], bytes.Repeat(want, width*height)) {
				t.Errorf("%s frame %d is not uniformly %v: starts %v", format, i, want, f.Planes[0][:8])
			}
			frames = append(frames, f)
		}
		if src.released != 3 {
			t.Errorf("%d backend frames released, want 3", src.released)
		}
		// Frames are independent of each other and outlive the decoder.
		if &frames[0].Planes[0][0] == &frames[1].Planes[0][0] {
			t.Error("two live frames share their pixels")
		}
		if err := dec.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := dec.Receive(ctx); err != io.EOF {
			t.Errorf("Receive after Flush = %v, want io.EOF", err)
		}
		if err := dec.Close(); err != nil || src.closed != 1 {
			t.Errorf("Close = %v, decoder underneath closed %d times", err, src.closed)
		}
		if !bytes.Equal(frames[2].Planes[0], frames[0].Planes[0]) {
			t.Error("a frame changed when the decoder was closed")
		}
		for _, f := range frames {
			f.Release()
			f.Release()
			if f.Planes != nil {
				t.Error("Release left the planes in place")
			}
		}
		if _, err := dec.Receive(ctx); !errors.Is(err, codec.ErrClosed) {
			t.Errorf("Receive after Close = %v, want ErrClosed", err)
		}
		if err := dec.Send(ctx, codec.Packet{}); !errors.Is(err, codec.ErrClosed) {
			t.Errorf("Send after Close = %v, want ErrClosed", err)
		}
	}
}

func BenchmarkNV12ToRGB_1080p(b *testing.B) {
	const width, height = 1920, 1080
	y := randomPicture(1, width, height)
	uv := randomPicture(2, width, height/2)
	dst := make([]byte, width*height*4)
	b.SetBytes(int64(len(dst)))
	for i := 0; i < b.N; i++ {
		NV12ToRGB(dst, width*4, y, width, uv, width, width, height, Color{BT709, false}, false)
	}
}
