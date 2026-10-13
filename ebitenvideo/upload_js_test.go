//go:build js

package ebitenvideo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall/js"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// directGame plays a stream in a real Ebitengine game loop: first with
// every frame copied, then with the direct upload allowed, then it stops
// on a frame and reads the image back.
type directGame struct {
	t        *testing.T
	player   *Player
	rgba     *Player // the same stream with RGBA frames from the decoder
	fallback *Player // the same stream where the texture cannot be found
	phase    int
	ticks    int
	shown    []byte // the image's pixels, read back while paused
	// after the fallback: the position it happened at, the frames shown
	// since, and the image read back
	fellAt    time.Duration
	fellShown int
	afterFall []byte
	failure   string
}

const (
	copiedFrames = 30 // frames to play on the copy path
	directFrames = 60 // frames to play on the direct path
)

func (g *directGame) Update() error {
	g.ticks++
	if g.ticks > 3600 {
		g.failure = "the game ran for a minute without getting there"
		return ebiten.Termination
	}
	if g.phase < 0 {
		// The way it works outside the browser, for comparison.
		if err := g.rgba.Update(); err != nil {
			g.failure = err.Error()
			return ebiten.Termination
		}
		if g.rgba.direct.stats.copied >= copiedFrames {
			g.rgba.Pause()
			g.player.Play()
			g.phase = 0
		}
		return nil
	}
	if err := g.player.Update(); err != nil {
		g.failure = err.Error()
		return ebiten.Termination
	}
	stats := &g.player.direct.stats
	switch g.phase {
	case 0: // copy
		if stats.copied >= copiedFrames {
			forceCopy = false
			g.phase = 1
		}
	case 1: // direct
		if g.player.direct.broken {
			g.failure = "the direct upload gave up"
			return ebiten.Termination
		}
		if stats.direct >= directFrames {
			g.player.Pause()
			g.phase = 2
		}
	case 2: // the picture on the screen is one that was uploaded directly
		if !g.player.ZeroCopy() {
			g.failure = "the frame that is showing was copied"
			return ebiten.Termination
		}
		w, h := g.player.Size()
		g.shown = make([]byte, w*h*4)
		g.player.Image().ReadPixels(g.shown)
		breakDirect = true
		g.fallback.Play()
		g.phase = 3
	case 3, 4: // a player that cannot find its texture goes back to RGBA frames
		if err := g.fallback.Update(); err != nil {
			g.failure = "after the fallback: " + err.Error()
			return ebiten.Termination
		}
		f := g.fallback
		if g.phase == 3 {
			if f.direct.fellBack {
				g.fellAt, g.fellShown = f.Position(), f.direct.stats.copied
				g.phase = 4
			}
			return nil
		}
		// Twenty frames from the reopened decoder, and the clock moved on.
		if f.direct.stats.copied >= g.fellShown+20 && f.Position() > g.fellAt+300*time.Millisecond {
			f.Pause()
			w, h := f.Size()
			g.afterFall = make([]byte, w*h*4)
			f.Image().ReadPixels(g.afterFall)
			return ebiten.Termination
		}
	}
	return nil
}

func (g *directGame) Draw(screen *ebiten.Image) {
	shown := g.player
	switch {
	case g.phase < 0:
		shown = g.rgba
	case g.phase >= 3:
		shown = g.fallback
	}
	if img := shown.Image(); img != nil {
		screen.DrawImage(img, nil)
	}
}

func (g *directGame) Layout(int, int) (int, int) { return 640, 360 }

// TestDirectUpload runs the player inside ebiten.RunGame in the browser
// (scripts/browser_test.sh ./ebitenvideo) and checks the short way from
// the decoder to the texture: that the player finds Ebitengine's texture,
// that the frames it uploads there are the stream's pictures, the right
// way up and in the right colours (the image read back is compared with
// the same stream decoded to RGBA), and what it saves.
func TestDirectUpload(t *testing.T) {
	if os.Getenv("HWMEDIACODEC_TEST_HOST") == "" || js.Global().Get("VideoDecoder").Type() != js.TypeFunction {
		t.Skip("needs a browser: scripts/browser_test.sh ./ebitenvideo")
	}
	ctx := context.Background()
	if !hwmediacodec.HasHardware(ctx, hwmediacodec.H264, hwmediacodec.Decode) {
		t.Skip("no hardware H.264 decoder in this browser")
	}
	// HWMEDIACODEC_TEST_SIZE=3840x2160 times a larger picture.
	w, h := 1280, 720
	if size := os.Getenv("HWMEDIACODEC_TEST_SIZE"); size != "" {
		if _, err := fmt.Sscanf(size, "%dx%d", &w, &h); err != nil {
			t.Fatalf("HWMEDIACODEC_TEST_SIZE=%q: %v", size, err)
		}
	}
	s := testutil.GenerateStream(t, hwmediacodec.H264, w, h, 30)
	stream := testutil.ReadFile(t, s.Path)

	// The RGBA way does part of its work outside Player.Update: the
	// decoder copies every frame into Go memory, and Ebitengine uploads
	// the pixels WritePixels took when it flushes the frame. Both are
	// timed here so that the comparison is of whole frames: the upload by
	// wrapping texSubImage2D (which has to happen before Ebitengine
	// starts), the copy by doing one of the same size.
	var flushTime time.Duration
	flushes := 0
	proto := js.Global().Get("WebGL2RenderingContext").Get("prototype")
	original := proto.Get("texSubImage2D")
	proto.Set("texSubImage2D", js.FuncOf(func(this js.Value, args []js.Value) any {
		list := make([]any, len(args))
		for i, a := range args {
			list[i] = a
		}
		start := time.Now()
		ret := original.Call("apply", this, list)
		if len(args) >= 9 && args[4].Int() == w && args[5].Int() == h {
			flushTime += time.Since(start)
			flushes++
		}
		return ret
	}))
	bytesJS := js.Global().Get("Uint8Array").New(w * h * 4)
	bytesGo := make([]byte, w*h*4)
	start := time.Now()
	for i := 0; i < 20; i++ {
		js.CopyBytesToGo(bytesGo, bytesJS)
	}
	copyIn := time.Since(start) / 20

	forceRGBA = true
	rgba, err := NewPlayer(bytes.NewReader(stream), hwmediacodec.H264, 30, WithLoop())
	forceRGBA = false
	if err != nil {
		t.Fatal(err)
	}
	defer rgba.Close()
	rgba.Play()
	player, err := NewPlayer(bytes.NewReader(stream), hwmediacodec.H264, 30, WithLoop())
	if err != nil {
		t.Fatal(err)
	}
	defer player.Close()
	if uploadFormat() != hwmediacodec.Native {
		t.Skip("in this browser the player takes RGBA frames from the decoder; there is no direct upload to test")
	}
	fallback, err := NewPlayer(bytes.NewReader(stream), hwmediacodec.H264, 30, WithLoop())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { fallback.Close() }()
	forceCopy = true
	defer func() { forceCopy, breakDirect = false, false }()
	g := &directGame{t: t, player: player, rgba: rgba, fallback: fallback, phase: -1}
	if err := ebiten.RunGame(g); err != nil {
		t.Fatal(err)
	}
	if g.failure != "" {
		t.Fatalf("%s (after %d ticks; %d frames copied, %d direct, %d texture allocations seen)",
			g.failure, g.ticks, player.direct.stats.copied, player.direct.stats.direct, textures.seq)
	}
	stats := player.direct.stats
	copied := stats.copiedTime / time.Duration(max(stats.copied, 1))
	direct := stats.directTime / time.Duration(max(stats.direct, 1))
	pixels := rgba.direct.stats.copiedTime / time.Duration(max(rgba.direct.stats.copied, 1))
	flush := flushTime / time.Duration(max(flushes, 1))
	round := func(d time.Duration) time.Duration { return d.Round(10 * time.Microsecond) }
	t.Logf("%dx%d, time per frame on the page's one thread:", w, h)
	t.Logf("  RGBA frames:   %v = %v copy into Go memory + %v WritePixels + %v texture upload at the flush (and the browser reads the frame back from the GPU)",
		round(copyIn+pixels+flush), round(copyIn), round(pixels), round(flush))
	t.Logf("  direct upload: %v, one texSubImage2D from the VideoFrame (%d frames)", round(direct), stats.direct)
	t.Logf("  (a VideoFrame read back through a canvas, the fallback before the texture is found: %v)", round(copied))
	if stats.direct < directFrames {
		t.Fatalf("only %d frames took the direct way", stats.direct)
	}

	// The picture read back from the image must be one of the stream's:
	// decode the stream to RGBA and look for it.
	dec, err := hwmediacodec.NewDecoder(ctx, hwmediacodec.H264, hwmediacodec.WithOutputFormat(hwmediacodec.RGBA))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	if !fallback.direct.fellBack || g.afterFall == nil {
		t.Fatal("the player that could not find its texture did not go back to RGBA frames")
	}
	if fallback.ZeroCopy() {
		t.Error("ZeroCopy is true after the fallback")
	}
	bestAfter := 0.0
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
			if p := testutil.BlockPSNR(g.shown, f.Planes[0], w, h, 8); p > best {
				best, index = p, n
			}
			bestAfter = max(bestAfter, testutil.BlockPSNR(g.afterFall, f.Planes[0], w, h, 8))
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
	t.Logf("the image holds frame %d of %d: %.1f dB (8x8 blocks) against the same frame decoded to RGBA", index, n, best)
	if best < 35 {
		t.Errorf("the image read back matches no frame of the stream (best %.1f dB): wrong texture, orientation or colours", best)
	}
	t.Logf("after the fallback at %v the player went on with RGBA frames; its image is %.1f dB from a frame of the stream", g.fellAt.Round(time.Millisecond), bestAfter)
	if bestAfter < 35 {
		t.Errorf("the image after the fallback matches no frame of the stream (best %.1f dB)", bestAfter)
	}
}
