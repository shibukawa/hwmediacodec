package main

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/pkg/browser"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/ebitenvideo"
	"github.com/shibukawa/hwmediacodec/examples/internal/fireworks"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

const (
	winW, winH = 1200, 760
	panelW     = 640 // the result list on the left; pictures on the right
	lineH      = 16
	charW      = 6 // of the debug font
)

type stage int

const (
	stAuto stage = iota
	stPlayback
	stWebRTC
	stDone
)

// question is what the playback stage is waiting for.
type question int

const (
	watching question = iota
	askPicture
	askSound
)

type game struct {
	r        *report
	out      string
	addr     string
	open     bool
	playLen  time.Duration
	autoDone atomic.Bool

	stage stage
	pixel *ebiten.Image

	skipPlayback, skipWebRTC bool

	// Playback.
	queue      []clip
	player     *ebitenvideo.Player
	playing    clip
	playStart  time.Time
	ask        question
	soundAsked bool

	// WebRTC.
	web    *web
	scene  *fireworks.Scene
	canvas *ebiten.Image

	// Done.
	saved   bool
	savedAt string
	message string
}

func newGame(r *report, out, addr string, open bool, playLen time.Duration) *game {
	pixel := ebiten.NewImage(1, 1)
	pixel.Fill(color.White)
	return &game{r: r, out: out, addr: addr, open: open, playLen: playLen, pixel: pixel}
}

func (g *game) Layout(int, int) (int, int) { return winW, winH }

func (g *game) Update() error {
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		if g.stage != stDone {
			g.finish()
		}
		return ebiten.Termination
	}
	switch g.stage {
	case stAuto:
		if g.autoDone.Load() {
			g.startPlayback()
		}
	case stPlayback:
		g.updatePlayback()
	case stWebRTC:
		g.scene.Update()
		if inpututil.IsKeyJustPressed(ebiten.KeyS) || inpututil.IsKeyJustPressed(ebiten.KeyEnter) || g.web.done() {
			g.finish()
		}
	case stDone:
		if inpututil.IsKeyJustPressed(ebiten.KeyO) {
			if err := browser.OpenURL(g.r.issueURL()); err != nil {
				g.message = "could not open the browser: " + err.Error()
			}
		}
	}
	return nil
}

// startPlayback queues the clips of every codec some backend decodes.
func (g *game) startPlayback() {
	g.stage = stPlayback
	if g.skipPlayback {
		g.startWebRTC()
		return
	}
	for _, cl := range clips() {
		for _, c := range g.r.caps {
			if c.Codec == cl.codec && c.Direction == hwmediacodec.Decode {
				g.queue = append(g.queue, cl)
				break
			}
		}
	}
	g.nextClip()
}

func (g *game) nextClip() {
	if g.player != nil {
		g.player.Close()
		g.player = nil
	}
	for len(g.queue) > 0 {
		cl := g.queue[0]
		g.queue = g.queue[1:]
		p, err := openClip(cl)
		if err != nil {
			res := g.r.add("Playback", codecName(cl.codec)+" picture")
			g.r.set(res, statusOf(err), "%v", err)
			continue
		}
		if err := p.AudioErr(); err != nil && !g.soundAsked {
			g.soundAsked = true
			res := g.r.add("Playback", "Sound (AAC)")
			g.r.set(res, failed, "%v", err)
		}
		p.Play()
		g.player, g.playing, g.playStart, g.ask = p, cl, time.Now(), watching
		return
	}
	g.startWebRTC()
}

func openClip(cl clip) (*ebitenvideo.Player, error) {
	d, err := mp4.NewDemuxer(cl.clip.Open())
	if err != nil {
		return nil, err
	}
	v := d.Video()
	if v == nil {
		return nil, fmt.Errorf("no video track in %s", cl.clip.Name)
	}
	var opts []ebitenvideo.Option
	if a := d.Audio(); a != nil {
		opts = append(opts, ebitenvideo.WithAudio(a.PacketSource()))
	}
	return ebitenvideo.NewPlayerFromSource(v.PacketSource(), opts...)
}

func (g *game) updatePlayback() {
	if err := g.player.Update(); err != nil {
		res := g.r.add("Playback", codecName(g.playing.codec)+" picture")
		g.r.set(res, failed, "%v", err)
		g.nextClip()
		return
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyS) {
		res := g.r.add("Playback", codecName(g.playing.codec)+" picture")
		g.r.set(res, skipped, "skipped by the user")
		g.nextClip()
		return
	}
	switch g.ask {
	case watching:
		if time.Since(g.playStart) >= g.playLen {
			g.ask = askPicture
		}
	case askPicture:
		if yes, ok := answer(); ok {
			res := g.r.add("Playback", codecName(g.playing.codec)+" picture")
			g.r.set(res, pick(yes), "user answered %s; %d frames skipped by the player", yesNo(yes), g.player.Skipped())
			if g.player.HasAudio() && !g.soundAsked {
				g.ask = askSound
				return
			}
			g.nextClip()
		}
	case askSound:
		if yes, ok := answer(); ok {
			g.soundAsked = true
			res := g.r.add("Playback", "Sound (AAC)")
			g.r.set(res, pick(yes), "user answered %s", yesNo(yes))
			g.nextClip()
		}
	}
}

// answer reads a Y or N key press.
func answer() (yes, ok bool) {
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyY):
		return true, true
	case inpututil.IsKeyJustPressed(ebiten.KeyN):
		return false, true
	}
	return false, false
}

func pick(yes bool) status {
	if yes {
		return passed
	}
	return failed
}

func yesNo(yes bool) string {
	if yes {
		return "yes"
	}
	return "no"
}

// startWebRTC opens a stream for every codec some backend encodes and
// opens the page.
func (g *game) startWebRTC() {
	if g.skipWebRTC {
		g.finish()
		return
	}
	var codecs []hwmediacodec.Codec
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC} {
		for _, cp := range g.r.caps {
			if cp.Codec == c && cp.Direction == hwmediacodec.Encode {
				codecs = append(codecs, c)
				break
			}
		}
	}
	if len(codecs) == 0 {
		res := g.r.add("WebRTC", "Streaming")
		g.r.set(res, unsupported, "no H.264 or HEVC encoder on this machine")
		g.finish()
		return
	}
	w, err := startWeb(g.r, g.addr, codecs)
	if err != nil {
		res := g.r.add("WebRTC", "Page")
		g.r.set(res, failed, "%v", err)
		g.finish()
		return
	}
	if len(w.streams) == 0 {
		g.finish()
		return
	}
	g.web = w
	g.scene = fireworks.NewScene(webW, webH, webFPS)
	g.scene.Start(true)
	g.canvas = ebiten.NewImage(webW, webH)
	g.stage = stWebRTC
	fmt.Println("WebRTC check page:", w.url)
	if g.open {
		if err := browser.OpenURL(w.url); err != nil {
			g.message = "could not open the browser: " + err.Error()
		}
	}
}

// finish ends the checks and saves the report.
func (g *game) finish() {
	g.close()
	g.stage = stDone
	if g.saved {
		return
	}
	g.saved = true
	finish(g.r, g.out)
	if abs, err := filepath.Abs(g.out); err == nil {
		g.savedAt = abs
	}
	if _, err := os.Stat(g.out); err != nil {
		g.savedAt = ""
	}
}

func (g *game) close() {
	if g.player != nil {
		g.player.Close()
		g.player = nil
	}
	if g.web != nil {
		g.web.close()
		g.web = nil
	}
}

func (g *game) Draw(screen *ebiten.Image) {
	screen.Fill(color.RGBA{20, 22, 27, 255})
	g.rect(screen, panelW, 0, 1, winH, color.RGBA{60, 64, 72, 255})
	switch g.stage {
	case stPlayback:
		if g.player != nil {
			if img := g.player.Image(); img != nil {
				vw, vh := g.player.Size()
				g.fit(screen, img, vw, vh)
			}
		}
	case stWebRTC:
		g.scene.Draw(g.canvas)
		for _, s := range g.web.streams {
			s.rec.Capture(g.canvas)
		}
		g.fit(screen, g.canvas, webW, webH)
	}
	g.drawPanel(screen)
}

// fit draws img centred in the right-hand area.
func (g *game) fit(screen, img *ebiten.Image, w, h int) {
	aw, ah := float64(winW-panelW-32), float64(winH-32)
	s := math.Min(aw/float64(w), ah/float64(h))
	op := &ebiten.DrawImageOptions{Filter: ebiten.FilterLinear}
	op.GeoM.Scale(s, s)
	op.GeoM.Translate(panelW+16+(aw-float64(w)*s)/2, 16+(ah-float64(h)*s)/2)
	screen.DrawImage(img, op)
}

func (g *game) drawPanel(screen *ebiten.Image) {
	y := 12
	line := func(s string) {
		max := (panelW - 24) / charW
		if len(s) > max {
			s = s[:max-1] + "~"
		}
		ebitenutil.DebugPrintAt(screen, s, 12, y)
		y += lineH
	}
	titles := [...]string{"1/3 automatic checks", "2/3 playback", "3/3 WebRTC", "done"}
	line("hwmediacodec check - " + titles[g.stage])
	y += lineH / 2
	section := ""
	for _, res := range g.r.snapshot() {
		if res.Section != section {
			section = res.Section
			y += 4
			line(section)
		}
		detail := res.Detail
		if res.Status == running && detail == "" {
			detail = "running..."
		}
		line(fmt.Sprintf(" [%s] %s  %s", res.Status.mark(), res.Name, detail))
	}

	y = winH - 5*lineH
	g.rect(screen, 0, float64(y-8), panelW, float64(winH-y+8), color.RGBA{30, 33, 40, 255})
	var prompt []string
	switch g.stage {
	case stAuto:
		prompt = []string{"Checking the codecs; this takes up to a minute.", "[Esc] stop and save the report"}
	case stPlayback:
		name := codecName(g.playing.codec)
		switch g.ask {
		case watching:
			left := g.playLen - time.Since(g.playStart)
			prompt = []string{fmt.Sprintf("Playing the %s clip with sound (%.0fs).", name, math.Max(0, left.Seconds())), "[S] skip  [Esc] stop and save the report"}
		case askPicture:
			prompt = []string{fmt.Sprintf("Did the %s video play smoothly, with natural colours?", name), "[Y] yes  [N] no  [S] skip"}
		case askSound:
			prompt = []string{"Did you hear the sound of the waterfall?", "[Y] yes  [N] no  [S] skip"}
		}
	case stWebRTC:
		prompt = []string{"The browser shows the fireworks over WebRTC: answer there for each codec.", "Page: " + g.web.url, "[Enter] finish  [Esc] stop and save the report"}
	case stDone:
		pass, fail := g.r.counts()
		where := "the report could not be saved"
		if g.savedAt != "" {
			where = "Report saved to " + g.savedAt
		}
		prompt = []string{fmt.Sprintf("Finished: %d passed, %d failed. %s", pass, fail, where),
			"It is also printed in the terminal. Please paste it into a new issue.",
			"[O] open the GitHub issue page  [Esc] quit"}
	}
	if g.message != "" {
		prompt = append(prompt, g.message)
	}
	for _, p := range prompt {
		line(strings.TrimSpace(p))
	}
}

func (g *game) rect(dst *ebiten.Image, x, y, w, h float64, c color.RGBA) {
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(w, h)
	op.GeoM.Translate(x, y)
	op.ColorScale.ScaleWithColor(c)
	dst.DrawImage(g.pixel, op)
}
