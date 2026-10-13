//go:build js

package ebitenvideo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"syscall/js"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// loopGame plays a stream in a real Ebitengine game loop, stops on a frame
// and reads the image back.
type loopGame struct {
	player  *Player
	ticks   int
	shown   int    // ticks on which the image existed
	pixels  []byte // the image, read back while paused
	failure string
}

const framesToPlay = 40

func (g *loopGame) Update() error {
	g.ticks++
	if g.ticks > 3600 {
		g.failure = "the game ran for a minute without showing enough frames"
		return ebiten.Termination
	}
	if g.pixels == nil && g.shown >= framesToPlay {
		// Paused on the previous tick: the picture is settled.
		w, h := g.player.Size()
		g.pixels = make([]byte, w*h*4)
		g.player.Image().ReadPixels(g.pixels)
		return ebiten.Termination
	}
	if err := g.player.Update(); err != nil {
		g.failure = err.Error()
		return ebiten.Termination
	}
	if g.player.Image() != nil {
		if g.shown++; g.shown >= framesToPlay {
			g.player.Pause()
		}
	}
	return nil
}

func (g *loopGame) Draw(screen *ebiten.Image) {
	if img := g.player.Image(); img != nil {
		screen.DrawImage(img, nil)
	}
}

func (g *loopGame) Layout(int, int) (int, int) { return 640, 360 }

// TestPlayerInGameLoop runs the player inside ebiten.RunGame in the
// browser (scripts/browser_test.sh ./ebitenvideo): decoding through
// WebCodecs while Ebitengine drives the page's one thread, and the picture
// that ends up in the image, read back and compared with the stream
// decoded on its own. Outside a browser test run it skips; the other tests
// drive Update without a game loop.
func TestPlayerInGameLoop(t *testing.T) {
	if os.Getenv("HWMEDIACODEC_TEST_HOST") == "" || js.Global().Get("VideoDecoder").Type() != js.TypeFunction {
		t.Skip("needs a browser: scripts/browser_test.sh ./ebitenvideo")
	}
	ctx := context.Background()
	if !hwmediacodec.HasHardware(ctx, hwmediacodec.H264, hwmediacodec.Decode) {
		t.Skip("no hardware H.264 decoder in this browser")
	}
	const w, h = 640, 360
	s := testutil.GenerateStream(t, hwmediacodec.H264, w, h, 30)
	stream := testutil.ReadFile(t, s.Path)

	player, err := NewPlayer(bytes.NewReader(stream), hwmediacodec.H264, 30, WithLoop())
	if err != nil {
		t.Fatal(err)
	}
	defer player.Close()
	player.Play()
	g := &loopGame{player: player}
	if err := ebiten.RunGame(g); err != nil {
		t.Fatal(err)
	}
	if g.failure != "" {
		t.Fatalf("%s (after %d ticks, %d with a picture)", g.failure, g.ticks, g.shown)
	}
	if pw, ph := player.Size(); pw != w || ph != h {
		t.Fatalf("picture %dx%d, want %dx%d", pw, ph, w, h)
	}

	// The picture read back must be one of the stream's frames.
	dec, err := hwmediacodec.NewDecoder(ctx, hwmediacodec.H264, hwmediacodec.WithOutputFormat(hwmediacodec.RGBA))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	best, index, n := 0.0, -1, 0
	look := func(end error) {
		for {
			f, err := dec.Receive(ctx)
			if err == end {
				return
			}
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			if p := testutil.BlockPSNR(g.pixels, f.Planes[0], w, h, 8); p > best {
				best, index = p, n
			}
			n++
			f.Release()
		}
	}
	r := annexb.NewReader(bytes.NewReader(stream), hwmediacodec.H264)
	for {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := dec.Send(ctx, hwmediacodec.Packet{Data: au}); err != nil && !errors.Is(err, hwmediacodec.ErrAgain) {
			t.Fatal(err)
		}
		look(hwmediacodec.ErrAgain)
	}
	if err := dec.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	look(io.EOF)
	t.Logf("after %d ticks the image holds frame %d of %d: %.1f dB (8x8 blocks) against that frame decoded on its own", g.ticks, index, n, best)
	if best < 35 {
		t.Errorf("the image read back matches no frame of the stream (best %.1f dB)", best)
	}
}
