//go:build darwin

package ebitenvideo

import (
	"bytes"
	"context"
	"errors"
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
	if !hwmediacodec.HasHardware(context.Background(), hwmediacodec.H264, hwmediacodec.Decode) {
		t.Skip("no hardware decoder on this machine")
	}
}

// TestPlayerUploadsFrames drives a Player with Update ticks, the way a
// game does: the picture appears in an image of the stream's size, the end
// is reported, and seeking brings the player back. The decoding and the
// timing are tested in package playback.
func TestPlayerUploadsFrames(t *testing.T) {
	requireHardware(t)
	s := testutil.GenerateStream(t, hwmediacodec.H264, 160, 120, 30)
	data := testutil.ReadFile(t, s.Path)
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
	run := func(ticks int, until func() bool) {
		t.Helper()
		for i := 0; i < ticks && !until(); i++ {
			if err := p.Update(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	// Default TPS is 60, so the 30 frames at 30 fps need 60 ticks plus
	// decoding latency.
	run(600, p.Ended)
	if !p.Ended() || p.IsPlaying() {
		t.Fatalf("player did not end (err %v, position %v)", p.Err(), p.Position())
	}
	if w, h := p.Size(); w != 160 || h != 120 || p.Image() == nil {
		t.Fatalf("size %dx%d image %v", w, h, p.Image())
	}
	if b := p.Image().Bounds(); b.Dx() != 160 || b.Dy() != 120 {
		t.Fatalf("image bounds %v", b)
	}
	if !p.Seekable() {
		t.Error("a bytes.Reader source is not seekable")
	}
	if err := p.Seek(500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if p.Ended() || !p.IsPlaying() {
		t.Fatal("seek did not resume an ended player")
	}
	run(600, func() bool { return p.Position() > 500*time.Millisecond })
	if pos := p.Position(); pos <= 500*time.Millisecond || pos > 700*time.Millisecond {
		t.Errorf("position %v after the seek resumed, want just above 500ms", pos)
	}
	// The first seek scanned the raw stream, so its length is known now.
	if p.Length() != time.Second {
		t.Errorf("Length %v, want 1s", p.Length())
	}

	np, err := NewPlayer(bytes.NewBuffer(data), hwmediacodec.H264, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer np.Close()
	if np.Seekable() || !errors.Is(np.Seek(time.Second), ErrNotSeekable) {
		t.Error("a non-seekable reader accepted Seek")
	}
	if _, err := NewPlayer(bytes.NewBuffer(data), hwmediacodec.H264, 30, WithLoop()); err == nil {
		t.Error("looping over a non-seekable reader was accepted")
	}
}
