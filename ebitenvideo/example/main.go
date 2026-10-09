// Command example plays an H.264 or HEVC elementary stream in a window:
//
//	go run ./example -codec h264 -fps 30 video.h264
//
// Space pauses and resumes. The stream loops.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/ebitenvideo"
)

type game struct {
	player *ebitenvideo.Player
}

func (g *game) Update() error {
	if inpututil.IsKeyJustPressed(ebiten.KeySpace) {
		if g.player.IsPlaying() {
			g.player.Pause()
		} else {
			g.player.Play()
		}
	}
	return g.player.Update()
}

func (g *game) Draw(screen *ebiten.Image) {
	img := g.player.Image()
	if img == nil {
		ebitenutil.DebugPrint(screen, "decoding...")
		return
	}
	sw, sh := screen.Bounds().Dx(), screen.Bounds().Dy()
	w, h := g.player.Size()
	scale := min(float64(sw)/float64(w), float64(sh)/float64(h))
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(scale, scale)
	op.GeoM.Translate((float64(sw)-float64(w)*scale)/2, (float64(sh)-float64(h)*scale)/2)
	screen.DrawImage(img, op)
	ebitenutil.DebugPrint(screen, fmt.Sprintf("%.1f fps  position %.2fs  skipped %d", ebiten.ActualFPS(), g.player.Position().Seconds(), g.player.Skipped()))
}

func (g *game) Layout(outsideWidth, outsideHeight int) (int, int) {
	return outsideWidth, outsideHeight
}

func main() {
	codecName := flag.String("codec", "h264", "stream codec: h264 or hevc")
	fps := flag.Float64("fps", 30, "frame rate of the stream")
	software := flag.Bool("software", false, "allow the OS software decoder")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: example [-codec h264|hevc] [-fps 30] file.h264")
		os.Exit(2)
	}
	var c hwmediacodec.Codec
	switch *codecName {
	case "h264":
		c = hwmediacodec.H264
	case "hevc", "h265":
		c = hwmediacodec.HEVC
	default:
		log.Fatalf("unknown codec %q", *codecName)
	}
	f, err := os.Open(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	opts := []ebitenvideo.Option{ebitenvideo.WithLoop()}
	if *software {
		opts = append(opts, ebitenvideo.WithSoftwareFallback())
	}
	player, err := ebitenvideo.NewPlayer(f, c, *fps, opts...)
	if err != nil {
		log.Fatal(err)
	}
	defer player.Close()
	player.Play()

	ebiten.SetWindowSize(960, 540)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowTitle("hwmediacodec " + flag.Arg(0))
	if err := ebiten.RunGame(&game{player: player}); err != nil {
		log.Fatal(err)
	}
}
