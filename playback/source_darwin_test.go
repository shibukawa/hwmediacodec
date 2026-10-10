//go:build darwin

package playback

import (
	"bytes"
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

func requireHardware(t *testing.T) {
	t.Helper()
	if runtime.GOARCH != "arm64" {
		t.Skip("hardware decode tests target Apple Silicon")
	}
	caps, err := hwmediacodec.Probe(context.Background())
	if err != nil || len(caps) == 0 {
		t.Skip("no hardware decoder on this machine")
	}
}

// TestSourceDeliversDisplayOrderRGBA pulls every frame of a B-frame stream
// through the decode goroutine and compares it with ffmpeg's RGBA output
// (block-averaged; see the hwmediacodec decode tests for why).
func TestSourceDeliversDisplayOrderRGBA(t *testing.T) {
	requireHardware(t)
	s := testutil.GenerateStreamBFrames(t, hwmediacodec.H264, 160, 120, 20, 2)
	want := testutil.ReferenceFrames(t, s.Path, s.Codec, hwmediacodec.RGBA, s.Width, s.Height)
	f, err := os.Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ss, err := newStreamSource(f, hwmediacodec.H264, 30)
	if err != nil {
		t.Fatal(err)
	}
	src, err := newSource(&seekableStream{ss}, 2, false, false, hwmediacodec.RGBA)
	if err != nil {
		t.Fatal(err)
	}
	defer src.close()
	var got [][]byte
	deadline := time.Now().Add(10 * time.Second)
	for len(got) < len(want) {
		it, ok := src.next()
		if !ok {
			if err := src.Err(); err != nil {
				t.Fatal(err)
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out after %d frames", len(got))
			}
			time.Sleep(time.Millisecond)
			continue
		}
		if it.end {
			t.Fatalf("stream ended after %d frames, want %d", len(got), len(want))
		}
		if want := ss.frameTime(len(got)); it.pts != want || it.loop != 0 {
			t.Fatalf("frame %d came at %v loop %d, want %v", len(got), it.pts, it.loop, want)
		}
		if it.frame.Format != hwmediacodec.RGBA || it.frame.Strides[0] != s.Width*4 {
			t.Fatalf("frame %d: format %s stride %d", len(got), it.frame.Format, it.frame.Strides[0])
		}
		got = append(got, append([]byte(nil), it.frame.Planes[0]...))
		it.release()
	}
	for i := range want {
		if psnr := testutil.BlockPSNR(got[i], want[i], s.Width, s.Height, 8); psnr < 30 {
			t.Fatalf("frame %d: block PSNR %.2f dB against ffmpeg", i, psnr)
		}
	}
	ended := false
	for time.Now().Before(deadline) && !ended {
		it, ok := src.next()
		if !ok {
			time.Sleep(time.Millisecond)
			continue
		}
		ended = it.end
	}
	if !ended {
		t.Error("source did not report the end of the stream")
	}
	if err := src.Err(); err != nil {
		t.Fatal(err)
	}
}

// tick advances the player by one 60 Hz tick and releases the frame it
// returns; it reports whether there was one.
func tick(t *testing.T, p *Player) bool {
	t.Helper()
	f, err := p.Advance(time.Second / 60)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		return false
	}
	f.Release()
	return true
}

// TestPlayerPlaysAndLoops drives a Player with ticks and checks that frames
// appear at the stream's rate, that the end is reported, and that a looping
// player restarts.
func TestPlayerPlaysAndLoops(t *testing.T) {
	requireHardware(t)
	s := testutil.GenerateStream(t, hwmediacodec.H264, 160, 120, 12)
	data := testutil.ReadFile(t, s.Path)

	t.Run("once", func(t *testing.T) {
		p, err := NewStream(bytes.NewReader(data), hwmediacodec.H264, 30)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		if tick(t, p) {
			t.Fatal("a paused player returned a frame")
		}
		p.Play()
		// At 60 ticks a second the 12 frames at 30 fps need 24 ticks plus
		// decoding latency.
		ticks, shown := 0, 0
		for !p.Ended() && ticks < 600 {
			if tick(t, p) {
				shown++
			}
			ticks++
			time.Sleep(time.Millisecond)
		}
		if !p.Ended() {
			t.Fatalf("player did not end after %d ticks (err %v)", ticks, p.Err())
		}
		if w, h := p.Size(); w != 160 || h != 120 || shown == 0 {
			t.Fatalf("size %dx%d, %d frames shown", w, h, shown)
		}
		if shown+p.Skipped() != 12 {
			t.Errorf("%d frames shown and %d skipped, want 12 in total", shown, p.Skipped())
		}
		if p.IsPlaying() {
			t.Error("an ended player reports playing")
		}
		if pos := p.Position(); pos < 11*time.Second/30 {
			t.Errorf("position %v at the end", pos)
		}
		if tick(t, p) {
			t.Error("an ended player returned a frame")
		}
		t.Logf("ended after %d ticks, %d shown, %d skipped", ticks, shown, p.Skipped())
	})

	t.Run("loop", func(t *testing.T) {
		p, err := NewStream(bytes.NewReader(data), hwmediacodec.H264, 30, WithLoop(), WithPrefetch(2))
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		p.Play()
		// Run through the stream three times.
		for i := 0; i < 24*3+30; i++ {
			tick(t, p)
			time.Sleep(time.Millisecond)
		}
		if p.Ended() {
			t.Fatal("a looping player ended")
		}
		if p.tl.loop < 2 {
			t.Fatalf("only %d loop iterations", p.tl.loop+1)
		}
		t.Logf("%d iterations, position %v, %d skipped", p.tl.loop+1, p.Position(), p.Skipped())
	})

	t.Run("nv12", func(t *testing.T) {
		p, err := NewStream(bytes.NewReader(data), hwmediacodec.H264, 30, WithOutputFormat(hwmediacodec.NV12))
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		p.Play()
		for i := 0; i < 600; i++ {
			f, err := p.Advance(time.Second / 60)
			if err != nil {
				t.Fatal(err)
			}
			if f != nil {
				if f.Format != hwmediacodec.NV12 || len(f.Planes) != 2 {
					t.Errorf("frame %s with %d planes, want NV12", f.Format, len(f.Planes))
				}
				f.Release()
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("no frame")
	})

	if _, err := NewStream(bytes.NewReader(data), hwmediacodec.H264, 0); err == nil {
		t.Error("frame rate 0 was accepted")
	}
	if _, err := NewStream(bytes.NewBuffer(data), hwmediacodec.H264, 30, WithLoop()); err == nil {
		t.Error("looping over a non-seekable reader was accepted")
	}
}
