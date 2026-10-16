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
	"github.com/shibukawa/hwmediacodec/examples/internal/checker"
	"github.com/shibukawa/hwmediacodec/examples/internal/fireworks"
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

type game struct {
	r        *checker.Report
	out      string
	addr     string
	open     bool
	playLen  time.Duration
	autoDone atomic.Bool

	stage stage
	pixel *ebiten.Image

	skipPlayback, skipWebRTC bool

	// Playback.
	pb *checker.Playback

	// WebRTC.
	web    *web
	scene  *fireworks.Scene
	canvas *ebiten.Image

	// Done.
	saved   bool
	savedAt string
	message string
}

func newGame(r *checker.Report, out, addr string, open bool, playLen time.Duration) *game {
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
			link, _ := g.r.IssueURL()
			if err := browser.OpenURL(link); err != nil {
				g.message = "could not open the browser: " + err.Error()
			}
		}
	}
	return nil
}

// startPlayback plays the clips of every codec some backend decodes.
func (g *game) startPlayback() {
	g.stage = stPlayback
	if g.skipPlayback {
		g.startWebRTC()
		return
	}
	g.pb = checker.NewPlayback(g.r, g.playLen, false)
	if g.pb.Done() {
		g.startWebRTC()
	}
}

func (g *game) updatePlayback() {
	if !g.pb.Update() {
		if inpututil.IsKeyJustPressed(ebiten.KeyS) {
			g.pb.Skip()
		} else if yes, ok := answer(); ok {
			g.pb.Answer(yes)
		}
	}
	if g.pb.Done() {
		g.startWebRTC()
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

// startWebRTC opens a stream for every codec some backend encodes and
// opens the page.
func (g *game) startWebRTC() {
	if g.skipWebRTC {
		g.finish()
		return
	}
	var codecs []hwmediacodec.Codec
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC} {
		for _, cp := range g.r.Caps() {
			if cp.Codec == c && cp.Direction == hwmediacodec.Encode {
				codecs = append(codecs, c)
				break
			}
		}
	}
	if len(codecs) == 0 {
		res := g.r.Add("WebRTC", "Streaming")
		g.r.Set(res, checker.Unsupported, "no H.264 or HEVC encoder on this machine")
		g.finish()
		return
	}
	w, err := startWeb(g.r, g.addr, codecs)
	if err != nil {
		res := g.r.Add("WebRTC", "Page")
		g.r.Set(res, checker.Failed, "%v", err)
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
	if _, err := os.Stat(g.out); err == nil {
		g.savedAt = displayPath(g.out)
	}
}

// displayPath shows a relative path as ./name, which is short enough for
// the window; the terminal has the absolute one.
func displayPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return "." + string(filepath.Separator) + filepath.Clean(p)
}

// wrap breaks s into lines of at most n characters at spaces.
func wrap(s string, n int) []string {
	var out []string
	for len(s) > n {
		cut := strings.LastIndexByte(s[:n], ' ')
		if cut <= 0 {
			cut = n
		}
		out = append(out, s[:cut])
		s = strings.TrimLeft(s[cut:], " ")
	}
	return append(out, s)
}

func (g *game) close() {
	if g.pb != nil {
		g.pb.Close()
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
		if p := g.pb.Player(); p != nil {
			if img := p.Image(); img != nil {
				vw, vh := p.Size()
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
	for _, res := range g.r.Snapshot() {
		if res.Section != section {
			section = res.Section
			y += 4
			line(section)
		}
		detail := res.Detail
		if res.Status == checker.Running && detail == "" {
			detail = "running..."
		}
		line(fmt.Sprintf(" [%s] %s  %s", res.Status.Mark(), res.Name, detail))
	}

	var prompt []string
	switch g.stage {
	case stAuto:
		prompt = []string{"Checking the codecs; this takes up to a minute.", "[Esc] stop and save the report"}
	case stPlayback:
		name := checker.CodecName(g.pb.Codec())
		switch g.pb.Question() {
		case checker.Watching:
			prompt = []string{fmt.Sprintf("Playing the %s clip with sound (%.0fs).", name, g.pb.Left().Seconds()), "[S] skip  [Esc] stop and save the report"}
		case checker.AskPicture:
			prompt = []string{fmt.Sprintf("Did the %s video play smoothly, with natural colours?", name), "[Y] yes  [N] no  [S] skip"}
		case checker.AskSound:
			prompt = []string{"Did you hear the sound of the waterfall?", "[Y] yes  [N] no  [S] skip"}
		}
	case stWebRTC:
		prompt = []string{"The browser shows the fireworks over WebRTC: answer there for each codec.", "Page: " + g.web.url, "[Enter] finish  [Esc] stop and save the report"}
	case stDone:
		pass, fail := g.r.Counts()
		where := "the report could not be saved"
		if g.savedAt != "" {
			where = "Report saved to " + g.savedAt
		}
		_, full := g.r.IssueURL()
		issue := "[O] opens a new GitHub issue with the report filled in; please submit it there."
		if !full {
			issue = "[O] opens a new GitHub issue; please paste the report from the file into it."
		}
		prompt = []string{fmt.Sprintf("Finished: %d passed, %d failed.", pass, fail), where + " (also printed in the terminal).",
			issue, "[Esc] quit"}
	}
	if g.message != "" {
		prompt = append(prompt, g.message)
	}
	var lines []string
	for _, p := range prompt {
		lines = append(lines, wrap(strings.TrimSpace(p), (panelW-24)/charW)...)
	}
	y = winH - (len(lines)+1)*lineH
	g.rect(screen, 0, float64(y-8), panelW, float64(winH-y+8), color.RGBA{30, 33, 40, 255})
	for _, l := range lines {
		line(l)
	}
}

func (g *game) rect(dst *ebiten.Image, x, y, w, h float64, c color.RGBA) {
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(w, h)
	op.GeoM.Translate(x, y)
	op.ColorScale.ScaleWithColor(c)
	dst.DrawImage(g.pixel, op)
}
