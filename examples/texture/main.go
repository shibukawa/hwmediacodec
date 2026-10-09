// Command texture plays a video as a texture in Ebitengine: flat, on the
// faces of a spinning cube drawn with DrawTriangles, and through a Kage
// shader.
//
//	go run ./texture movie.mp4
//	go run ./texture -codec hevc -fps 30 stream.hevc
//	go run ./texture -seconds 12 -record demo.mp4 movie.mp4
//
// Keys: 1 flat, 2 cube, 3 shader, space pause; without a key press the
// modes cycle every few seconds. MP4 input is demuxed by the container
// package into the elementary stream ebitenvideo plays; the frame rate
// comes from the sample table. The player's image is an ordinary
// *ebiten.Image updated in place, so it works wherever an image does.
package main

import (
	"flag"
	"fmt"
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
	"github.com/shibukawa/hwmediacodec/examples/container"
	"github.com/shibukawa/hwmediacodec/examples/screencast"
)

type mode int

const (
	modeFlat mode = iota
	modeCube
	modeShader
	modeCount
)

type game struct {
	w, h   int
	player *ebitenvideo.Player
	shader *ebiten.Shader
	rec    *screencast.Recorder
	mode   mode
	auto   bool
	tick   int
	limit  time.Duration
	start  time.Time
}

func (g *game) Update() error {
	if g.start.IsZero() {
		g.start = time.Now()
	}
	if g.limit > 0 && time.Since(g.start) > g.limit {
		return ebiten.Termination
	}
	if g.rec != nil {
		if err := g.rec.Err(); err != nil {
			return err
		}
	}
	for k, m := range map[ebiten.Key]mode{ebiten.Key1: modeFlat, ebiten.Key2: modeCube, ebiten.Key3: modeShader} {
		if inpututil.IsKeyJustPressed(k) {
			g.mode, g.auto = m, false
		}
	}
	if inpututil.IsKeyJustPressed(ebiten.KeySpace) {
		if g.player.IsPlaying() {
			g.player.Pause()
		} else {
			g.player.Play()
		}
	}
	g.tick++
	if g.auto && g.tick%(4*ebiten.TPS()) == 0 {
		g.mode = (g.mode + 1) % modeCount
	}
	return g.player.Update()
}

func (g *game) Draw(screen *ebiten.Image) {
	screen.Fill(colorBG)
	img := g.player.Image()
	if img == nil {
		ebitenutil.DebugPrint(screen, "decoding...")
		return
	}
	vw, vh := g.player.Size()
	switch g.mode {
	case modeFlat:
		scale := math.Min(float64(g.w)/float64(vw), float64(g.h)/float64(vh))
		op := &ebiten.DrawImageOptions{Filter: ebiten.FilterLinear}
		op.GeoM.Scale(scale, scale)
		op.GeoM.Translate((float64(g.w)-float64(vw)*scale)/2, (float64(g.h)-float64(vh)*scale)/2)
		screen.DrawImage(img, op)
	case modeCube:
		t := float64(g.tick) / float64(ebiten.TPS())
		cam := camera{cx: float64(g.w) / 2, cy: float64(g.h) / 2, focal: float64(g.h) * 0.9, distance: 4}
		m := cubeMesh(cam, t*0.7, math.Sin(t*0.4)*0.6, float64(vw), float64(vh), 8)
		screen.DrawTriangles(m.vertices, m.indices, img, &ebiten.DrawTrianglesOptions{Filter: ebiten.FilterLinear})
	case modeShader:
		// DrawRectShader wants the destination rectangle and the source
		// images to have the same size, so draw at video size and scale
		// with GeoM.
		scale := math.Min(float64(g.w)/float64(vw), float64(g.h)/float64(vh))
		op := &ebiten.DrawRectShaderOptions{}
		op.Images[0] = img
		op.Uniforms = map[string]any{"Time": float32(g.tick) / float32(ebiten.TPS())}
		op.GeoM.Scale(scale, scale)
		op.GeoM.Translate((float64(g.w)-float64(vw)*scale)/2, (float64(g.h)-float64(vh)*scale)/2)
		screen.DrawRectShader(vw, vh, g.shader, op)
	}
	if g.rec != nil {
		g.rec.Capture(screen)
	}
	ebitenutil.DebugPrint(screen, fmt.Sprintf("[1] flat  [2] cube  [3] shader  [space] pause   mode %d  %.1f fps  video %.2fs  skipped %d",
		g.mode+1, ebiten.ActualFPS(), g.player.Position().Seconds(), g.player.Skipped()))
}

func (g *game) Layout(int, int) (int, int) { return g.w, g.h }

var colorBG = colorRGB(24, 26, 32)

func main() {
	codecName := flag.String("codec", "h264", "codec of a raw elementary stream (ignored for MP4)")
	fps := flag.Float64("fps", 30, "frame rate of a raw elementary stream (ignored for MP4)")
	size := flag.String("size", "1280x720", "window size")
	seconds := flag.Float64("seconds", 0, "exit after this many seconds")
	record := flag.String("record", "", "also record the window into this MP4 file")
	software := flag.Bool("software", false, "allow the OS software decoder")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: texture [flags] movie.mp4|stream.h264\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	var w, h int
	if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		log.Fatalf("bad -size %q", *size)
	}

	stream, codec, rate, closer, err := openVideo(flag.Arg(0), *codecName, *fps)
	if err != nil {
		log.Fatal(err)
	}
	defer closer.Close()
	opts := []ebitenvideo.Option{ebitenvideo.WithLoop()}
	if *software {
		opts = append(opts, ebitenvideo.WithSoftwareFallback())
	}
	player, err := ebitenvideo.NewPlayer(stream, codec, rate, opts...)
	if err != nil {
		log.Fatal(err)
	}
	defer player.Close()
	player.Play()

	shader, err := ebiten.NewShader([]byte(crtShader))
	if err != nil {
		log.Fatal(err)
	}
	g := &game{w: w, h: h, player: player, shader: shader, auto: true, limit: time.Duration(*seconds * float64(time.Second))}
	if *record != "" {
		sink, err := screencast.NewMP4File(*record, hwmediacodec.H264)
		if err != nil {
			log.Fatal(err)
		}
		g.rec, err = screencast.New(w, h, sink, screencast.Options{FPS: 60, Bitrate: 8_000_000})
		if err != nil {
			log.Fatal(err)
		}
	}
	ebiten.SetWindowSize(w, h)
	ebiten.SetWindowTitle("hwmediacodec texture")
	runErr := ebiten.RunGame(g)
	if g.rec != nil {
		if err := g.rec.Close(); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s: %d frames, %d dropped\n", *record, g.rec.Captured(), g.rec.Dropped())
	}
	if runErr != nil {
		log.Fatal(runErr)
	}
}

// openVideo returns the elementary stream of path: demuxed from an MP4, or
// the file itself for a raw .h264/.hevc stream.
func openVideo(path, codecName string, fps float64) (io.ReadSeeker, hwmediacodec.Codec, float64, io.Closer, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".mov", ".m4v":
		d, err := container.Open(path)
		if err != nil {
			return nil, 0, 0, nil, err
		}
		v := d.Video()
		if v == nil {
			d.Close()
			return nil, 0, 0, nil, fmt.Errorf("%s has no H.264, HEVC or AV1 video track", path)
		}
		return v.ElementaryStream(), v.Codec, v.FrameRate(), d, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, nil, err
	}
	codec := hwmediacodec.H264
	switch strings.ToLower(codecName) {
	case "hevc", "h265":
		codec = hwmediacodec.HEVC
	case "av1":
		codec = hwmediacodec.AV1
	}
	return f, codec, fps, f, nil
}
