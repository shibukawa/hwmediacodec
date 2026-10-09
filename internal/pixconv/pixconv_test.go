package pixconv

import (
	"bytes"
	"context"
	"errors"
	"math"
	"math/rand"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// reference converts with the BT.709 video-range equations in floating
// point: luma per pixel, chroma from the mean of each 2x2 block.
func reference(src []byte, stride, width, height int, bgr bool) (y, uv []byte) {
	const kr, kb = 0.2126, 0.0722
	kg := 1 - kr - kb
	rgb := func(px, py int) (r, g, b float64) {
		p := src[min(py, height-1)*stride+min(px, width-1)*4:]
		if bgr {
			return float64(p[2]), float64(p[1]), float64(p[0])
		}
		return float64(p[0]), float64(p[1]), float64(p[2])
	}
	cw, ch := (width+1)/2, (height+1)/2
	y = make([]byte, width*height)
	uv = make([]byte, cw*2*ch)
	for py := 0; py < height; py++ {
		for px := 0; px < width; px++ {
			r, g, b := rgb(px, py)
			y[py*width+px] = uint8(math.Round(16 + 219.0/255*(kr*r+kg*g+kb*b)))
		}
	}
	for cy := 0; cy < ch; cy++ {
		for cx := 0; cx < cw; cx++ {
			var r, g, b float64
			for i := 0; i < 4; i++ {
				pr, pg, pb := rgb(2*cx+i&1, 2*cy+i>>1)
				r, g, b = r+pr/4, g+pg/4, b+pb/4
			}
			luma := kr*r + kg*g + kb*b
			uv[(cy*cw+cx)*2] = uint8(math.Round(128 + 224.0/255*(b-luma)/(2*(1-kb))))
			uv[(cy*cw+cx)*2+1] = uint8(math.Round(128 + 224.0/255*(r-luma)/(2*(1-kr))))
		}
	}
	return y, uv
}

func randomPicture(seed int64, stride, height int) []byte {
	pix := make([]byte, stride*height)
	rand.New(rand.NewSource(seed)).Read(pix)
	return pix
}

func convert(src []byte, stride, width, height int, bgr bool) (y, uv []byte) {
	cw, ch := (width+1)/2, (height+1)/2
	y = make([]byte, width*height)
	uv = make([]byte, cw*2*ch)
	RGBToNV12(y, width, uv, cw*2, src, stride, width, height, bgr)
	return y, uv
}

func maxDiff(a, b []byte) int {
	worst := 0
	for i := range a {
		d := int(a[i]) - int(b[i])
		worst = max(worst, d, -d)
	}
	return worst
}

// TestRGBToNV12MatchesReference compares the fixed-point conversion with
// the floating-point equations, for even and odd sizes, padded rows and
// pictures tall enough to be split across goroutines.
func TestRGBToNV12MatchesReference(t *testing.T) {
	for _, size := range [][2]int{{64, 48}, {33, 17}, {1, 1}, {2, 5}, {96, 600}, {321, 513}} {
		width, height := size[0], size[1]
		stride := width*4 + 12
		src := randomPicture(int64(width*height), stride, height)
		for _, bgr := range []bool{false, true} {
			y, uv := convert(src, stride, width, height, bgr)
			wantY, wantUV := reference(src, stride, width, height, bgr)
			if d := maxDiff(y, wantY); d > 1 {
				t.Errorf("%dx%d bgr=%v: luma differs from the reference by up to %d", width, height, bgr, d)
			}
			if d := maxDiff(uv, wantUV); d > 1 {
				t.Errorf("%dx%d bgr=%v: chroma differs from the reference by up to %d", width, height, bgr, d)
			}
		}
	}
}

// TestRGBToNV12Range checks the ends of the video range: black, white and
// greys carry no chroma, the primaries reach the chroma limits.
func TestRGBToNV12Range(t *testing.T) {
	cases := []struct {
		r, g, b   byte
		y, cb, cr byte
	}{
		{0, 0, 0, 16, 128, 128},
		{255, 255, 255, 235, 128, 128},
		{128, 128, 128, 126, 128, 128},
		{255, 0, 0, 63, 102, 240},
		{0, 255, 0, 173, 42, 26},
		{0, 0, 255, 32, 240, 118},
	}
	for _, c := range cases {
		src := bytes.Repeat([]byte{c.r, c.g, c.b, 7}, 4)
		y, uv := convert(src, 8, 2, 2, false)
		if y[0] != c.y || y[3] != c.y || uv[0] != c.cb || uv[1] != c.cr {
			t.Errorf("rgb %d,%d,%d: got Y %d Cb %d Cr %d, want %d %d %d", c.r, c.g, c.b, y[0], uv[0], uv[1], c.y, c.cb, c.cr)
		}
	}
}

// TestRGBToNV12LeavesPaddingAlone converts into planes with padded rows and
// checks that only the picture bytes were written.
func TestRGBToNV12LeavesPaddingAlone(t *testing.T) {
	const width, height, yStride, uvStride = 5, 3, 9, 11
	src := randomPicture(1, width*4, height)
	y := bytes.Repeat([]byte{0xAA}, yStride*height)
	uv := bytes.Repeat([]byte{0xAA}, uvStride*2)
	RGBToNV12(y, yStride, uv, uvStride, src, width*4, width, height, false)
	wantY, wantUV := convert(src, width*4, width, height, false)
	for r := 0; r < height; r++ {
		if !bytes.Equal(y[r*yStride:r*yStride+width], wantY[r*width:(r+1)*width]) {
			t.Errorf("luma row %d differs from the tightly packed conversion", r)
		}
		if !bytes.Equal(y[r*yStride+width:(r+1)*yStride], bytes.Repeat([]byte{0xAA}, yStride-width)) {
			t.Errorf("luma row %d: padding was overwritten", r)
		}
	}
	for r := 0; r < 2; r++ {
		if !bytes.Equal(uv[r*uvStride:r*uvStride+6], wantUV[r*6:(r+1)*6]) {
			t.Errorf("chroma row %d differs from the tightly packed conversion", r)
		}
		if !bytes.Equal(uv[r*uvStride+6:(r+1)*uvStride], bytes.Repeat([]byte{0xAA}, uvStride-6)) {
			t.Errorf("chroma row %d: padding was overwritten", r)
		}
	}
}

// recorder is an NV12 encoder that keeps what it was sent.
type recorder struct {
	frames []codec.Frame
	pixels [][]byte
	sendOK error
	closed int
}

func (r *recorder) Send(_ context.Context, f *codec.Frame) error {
	if r.sendOK != nil {
		return r.sendOK
	}
	r.frames = append(r.frames, *f)
	r.pixels = append(r.pixels, append(append([]byte{}, f.Planes[0]...), f.Planes[1]...))
	return nil
}
func (r *recorder) Receive(context.Context) (codec.Packet, error) {
	return codec.Packet{PTS: 42}, nil
}
func (r *recorder) Flush(context.Context) error { return nil }
func (r *recorder) Close() error                { r.closed++; return nil }

func TestEncoderConvertsFrames(t *testing.T) {
	const width, height = 34, 18
	ctx := context.Background()
	for _, format := range []codec.PixelFormat{codec.RGBA, codec.BGRA} {
		inner := &recorder{}
		enc := WrapEncoder(inner, format, width, height)
		stride := width*4 + 8
		for i := 0; i < 2; i++ {
			src := randomPicture(int64(i), stride, height)
			f := &codec.Frame{
				Width: width, Height: height, Format: format,
				Planes: [][]byte{src}, Strides: []int{stride},
				PTS: int64(100 + i), ForceKeyframe: i == 1,
			}
			if err := enc.Send(ctx, f); err != nil {
				t.Fatalf("%s: Send: %v", format, err)
			}
			got := inner.frames[i]
			if got.Format != codec.NV12 || got.Width != width || got.Height != height || got.PTS != f.PTS || got.ForceKeyframe != f.ForceKeyframe {
				t.Errorf("%s frame %d: backend got %s %dx%d pts %d key %v", format, i, got.Format, got.Width, got.Height, got.PTS, got.ForceKeyframe)
			}
			if len(got.Planes) != 2 || got.Strides[0] != width || got.Strides[1] != width {
				t.Fatalf("%s frame %d: backend got %d planes, strides %v", format, i, len(got.Planes), got.Strides)
			}
			y, uv := convert(src, stride, width, height, format == codec.BGRA)
			if !bytes.Equal(inner.pixels[i], append(y, uv...)) {
				t.Errorf("%s frame %d: the backend did not get the converted picture", format, i)
			}
		}
		if p, err := enc.Receive(ctx); err != nil || p.PTS != 42 {
			t.Errorf("Receive = %+v, %v", p, err)
		}
		if enc.Unwrap() != codec.Encoder(inner) {
			t.Error("Unwrap does not return the backend encoder")
		}
		if err := enc.Close(); err != nil || inner.closed != 1 {
			t.Errorf("Close = %v, backend closed %d times", err, inner.closed)
		}
		if err := enc.Send(ctx, &codec.Frame{}); !errors.Is(err, codec.ErrClosed) {
			t.Errorf("Send after Close = %v, want ErrClosed", err)
		}
	}
}

func TestEncoderRejectsBadFrames(t *testing.T) {
	const width, height = 16, 8
	ctx := context.Background()
	inner := &recorder{}
	enc := WrapEncoder(inner, codec.RGBA, width, height)
	good := func() *codec.Frame {
		return &codec.Frame{
			Width: width, Height: height, Format: codec.RGBA,
			Planes: [][]byte{make([]byte, width*4*height)}, Strides: []int{width * 4},
		}
	}
	cases := map[string]func(*codec.Frame) *codec.Frame{
		"nil frame":    func(*codec.Frame) *codec.Frame { return nil },
		"wrong format": func(f *codec.Frame) *codec.Frame { f.Format = codec.BGRA; return f },
		"wrong size":   func(f *codec.Frame) *codec.Frame { f.Width = width + 2; return f },
		"no plane":     func(f *codec.Frame) *codec.Frame { f.Planes = nil; return f },
		"small stride": func(f *codec.Frame) *codec.Frame { f.Strides[0] = width*4 - 1; return f },
		"short plane":  func(f *codec.Frame) *codec.Frame { f.Planes[0] = f.Planes[0][:width*4*height-1]; return f },
	}
	for name, mutate := range cases {
		if err := enc.Send(ctx, mutate(good())); !errors.Is(err, codec.ErrInvalidData) {
			t.Errorf("%s: Send = %v, want ErrInvalidData", name, err)
		}
	}
	if len(inner.frames) != 0 {
		t.Errorf("%d bad frames reached the backend", len(inner.frames))
	}
	inner.sendOK = codec.ErrAgain
	if err := enc.Send(ctx, good()); !errors.Is(err, codec.ErrAgain) {
		t.Errorf("Send = %v, want the backend's ErrAgain", err)
	}
}

func BenchmarkRGBToNV12_1080p(b *testing.B) {
	const width, height = 1920, 1080
	src := randomPicture(1, width*4, height)
	y := make([]byte, width*height)
	uv := make([]byte, width*height/2)
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		RGBToNV12(y, width, uv, width, src, width*4, width, height, false)
	}
}
