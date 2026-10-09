//go:build darwin

package ebitenvideo

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
	src, err := newSource(f, hwmediacodec.H264, 2, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer src.close()
	var got [][]byte
	deadline := time.Now().Add(10 * time.Second)
	for len(got) < len(want) {
		it, ok := src.next()
		if !ok {
			if src.ended() {
				t.Fatalf("stream ended after %d frames, want %d (err %v)", len(got), len(want), src.Err())
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out after %d frames", len(got))
			}
			time.Sleep(time.Millisecond)
			continue
		}
		if it.index != len(got) || it.loop != 0 {
			t.Fatalf("frame %d came as index %d loop %d", len(got), it.index, it.loop)
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
	for {
		if _, ok := src.next(); !ok {
			break
		}
	}
	if !src.ended() {
		t.Error("source did not report the end of the stream")
	}
	if err := src.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestPlayerPlaysAndLoops drives a Player with ticks and checks that frames
// appear at the stream's rate, that the end is reported, and that a looping
// player restarts.
func TestPlayerPlaysAndLoops(t *testing.T) {
	requireHardware(t)
	s := testutil.GenerateStream(t, hwmediacodec.H264, 160, 120, 12)
	data := testutil.ReadFile(t, s.Path)

	t.Run("once", func(t *testing.T) {
		p, err := NewPlayer(bytes.NewReader(data), hwmediacodec.H264, 30)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		if err := p.Update(); err != nil {
			t.Fatal(err)
		}
		if p.Image() != nil {
			t.Fatal("a paused player showed a frame")
		}
		p.Play()
		// Default TPS is 60, so the 12 frames at 30 fps need 24 ticks plus
		// decoding latency.
		ticks := 0
		for !p.Ended() && ticks < 600 {
			if err := p.Update(); err != nil {
				t.Fatal(err)
			}
			ticks++
			time.Sleep(time.Millisecond)
		}
		if !p.Ended() {
			t.Fatalf("player did not end after %d ticks (err %v)", ticks, p.Err())
		}
		if w, h := p.Size(); w != 160 || h != 120 || p.Image() == nil {
			t.Fatalf("size %dx%d image %v", w, h, p.Image())
		}
		if b := p.Image().Bounds(); b.Dx() != 160 || b.Dy() != 120 {
			t.Fatalf("image bounds %v", b)
		}
		if p.IsPlaying() {
			t.Error("an ended player reports playing")
		}
		if pos := p.Position(); pos < 11*time.Second/30 {
			t.Errorf("position %v at the end", pos)
		}
		t.Logf("ended after %d ticks, %d skipped", ticks, p.Skipped())
	})

	t.Run("loop", func(t *testing.T) {
		p, err := NewPlayer(bytes.NewReader(data), hwmediacodec.H264, 30, WithLoop(), WithPrefetch(2))
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		p.Play()
		// Run through the stream three times.
		for i := 0; i < 24*3+30; i++ {
			if err := p.Update(); err != nil {
				t.Fatal(err)
			}
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

	if _, err := NewPlayer(bytes.NewReader(data), hwmediacodec.H264, 0); err == nil {
		t.Error("frame rate 0 was accepted")
	}
	if _, err := NewPlayer(bytes.NewBuffer(data), hwmediacodec.H264, 30, WithLoop()); err == nil {
		t.Error("looping over a non-seekable reader was accepted")
	}
}
