package mobile

import (
	"context"
	"testing"

	"github.com/shibukawa/hwmediacodec"
)

// TestOpensBundledClip runs the first Update's work (probe, demux, open the
// decoder) without a window, the way the mobile view controller will.
func TestOpensBundledClip(t *testing.T) {
	ctx := context.Background()
	if !hwmediacodec.HasHardware(ctx, hwmediacodec.HEVC, hwmediacodec.Decode) {
		t.Skip("no hardware HEVC decoder on this machine")
	}
	g := newGame()
	g.open()
	defer g.Close()
	if g.err != nil {
		t.Fatalf("open: %v", g.err)
	}
	if g.player == nil || g.vw == 0 || g.vh == 0 {
		t.Fatalf("no player or picture size after open: %+v", g)
	}
	if g.info == "" {
		t.Error("the probe summary is empty")
	}
	t.Log(g.info)
}
