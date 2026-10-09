// Command hls streams a small Ebitengine animation to browsers as live HLS:
//
//	go run ./hls -addr :8080
//	open http://localhost:8080/
//
// The screen is captured and encoded by screencast.Recorder, cut into
// fragmented-MP4 segments by container.Segmenter at every keyframe once
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

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/examples/container"
	"github.com/shibukawa/hwmediacodec/examples/internal/demo"
	"github.com/shibukawa/hwmediacodec/examples/screencast"
)

type game struct {
	scene *demo.Scene
	rec   *screencast.Recorder
	url   string
}

func (g *game) Update() error {
	if err := g.rec.Err(); err != nil {
		return err
	}
	g.scene.Update()
	return nil
}

func (g *game) Draw(screen *ebiten.Image) {
	g.scene.Draw(screen)
	g.rec.Capture(screen)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("LIVE %s  %d frames  %d dropped  %.0f fps", g.url, g.rec.Captured(), g.rec.Dropped(), ebiten.ActualFPS()), 8, g.scene.H-20)
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
	flag.Parse()

	var w, h int
	if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		log.Fatalf("bad -size %q", *size)
	}
	codec := hwmediacodec.H264
	if strings.EqualFold(*codecName, "hevc") {
		codec = hwmediacodec.HEVC
	}
	playlist := NewPlaylist(*window, *segment)
	seg, err := container.NewSegmenter(codec, screencast.TimeScale, *segment, playlist.SetInit, playlist.Add)
	if err != nil {
		log.Fatal(err)
	}
	rec, err := screencast.New(w, h, seg, screencast.Options{
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
		if err := http.ListenAndServe(*addr, playlist); err != nil {
			log.Fatal(err)
		}
	}()
	url := "http://" + *addr + "/"
	if strings.HasPrefix(*addr, ":") {
		url = "http://localhost" + *addr + "/"
	}
	fmt.Println("serving", url)

	g := &game{scene: demo.NewScene(w, h, *fps), rec: rec, url: url}
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
