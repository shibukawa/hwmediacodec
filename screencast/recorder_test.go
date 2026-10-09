package screencast_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/screencast"
)

// requireHardwareEncode skips the test unless Probe reports a hardware
// H.264 encoder on this machine.
func requireHardwareEncode(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" && runtime.GOARCH != "arm64" {
		t.Skip("hardware tests target Apple Silicon (Intel Macs are out of scope)")
	}
	caps, err := hwmediacodec.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	for _, cap := range caps {
		if cap.Codec == hwmediacodec.H264 && cap.Direction == hwmediacodec.Encode && cap.Hardware {
			return
		}
	}
	t.Skipf("no hardware H.264 encoder on this machine (%s/%s)", runtime.GOOS, runtime.GOARCH)
}

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

func TestRecorderEncodes(t *testing.T) {
	requireHardwareEncode(t)
	const w, h, fps, frames = 320, 240, 30, 60
	var stream bytes.Buffer
	var pts []int64
	var keys []bool
	closed := false
	sink := screencast.Funcs{
		Write: func(p hwmediacodec.Packet) error {
			stream.Write(p.Data)
			pts = append(pts, p.PTS)
			keys = append(keys, p.Keyframe)
			return nil
		},
		Done: func() error { closed = true; return nil },
	}
	rec, err := screencast.New(w, h, sink, screencast.Options{FPS: fps, Bitrate: 2_000_000, Queue: 64})
	if err != nil {
		t.Fatal(err)
	}
	screen := newFakeScreen(w, h)
	start := time.Now()
	var first []byte
	for i := 0; i < frames; i++ {
		screen.advance()
		if i == 0 {
			first = slices.Clone(screen.img.Pix)
		}
		// Two captures per frame slot: the second must be ignored.
		at := start.Add(time.Duration(i) * time.Second / fps)
		rec.CaptureAt(screen, at)
		rec.CaptureAt(screen, at.Add(time.Millisecond))
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Error("Close did not close the sink")
	}
	if rec.Captured() != frames || rec.Dropped() != 0 || rec.Encoded() != frames {
		t.Errorf("captured %d dropped %d encoded %d, want %d, 0 and %d", rec.Captured(), rec.Dropped(), rec.Encoded(), frames, frames)
	}
	if d := rec.Duration(); d != 2*time.Second {
		t.Errorf("duration %v, want 2 s", d)
	}
	if len(pts) != frames {
		t.Fatalf("sink received %d packets, want %d", len(pts), frames)
	}
	if !keys[0] {
		t.Error("the first packet is not a keyframe")
	}
	for i := 1; i < len(pts); i++ {
		if pts[i]-pts[i-1] != screencast.TimeScale/fps {
			t.Errorf("packet %d pts step %d, want %d", i, pts[i]-pts[i-1], screencast.TimeScale/fps)
		}
	}

	// ffmpeg decodes the stream to the same number of pictures and its
	// first one is the first captured image (RGBA order and colour
	// conversion right).
	path := filepath.Join(t.TempDir(), "rec.h264")
	if err := os.WriteFile(path, stream.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	got := testutil.ReferenceFrames(t, path, hwmediacodec.H264, hwmediacodec.RGBA, w, h)
	if len(got) != frames {
		t.Fatalf("ffmpeg decoded %d frames, want %d", len(got), frames)
	}
	if p := testutil.BlockPSNR(first, got[0], w, h, 8); p < 30 {
		t.Errorf("first frame 8x8-block PSNR %.1f dB, want at least 30", p)
	}
}

func TestRecorderRequestKeyframe(t *testing.T) {
	requireHardwareEncode(t)
	var keys []int
	n := 0
	sink := screencast.Funcs{Write: func(p hwmediacodec.Packet) error {
		if p.Keyframe {
			keys = append(keys, n)
		}
		n++
		return nil
	}}
	rec, err := screencast.New(320, 240, sink, screencast.Options{FPS: 30, KeyframeInterval: 300, Queue: 64})
	if err != nil {
		t.Fatal(err)
	}
	screen := newFakeScreen(320, 240)
	start := time.Now()
	for i := 0; i < 30; i++ {
		screen.advance()
		if i == 10 || i == 20 {
			rec.RequestKeyframe()
		}
		rec.CaptureAt(screen, start.Add(time.Duration(i)*time.Second/30))
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	if want := []int{0, 10, 20}; !slices.Equal(keys, want) {
		t.Errorf("keyframes at %v, want %v", keys, want)
	}
}

func TestRecorderDropsWhenBehind(t *testing.T) {
	requireHardwareEncode(t)
	var packets int
	sink := screencast.Funcs{Write: func(hwmediacodec.Packet) error { packets++; return nil }}
	rec, err := screencast.New(320, 240, sink, screencast.Options{FPS: 60, Queue: 2})
	if err != nil {
		t.Fatal(err)
	}
	screen := newFakeScreen(320, 240)
	start := time.Now()
	for i := 0; i < 200; i++ {
		screen.advance()
		rec.CaptureAt(screen, start.Add(time.Duration(i)*time.Second/60))
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	if rec.Dropped() == 0 {
		t.Log("the encoder kept up with 200 bursts; nothing dropped")
	}
	// Every capture is either encoded or counted as dropped. VideoToolbox
	// may itself skip frames under such a burst, so packets <= captured.
	if rec.Captured()+rec.Dropped() != 200 || packets == 0 || int64(packets) > rec.Captured() {
		t.Errorf("captured %d dropped %d packets %d", rec.Captured(), rec.Dropped(), packets)
	}
}
