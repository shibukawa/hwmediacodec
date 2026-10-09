// Command hls streams the fireworks show (see internal/fireworks; keys
// launch shells) to browsers as live HLS and opens the player page:
//
//	go run ./hls -addr :8080
//	go run ./hls -open=false        # print the URL only
//
// The screen is captured and encoded by capture.Recorder, cut into
// fragmented-MP4 segments by mp4.Segmenter at every keyframe once
// the segment length has passed (the encoder's keyframe interval is set to
// exactly that length), and served from memory with a sliding-window
// playlist. Safari plays it natively, other browsers through hls.js.
// Expect a few seconds of latency: that is HLS, not the codec.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/pkg/browser"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/capture"
	"github.com/shibukawa/hwmediacodec/examples/internal/fireworks"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
	"github.com/shibukawa/hwmediacodec/net/hls"
)

type game struct {
	scene *fireworks.Scene
	rec   *capture.Recorder
	url   string

	minimize bool
	ticks    int
}

func (g *game) Update() error {
	if err := g.rec.Err(); err != nil {
		return err
	}
	if g.minimize && g.ticks == ebiten.TPS() {
		ebiten.MinimizeWindow()
	}
	g.ticks++
	g.scene.HandleInput()
	g.scene.Update()
	return nil
}

func (g *game) Draw(screen *ebiten.Image) {
	g.scene.Draw(screen)
	g.rec.Capture(screen)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("LIVE %s  %d frames  %d dropped  %.0f fps  %s\n%s",
		g.url, g.rec.Captured(), g.rec.Dropped(), ebiten.ActualFPS(), g.scene.Status(), fireworks.Help()), 8, g.scene.H-32)
}

func (g *game) Layout(int, int) (int, int) { return g.scene.W, g.scene.H }

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	codecName := flag.String("codec", "h264", "h264 or hevc (HEVC plays in Safari only)")
	bitrate := flag.Int("bitrate", 4_000_000, "bits per second")
	fps := flag.Float64("fps", 60, "frame rate")
	size := flag.String("size", "1280x720", "screen size")
	segment := flag.Duration("segment", 2*time.Second, "segment length")
	window := flag.Int("window", 6, "segments kept in the playlist")
	open := flag.Bool("open", true, "open the player page in the default browser")
	auto := flag.Bool("auto", false, "skip the title screen and run the automatic show")
	minimized := flag.Bool("minimized", false, "minimize the window after start; rendering and the stream carry on without it on screen")
	flag.Parse()

	var w, h int
	if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		log.Fatalf("bad -size %q", *size)
	}
	codec := hwmediacodec.H264
	if strings.EqualFold(*codecName, "hevc") {
		codec = hwmediacodec.HEVC
	}
	playlist := hls.NewPlaylist(*window, *segment)
	seg, err := mp4.NewSegmenter(codec, capture.TimeScale, *segment, playlist.SetInit, playlist.Add)
	if err != nil {
		log.Fatal(err)
	}
	rec, err := capture.New(w, h, seg, capture.Options{
		Codec:            codec,
		FPS:              *fps,
		Bitrate:          *bitrate,
		KeyframeInterval: int(*fps * segment.Seconds()),
		LowLatency:       true,
	})
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		if err := http.ListenAndServe(*addr, withPage(playlist)); err != nil {
			log.Fatal(err)
		}
	}()
	url := "http://" + *addr + "/"
	if strings.HasPrefix(*addr, ":") {
		url = "http://localhost" + *addr + "/"
	}
	fmt.Println("serving", url)
	if *open {
		if err := browser.OpenURL(url); err != nil {
			fmt.Fprintln(os.Stderr, "could not open the browser:", err)
		}
	}

	g := &game{scene: fireworks.NewScene(w, h, *fps), rec: rec, url: url}
	if *auto {
		g.scene.Start(true)
	}
	g.minimize = *minimized
	ebiten.SetWindowSize(w, h)
	ebiten.SetWindowTitle("hwmediacodec hls")
	ebiten.SetTPS(int(*fps))
	runErr := ebiten.RunGame(g)
	playlist.End()
	if err := rec.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "hls:", err)
		os.Exit(1)
	}
	if runErr != nil {
		log.Fatal(runErr)
	}
}
