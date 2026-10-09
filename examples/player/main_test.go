package main

import (
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/ebitenvideo"
	"github.com/shibukawa/hwmediacodec/examples/internal/testutil"
)

// TestPlaysBundledClip opens the embedded waterfall clip the way main does
// and drives the player through a seek with Update ticks.
func TestPlaysBundledClip(t *testing.T) {
	testutil.RequireHardware(t, hwmediacodec.HEVC, hwmediacodec.Decode)
	p, closer, name, w, h, err := open("", "h264", 30, []ebitenvideo.Option{ebitenvideo.WithLoop()})
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	defer p.Close()
	if name != "waterfall-720p-hevc.mp4" || w != 720 || h != 1280 {
		t.Fatalf("opened %s %dx%d", name, w, h)
	}
	if l := p.Length(); l < 9800*time.Millisecond || l > 9900*time.Millisecond {
		t.Errorf("length %v, want about 9.88 s", l)
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
	run(300, func() bool { return p.Image() != nil })
	if p.Image() == nil {
		t.Fatal("no frame shown")
	}
	if pw, ph := p.Size(); pw != 720 || ph != 1280 {
		t.Fatalf("picture %dx%d", pw, ph)
	}
	if err := p.Seek(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	run(600, func() bool { return p.Position() > 5*time.Second })
	if pos := p.Position(); pos <= 5*time.Second || pos > 5300*time.Millisecond {
		t.Fatalf("position %v after seeking to 5 s", pos)
	}
}
