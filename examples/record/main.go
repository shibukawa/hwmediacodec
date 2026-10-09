// Command record runs a small Ebitengine animation and records its screen
// into an MP4 file with the hardware encoder:
//
//	go run ./record -o capture.mp4 -seconds 10
//	go run ./record -o capture.mp4 -codec hevc -bitrate 12M -fps 60
//
// Close the window (or wait for -seconds) to finish the file. The capture
// is the screencast.Recorder: screen.ReadPixels in Draw, RGBA frames to
// the encoder on a goroutine, packets into the MP4 muxer.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/examples/internal/demo"
	"github.com/shibukawa/hwmediacodec/examples/screencast"
)

type game struct {
	scene   *demo.Scene
	rec     *screencast.Recorder
	limit   time.Duration
	start   time.Time
	capTime time.Duration // time spent in Capture (ReadPixels), for the summary
	draws   int
}

func (g *game) Update() error {
	if g.start.IsZero() {
		g.start = time.Now()
	}
	if err := g.rec.Err(); err != nil {
		return err
	}
	if g.limit > 0 && time.Since(g.start) > g.limit {
		return ebiten.Termination
	}
	g.scene.Update()
	return nil
}

func (g *game) Draw(screen *ebiten.Image) {
	g.scene.Draw(screen)
	t0 := time.Now()
	g.rec.Capture(screen) // before the overlay, so the stats stay out of the file
	g.capTime += time.Since(t0)
	g.draws++
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("REC %s  %d frames  %d dropped  %.0f fps", g.rec.Duration().Truncate(time.Second), g.rec.Captured(), g.rec.Dropped(), ebiten.ActualFPS()), 8, g.scene.H-20)
}

func (g *game) Layout(int, int) (int, int) { return g.scene.W, g.scene.H }

func main() {
	out := flag.String("o", "capture.mp4", "output file")
	codecName := flag.String("codec", "h264", "h264 or hevc")
	bitrate := flag.Int("bitrate", 8_000_000, "bits per second (0 = encoder default)")
	fps := flag.Float64("fps", 60, "frame rate of the game and the recording")
	size := flag.String("size", "1280x720", "screen size")
	seconds := flag.Float64("seconds", 0, "stop after this many seconds (0 = until the window closes)")
	flag.Parse()

	var w, h int
	if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		log.Fatalf("bad -size %q", *size)
	}
	codec := hwmediacodec.H264
	if strings.EqualFold(*codecName, "hevc") {
		codec = hwmediacodec.HEVC
	}
	sink, err := screencast.NewMP4File(*out, codec)
	if err != nil {
		log.Fatal(err)
	}
	rec, err := screencast.New(w, h, sink, screencast.Options{Codec: codec, FPS: *fps, Bitrate: *bitrate})
	if err != nil {
		log.Fatal(err)
	}
	g := &game{scene: demo.NewScene(w, h, *fps), rec: rec, limit: time.Duration(*seconds * float64(time.Second))}
	ebiten.SetWindowSize(w, h)
	ebiten.SetWindowTitle("hwmediacodec record")
	ebiten.SetTPS(int(*fps))
	runErr := ebiten.RunGame(g)
	if err := rec.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "record:", err)
		os.Exit(1)
	}
	if runErr != nil {
		log.Fatal(runErr)
	}
	fmt.Printf("%s: %d frames, %d dropped, %s; capture %.2f ms per draw over %d draws\n",
		*out, rec.Captured(), rec.Dropped(), rec.Duration().Truncate(time.Millisecond),
		float64(g.capTime.Microseconds())/1000/float64(max(g.draws, 1)), g.draws)
}
