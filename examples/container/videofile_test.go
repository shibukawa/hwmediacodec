package container_test

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/capture"
	"github.com/shibukawa/hwmediacodec/examples/container"
	"github.com/shibukawa/hwmediacodec/examples/internal/testutil"
)

// fakeScreen stands in for *ebiten.Image: a gradient that slides every
// frame.
type fakeScreen struct {
	img *image.RGBA
	n   int
}

func newFakeScreen(w, h int) *fakeScreen {
	return &fakeScreen{img: image.NewRGBA(image.Rect(0, 0, w, h))}
}

func (f *fakeScreen) advance() {
	f.n++
	w, h := f.img.Rect.Dx(), f.img.Rect.Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			f.img.SetRGBA(x, y, color.RGBA{R: uint8((x + 2*f.n) % 256), G: uint8(y % 256), B: uint8((x/2 + y/2) % 256), A: 255})
		}
	}
	// A moving block so that every frame differs clearly.
	bx := (f.n * 7) % (w - 32)
	for y := 20; y < 52 && y < h; y++ {
		for x := bx; x < bx+32; x++ {
			f.img.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
}

func (f *fakeScreen) Bounds() image.Rectangle { return f.img.Rect }
func (f *fakeScreen) ReadPixels(p []byte)     { copy(p, f.img.Pix) }

func rgbPSNR(a *image.RGBA, b image.Image) float64 {
	var se, n float64
	for y := 0; y < a.Rect.Dy(); y++ {
		for x := 0; x < a.Rect.Dx(); x++ {
			r1, g1, b1, _ := a.At(x, y).RGBA()
			r2, g2, b2, _ := b.At(x, y).RGBA()
			for _, d := range []float64{float64(r1>>8) - float64(r2>>8), float64(g1>>8) - float64(g2>>8), float64(b1>>8) - float64(b2>>8)} {
				se += d * d
				n++
			}
		}
	}
	if se == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(se/n))
}

// TestVideoFileRecordsCapture runs a capture.Recorder into a
// VideoFile and checks the MP4 with ffprobe: this one needs a hardware
// encoder.
func TestVideoFileRecordsCapture(t *testing.T) {
	testutil.RequireFFmpeg(t)
	testutil.RequireHardware(t, hwmediacodec.H264, hwmediacodec.Encode)
	dir := t.TempDir()
	out := filepath.Join(dir, "rec.mp4")
	sink, err := container.CreateVideoFile(out, hwmediacodec.H264, capture.TimeScale)
	if err != nil {
		t.Fatal(err)
	}
	const w, h, fps, frames = 320, 240, 30, 60
	rec, err := capture.New(w, h, sink, capture.Options{FPS: fps, Bitrate: 2_000_000, Queue: 64})
	if err != nil {
		t.Fatal(err)
	}
	screen := newFakeScreen(w, h)
	start := time.Now()
	var first *image.RGBA
	for i := 0; i < frames; i++ {
		screen.advance()
		if i == 0 {
			first = image.NewRGBA(screen.img.Rect)
			copy(first.Pix, screen.img.Pix)
		}
		// Two captures per frame slot: the second must be ignored.
		at := start.Add(time.Duration(i) * time.Second / fps)
		rec.CaptureAt(screen, at)
		rec.CaptureAt(screen, at.Add(time.Millisecond))
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	if rec.Captured() != frames || rec.Dropped() != 0 {
		t.Errorf("captured %d dropped %d, want %d and 0", rec.Captured(), rec.Dropped(), frames)
	}
	if d := rec.Duration(); d != 2*time.Second {
		t.Errorf("duration %v, want 2 s", d)
	}

	testutil.CheckDecodes(t, out)
	vs := testutil.VideoStream(t, out)
	if vs.CodecName != "h264" || vs.Width != w || vs.Height != h || vs.FrameCount() != frames {
		t.Errorf("stream %s %dx%d %d frames", vs.CodecName, vs.Width, vs.Height, vs.FrameCount())
	}
	if d := vs.Seconds(); d < 1.99 || d > 2.01 {
		t.Errorf("duration %.3f s, want 2", d)
	}
	fr := testutil.Frames(t, out)
	for i := 1; i < len(fr); i++ {
		if fr[i].PTS-fr[i-1].PTS != 3000 { // 90000 / 30
			t.Errorf("frame %d pts step %d", i, fr[i].PTS-fr[i-1].PTS)
		}
	}
	// The first decoded frame is the first captured image (RGBA order and
	// colour conversion right).
	f, err := os.Open(testutil.ExtractFrame(t, out, 0, dir))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ref, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if p := rgbPSNR(first, ref); p < 30 {
		t.Errorf("first frame PSNR %.1f dB, want at least 30", p)
	}
}
