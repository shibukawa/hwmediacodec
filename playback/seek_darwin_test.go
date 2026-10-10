//go:build darwin

package playback

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// pull waits for the next item of the given generation, releasing stale
// ones, and fails after a while.
func pull(t *testing.T, src *source, gen int) item {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		it, ok := src.next()
		if !ok {
			if err := src.Err(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
			continue
		}
		if it.gen != gen {
			it.doRelease()
			continue
		}
		return it
	}
	t.Fatalf("no item of generation %d arrived", gen)
	return item{}
}

// TestSourceSeek seeks a B-frame stream (keyframes every 25 frames) and
// checks that the first frame delivered after each seek is the one at the
// target, pixel for pixel against ffmpeg.
func TestSourceSeek(t *testing.T) {
	requireHardware(t)
	const fps = 30
	s := testutil.GenerateStreamBFrames(t, hwmediacodec.H264, 160, 120, 90, 2)
	want := testutil.ReferenceFrames(t, s.Path, s.Codec, hwmediacodec.RGBA, s.Width, s.Height)
	data := testutil.ReadFile(t, s.Path)
	ss, err := newStreamSource(bytes.NewReader(data), hwmediacodec.H264, fps)
	if err != nil {
		t.Fatal(err)
	}
	src, err := newSource(&seekableStream{ss}, 4, false, false, hwmediacodec.RGBA)
	if err != nil {
		t.Fatal(err)
	}
	defer src.close()
	frameTime := ss.frameTime
	check := func(it item, index int) {
		t.Helper()
		if it.pts != frameTime(index) {
			t.Fatalf("frame at %v, want %v (frame %d)", it.pts, frameTime(index), index)
		}
		if psnr := testutil.BlockPSNR(it.frame.Planes[0], want[index], s.Width, s.Height, 8); psnr < 30 {
			t.Fatalf("frame %d: block PSNR %.2f dB against ffmpeg", index, psnr)
		}
		it.release()
	}

	// Plays from the start.
	for i := 0; i < 3; i++ {
		check(pull(t, src, 0), i)
	}
	// Seek to 2 s: keyframe 50 is decoded from, frames 50..59 dropped,
	// frame 60 delivered first, then 61.
	src.requestSeek(seekRequest{target: 2 * time.Second, gen: 1})
	check(pull(t, src, 1), 60)
	check(pull(t, src, 1), 61)
	if got := src.seeker.Length(); got != 3*time.Second {
		t.Errorf("Length %v after the scan, want 3s", got)
	}
	// Between two frames: the first frame at or after the target.
	src.requestSeek(seekRequest{target: frameTime(52) + 10*time.Millisecond, gen: 2})
	check(pull(t, src, 2), 53)
	// Backwards, onto a keyframe exactly.
	src.requestSeek(seekRequest{target: frameTime(25), gen: 3})
	check(pull(t, src, 3), 25)
	// Past the end: no frame, just the end marker, and the goroutine keeps
	// waiting for the next seek.
	src.requestSeek(seekRequest{target: 10 * time.Second, gen: 4})
	if it := pull(t, src, 4); !it.end {
		t.Fatalf("seek past the end delivered a frame at %v", it.pts)
	}
	src.requestSeek(seekRequest{target: 500 * time.Millisecond, gen: 5})
	check(pull(t, src, 5), 15)
	// Everything to the end, then the end marker.
	for i := 16; i < 90; i++ {
		check(pull(t, src, 5), i)
	}
	if it := pull(t, src, 5); !it.end {
		t.Fatal("no end marker after the last frame")
	}
	if err := src.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestPlayerSeek drives a Player through seeks with 60 Hz ticks.
func TestPlayerSeek(t *testing.T) {
	requireHardware(t)
	s := testutil.GenerateStreamBFrames(t, hwmediacodec.H264, 160, 120, 90, 2)
	data := testutil.ReadFile(t, s.Path)
	p, err := NewStream(bytes.NewReader(data), hwmediacodec.H264, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !p.Seekable() {
		t.Fatal("a bytes.Reader source is not seekable")
	}
	p.Play()
	shown := 0
	run := func(ticks int, until func() bool) {
		t.Helper()
		for i := 0; i < ticks && !until(); i++ {
			f, err := p.Advance(time.Second / 60)
			if err != nil {
				t.Fatal(err)
			}
			if f != nil {
				if f.Format != hwmediacodec.RGBA || f.Width != 160 || f.Height != 120 {
					t.Fatalf("frame %s %dx%d", f.Format, f.Width, f.Height)
				}
				shown++
				f.Release()
			}
			time.Sleep(time.Millisecond)
		}
	}
	run(60, func() bool { return shown > 0 })
	if shown == 0 {
		t.Fatal("no frame shown")
	}
	if w, h := p.Size(); w != 160 || h != 120 {
		t.Errorf("Size %dx%d", w, h)
	}
	if err := p.Seek(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	if p.Position() != 2*time.Second {
		t.Fatalf("position %v right after Seek, want 2s", p.Position())
	}
	// The clock waits for the first frame after the seek, then runs on.
	run(600, func() bool { return p.Position() > 2*time.Second })
	if pos := p.Position(); pos <= 2*time.Second || pos > 2200*time.Millisecond {
		t.Fatalf("position %v after the seek resumed, want just above 2s", pos)
	}
	if p.Length() != 3*time.Second {
		t.Errorf("Length %v, want 3s", p.Length())
	}
	// Seeking past the end ends playback; seeking back resumes it.
	if err := p.Seek(time.Hour); err != nil {
		t.Fatal(err)
	}
	run(600, p.Ended)
	if !p.Ended() || p.IsPlaying() {
		t.Fatal("seeking past the end did not end playback")
	}
	if err := p.Seek(time.Second); err != nil {
		t.Fatal(err)
	}
	if p.Ended() || !p.IsPlaying() {
		t.Fatal("seek did not resume an ended player")
	}
	run(600, func() bool { return p.Position() > time.Second })
	if pos := p.Position(); pos <= time.Second || pos > 1200*time.Millisecond {
		t.Fatalf("position %v after resuming, want just above 1s", pos)
	}
	// Then it plays to the end.
	run(60*3+60, p.Ended)
	if !p.Ended() {
		t.Fatalf("player did not end; position %v", p.Position())
	}

	np, err := NewStream(bytes.NewBuffer(data), hwmediacodec.H264, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer np.Close()
	if np.Seekable() || !errors.Is(np.Seek(time.Second), ErrNotSeekable) {
		t.Error("a non-seekable reader accepted Seek")
	}
	if np.Length() != 0 {
		t.Errorf("Length %v for a non-seekable reader", np.Length())
	}
}
