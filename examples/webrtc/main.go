// Command webrtc streams the fireworks show (see internal/fireworks; keys
// launch shells) to browsers over WebRTC with about a hundred milliseconds
// of latency, and opens the player page:
//
//	go run ./webrtc -addr :8080
//	go run ./webrtc -open=false     # print the URL only
//
// The screen is captured and encoded by screencast.Recorder (H.264,
// low-latency mode, no B-frames) and every access unit is written to a
// pion track per viewer; pion packetises the Annex-B NAL units into RTP.
// Signalling is one HTTP POST of the browser's SDP offer. A viewer joining
// or reporting a picture loss makes the encoder emit a keyframe.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/pkg/browser"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/examples/internal/fireworks"
	"github.com/shibukawa/hwmediacodec/examples/screencast"
)

type game struct {
	scene *fireworks.Scene
	rec   *screencast.Recorder
	bc    *Broadcaster
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
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("WebRTC %s  %s  %d frames  %d dropped  %.0f fps  %s\n%s",
		g.url, g.bc, g.rec.Captured(), g.rec.Dropped(), ebiten.ActualFPS(), g.scene.Status(), fireworks.Help()), 8, g.scene.H-32)
}

func (g *game) Layout(int, int) (int, int) { return g.scene.W, g.scene.H }

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	bitrate := flag.Int("bitrate", 3_000_000, "bits per second")
	fps := flag.Float64("fps", 60, "frame rate")
	size := flag.String("size", "1280x720", "screen size")
	stun := flag.String("stun", "", "STUN server URL for viewers outside the LAN, e.g. stun:stun.l.google.com:19302")
	open := flag.Bool("open", true, "open the player page in the default browser")
	auto := flag.Bool("auto", false, "skip the title screen and run the automatic show")
	minimized := flag.Bool("minimized", false, "minimize the window after start; rendering and the stream carry on without it on screen")
	flag.Parse()

	var w, h int
	if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		log.Fatalf("bad -size %q", *size)
	}
	var ice []string
	if *stun != "" {
		ice = []string{*stun}
	}
	var rec *screencast.Recorder
	bc := NewBroadcaster(*fps, ice, func() {
		if rec != nil {
			rec.RequestKeyframe()
		}
	})
	rec, err := screencast.New(w, h, bc, screencast.Options{
		Codec:            hwmediacodec.H264,
		FPS:              *fps,
		Bitrate:          *bitrate,
		KeyframeInterval: int(*fps * 4), // viewers get one on join anyway
		LowLatency:       true,
		Profile:          hwmediacodec.ProfileBaseline,
	})
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		if err := http.ListenAndServe(*addr, bc); err != nil {
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

	g := &game{scene: fireworks.NewScene(w, h, *fps), rec: rec, bc: bc, url: url}
	if *auto {
		g.scene.Start(true)
	}
	g.minimize = *minimized
	ebiten.SetWindowSize(w, h)
	ebiten.SetWindowTitle("hwmediacodec webrtc")
	ebiten.SetTPS(int(*fps))
	runErr := ebiten.RunGame(g)
	if err := rec.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "webrtc:", err)
		os.Exit(1)
	}
	if runErr != nil {
		log.Fatal(runErr)
	}
}
