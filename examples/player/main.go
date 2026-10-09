// Command player plays an MP4 file in an Ebitengine window with the
// hardware decoder: the plain video-player sample.
//
//	go run ./player                     # the bundled waterfall clip
//	go run ./player movie.mp4
//	go run ./player -codec h264 -fps 30 stream.h264
//
// Space pauses, the left and right arrow keys seek five seconds, Home
// restarts, L toggles looping. The window takes the video's aspect ratio.
// MP4 files go through mp4.VideoTrack.PacketSource, which gives
// ebitenvideo presentation times, the sync-sample table for seeking and
// the length; raw streams go through ebitenvideo.NewPlayer. Audio tracks
// are ignored (Ebitengine has no AAC decoder).
package main

import (
	"flag"
	"fmt"
	"image/color"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/ebitenvideo"
	"github.com/shibukawa/hwmediacodec/examples/assets"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

type game struct {
	w, h   int
	player *ebitenvideo.Player
	name   string
	pixel  *ebiten.Image
	status string
	at     time.Time
}

func (g *game) say(s string) { g.status, g.at = s, time.Now() }

func (g *game) Update() error {
	if inpututil.IsKeyJustPressed(ebiten.KeySpace) {
		if g.player.IsPlaying() {
			g.player.Pause()
			g.say("paused")
		} else {
			g.player.Play()
			g.say("playing")
		}
	}
	var seek time.Duration = -1
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyArrowRight):
		seek = g.player.Position() + 5*time.Second
	case inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft):
		seek = g.player.Position() - 5*time.Second
	case inpututil.IsKeyJustPressed(ebiten.KeyHome):
		seek = 0
	}
	if seek >= 0 {
		if err := g.player.Seek(seek); err != nil {
			g.say(err.Error())
		} else {
			g.player.Play()
			g.say(fmt.Sprintf("seek %.1fs", seek.Seconds()))
		}
	}
	return g.player.Update()
}

func (g *game) Draw(screen *ebiten.Image) {
	screen.Fill(color.RGBA{18, 18, 22, 255})
	img := g.player.Image()
	if img == nil {
		ebitenutil.DebugPrint(screen, "decoding...")
		return
	}
	vw, vh := g.player.Size()
	scale := math.Min(float64(g.w)/float64(vw), float64(g.h)/float64(vh))
	op := &ebiten.DrawImageOptions{Filter: ebiten.FilterLinear}
	op.GeoM.Scale(scale, scale)
	op.GeoM.Translate((float64(g.w)-float64(vw)*scale)/2, (float64(g.h)-float64(vh)*scale)/2)
	screen.DrawImage(img, op)

	// Progress bar along the bottom edge.
	if l := g.player.Length(); l > 0 {
		frac := math.Min(1, g.player.Position().Seconds()/l.Seconds())
		g.rect(screen, 0, float64(g.h)-4, float64(g.w), 4, color.RGBA{255, 255, 255, 60})
		g.rect(screen, 0, float64(g.h)-4, float64(g.w)*frac, 4, color.RGBA{255, 200, 80, 220})
	}
	status := g.status
	if time.Since(g.at) > 2*time.Second {
		status = ""
	}
	ebitenutil.DebugPrint(screen, fmt.Sprintf("%s  %dx%d  %.2fs / %.1fs  %.0f fps  skipped %d  %s\n[space] pause  [<-][->] seek 5s  [home] restart",
		g.name, vw, vh, g.player.Position().Seconds(), g.player.Length().Seconds(), ebiten.ActualFPS(), g.player.Skipped(), status))
}

func (g *game) rect(dst *ebiten.Image, x, y, w, h float64, c color.RGBA) {
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(w, h)
	op.GeoM.Translate(x, y)
	op.ColorScale.ScaleWithColor(c)
	dst.DrawImage(g.pixel, op)
}

func (g *game) Layout(int, int) (int, int) { return g.w, g.h }

func main() {
	codecName := flag.String("codec", "h264", "codec of a raw elementary stream (ignored for MP4)")
	fps := flag.Float64("fps", 30, "frame rate of a raw elementary stream (ignored for MP4)")
	noLoop := flag.Bool("once", false, "stop at the end instead of looping")
	software := flag.Bool("software", false, "allow the OS software decoder")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: player [flags] [movie.mp4 | stream.h264]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() > 1 {
		flag.Usage()
		os.Exit(2)
	}
	var opts []ebitenvideo.Option
	if !*noLoop {
		opts = append(opts, ebitenvideo.WithLoop())
	}
	if *software {
		opts = append(opts, ebitenvideo.WithSoftwareFallback())
	}
	player, closer, name, vw, vh, err := open(flag.Arg(0), *codecName, *fps, opts)
	if err != nil {
		log.Fatal(err)
	}
	defer closer.Close()
	defer player.Close()
	player.Play()

	// A window with the video's aspect ratio, at most 1280x800.
	w, h := 1280, 720
	if vw > 0 && vh > 0 {
		s := math.Min(1280/float64(vw), 800/float64(vh))
		w, h = int(float64(vw)*s), int(float64(vh)*s)
	}
	pixel := ebiten.NewImage(1, 1)
	pixel.Fill(color.White)
	ebiten.SetWindowSize(w, h)
	ebiten.SetWindowTitle("hwmediacodec player - " + name)
	if err := ebiten.RunGame(&game{w: w, h: h, player: player, name: name, pixel: pixel}); err != nil {
		log.Fatal(err)
	}
}

// open returns a player for path (or the bundled clip when path is empty)
// and the picture size when the container knows it.
func open(path, codecName string, fps float64, opts []ebitenvideo.Option) (p *ebitenvideo.Player, closer io.Closer, name string, w, h int, err error) {
	var d *mp4.Demuxer
	switch {
	case path == "":
		name = assets.WaterfallName
		if d, err = mp4.NewDemuxer(assets.Waterfall()); err != nil {
			return nil, nil, "", 0, 0, err
		}
		closer = io.NopCloser(nil)
	case isMP4(path):
		name = filepath.Base(path)
		if d, err = mp4.Open(path); err != nil {
			return nil, nil, "", 0, 0, err
		}
		closer = d
	default:
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, "", 0, 0, err
		}
		codec := hwmediacodec.H264
		if c := strings.ToLower(codecName); c == "hevc" || c == "h265" {
			codec = hwmediacodec.HEVC
		}
		p, err := ebitenvideo.NewPlayer(f, codec, fps, opts...)
		if err != nil {
			f.Close()
			return nil, nil, "", 0, 0, err
		}
		return p, f, filepath.Base(path), 0, 0, nil
	}
	v := d.Video()
	if v == nil {
		closer.Close()
		return nil, nil, "", 0, 0, fmt.Errorf("%s has no H.264, HEVC or AV1 video track", name)
	}
	p, err = ebitenvideo.NewPlayerFromSource(v.PacketSource(), opts...)
	if err != nil {
		closer.Close()
		return nil, nil, "", 0, 0, err
	}
	return p, closer, name, v.Width, v.Height, nil
}

func isMP4(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".mov", ".m4v":
		return true
	}
	return false
}
