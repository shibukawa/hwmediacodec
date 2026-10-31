package mobile

import (
	"context"
	"fmt"
	"image/color"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/cmd/hwmediacodec/checker"
	"github.com/shibukawa/hwmediacodec/cmd/hwmediacodec/checker/playback"
)

// Host is what the native application does for the check: the share
// sheet and the browser, which Ebitengine has no API for. The Swift and
// Java code in ios/ and android/ implement it and pass it to SetHost.
// Both methods are called from the game's goroutine.
type Host interface {
	// Share offers text (the Markdown report) to the platform's share
	// sheet, which can also copy it; subject is the issue title.
	Share(subject, text string)
	// OpenURL opens url in the browser.
	OpenURL(url string)
}

var (
	hostMu sync.Mutex
	host   Host
	device string
)

// SetHost installs the native side of the check's Share and Open issue
// buttons; without it they say that the app cannot share.
func SetHost(h Host) {
	hostMu.Lock()
	defer hostMu.Unlock()
	host = h
}

// SetDeviceInfo is the model and OS version for the report, such as
// "iPhone16,1, iOS 26.0"; the Go side cannot read the model.
func SetDeviceInfo(info string) {
	hostMu.Lock()
	defer hostMu.Unlock()
	device = info
}

func currentHost() (Host, string) {
	hostMu.Lock()
	defer hostMu.Unlock()
	return host, device
}

// playLen is how long each clip plays before the question, as on the
// desktop.
const playLen = 6 * time.Second

type checkStage int

const (
	ckAuto checkStage = iota
	ckPlayback
	ckDone
)

// check is the machine check of hwmediacodec check on a touch screen: the
// automatic checks, the playback stage with Yes / No buttons, then the
// report with buttons that share it and open a new GitHub issue. The
// WebRTC stage is left out: it needs a desktop browser.
type check struct {
	r        *checker.Report
	stage    checkStage
	autoDone atomic.Bool
	pb       *playback.Stage
	message  string
	closed   bool // Back was tapped

	// The interface is drawn in ui, w x h pixels of scale points each, so
	// that the debug font stays readable on tablets; the video is drawn on
	// the screen itself at its full resolution.
	w, h  int
	scale int
	ui    *ebiten.Image

	scroll   float64 // of the result list
	follow   bool    // keep the list scrolled to the end while the checks add to it
	dragID   ebiten.TouchID
	dragging bool
	dragY    int
	listH    float64 // the list's height, for clamping the scroll

	buttons []button // of the last Draw, for the next Update
}

type button struct {
	x, y, w, h float64
	label      string
	on         bool
	tap        func()
}

func (b button) hit(x, y int) bool {
	fx, fy := float64(x), float64(y)
	return b.on && fx >= b.x && fx < b.x+b.w && fy >= b.y && fy < b.y+b.h
}

func newCheck() *check {
	_, dev := currentHost()
	r := checker.NewReport()
	r.Generator = "the check mode of the mobile sample (examples/mobile)"
	r.PasteHint = "_The report was too long for the link: please paste the report that the app shared here._"
	r.Device = dev
	if r.Device == "" {
		r.Device = "_(please fill in the model and OS version)_"
	}
	c := &check{r: r, follow: true, scale: 1}
	go func() {
		k := &checker.Checker{R: r}
		k.Run(context.Background())
		c.autoDone.Store(true)
	}()
	return c
}

// layout sizes the interface for the screen in points: on tablets two
// points make a pixel (by the shorter side, so that a landscape tablet
// keeps room for the list).
func (c *check) layout(outW, outH int) {
	c.scale = max(1, min(outW, outH)/360)
	c.w, c.h = outW/c.scale, outH/c.scale
}

// pos turns a position on the screen into interface pixels.
func (c *check) pos(x, y int) (int, int) { return x / c.scale, y / c.scale }

// top and bottom keep text clear of the notch and the home indicator.
func (c *check) top() float64    { return float64(56 / c.scale) }
func (c *check) bottom() float64 { return float64(c.h) - float64(34/c.scale) }

func (c *check) update() {
	switch c.stage {
	case ckAuto:
		if c.autoDone.Load() {
			c.startPlayback()
		}
	case ckPlayback:
		if c.pb.Update() {
			c.buttons = nil // the clip changed under the buttons
		}
		if c.pb.Done() {
			c.finish()
		}
	}
	c.input()
}

func (c *check) startPlayback() {
	c.stage = ckPlayback
	// The iOS Simulator and Android emulators decode nothing in hardware;
	// there the clips play through the OS software decoder, so that the
	// stage can be tried at all, and the report says so.
	software := true
	for _, cp := range c.r.Caps() {
		if !cp.Codec.IsAudio() && cp.Direction == hwmediacodec.Decode && cp.Hardware {
			software = false
		}
	}
	c.pb = playback.New(c.r, playLen, software)
	if c.pb.Done() {
		c.finish()
	}
}

func (c *check) finish() {
	if c.pb != nil {
		c.pb.Close()
	}
	c.stage = ckDone
	c.follow = false
	c.scroll = 0
}

func (c *check) close() {
	if c.pb != nil {
		c.pb.Close()
	}
}

// input handles taps on the buttons and drags on the result list.
func (c *check) input() {
	var taps [][2]int
	for _, id := range inpututil.AppendJustPressedTouchIDs(nil) {
		x, y := c.pos(ebiten.TouchPosition(id))
		taps = append(taps, [2]int{x, y})
		if !c.dragging {
			c.dragging, c.dragID, c.dragY = true, id, y
		}
	}
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		x, y := c.pos(ebiten.CursorPosition())
		taps = append(taps, [2]int{x, y})
	}
	for _, t := range taps {
		for _, b := range c.buttons {
			if b.hit(t[0], t[1]) {
				b.tap()
				return
			}
		}
	}
	if c.dragging {
		if inpututil.IsTouchJustReleased(c.dragID) {
			c.dragging = false
		} else {
			_, y := c.pos(ebiten.TouchPosition(c.dragID))
			if y != c.dragY {
				c.scroll -= float64(y - c.dragY)
				c.dragY = y
				c.follow = false
			}
		}
	}
	if _, dy := ebiten.Wheel(); dy != 0 {
		c.scroll -= dy * 16
		c.follow = false
	}
}

func (c *check) share() {
	h, _ := currentHost()
	if h == nil {
		c.message = "This build cannot share; the report is in the log."
		fmt.Println(c.r.Markdown())
		return
	}
	h.Share(c.r.Title(), c.r.Markdown())
	c.message = ""
}

func (c *check) openIssue() {
	h, _ := currentHost()
	if h == nil {
		c.message = "This build cannot open the browser."
		return
	}
	link, full := c.r.IssueURL()
	h.OpenURL(link)
	c.message = ""
	if !full {
		c.message = "The report is too long for the link: share it and paste it into the issue."
	}
}

const (
	lineH = 16
	charW = 6 // of the debug font
	btnH  = 44
	pad   = 8
)

func (c *check) draw(screen *ebiten.Image, g *game) {
	if c.w <= 0 || c.h <= 0 {
		return // not laid out yet
	}
	if c.ui == nil || c.ui.Bounds().Dx() != c.w || c.ui.Bounds().Dy() != c.h {
		if c.ui != nil {
			c.ui.Deallocate()
		}
		c.ui = ebiten.NewImage(c.w, c.h)
	}
	video := c.drawUI(c.ui, g)
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(float64(c.scale), float64(c.scale))
	screen.DrawImage(c.ui, op)
	if video != nil {
		video.GeoM.Scale(float64(c.scale), float64(c.scale))
		screen.DrawImage(c.pb.Player().Image(), video)
	}
}

// drawUI draws everything but the video into screen and returns where
// the video goes, in interface pixels, during the playback stage.
func (c *check) drawUI(screen *ebiten.Image, g *game) *ebiten.DrawImageOptions {
	screen.Fill(color.RGBA{20, 22, 27, 255})
	cols := (c.w - 2*pad) / charW

	// The bottom bar: the prompt over a row of buttons.
	var prompt []string
	var row []button
	stop := button{label: "Stop", on: true, tap: c.finish}
	switch c.stage {
	case ckAuto:
		prompt = []string{"Checking the codecs; this takes up to a minute. Stop ends the run and keeps what was checked."}
		row = []button{stop}
	case ckPlayback:
		name := checker.CodecName(c.pb.Codec())
		asking := c.pb.Question() != playback.Watching
		switch c.pb.Question() {
		case playback.Watching:
			prompt = []string{fmt.Sprintf("Playing the %s clip with sound (%.0fs).", name, c.pb.Left().Seconds())}
		case playback.AskPicture:
			prompt = []string{fmt.Sprintf("Did the %s video play smoothly, with natural colours?", name)}
		case playback.AskSound:
			prompt = []string{"Did you hear the sound of the waterfall?"}
		}
		row = []button{
			{label: "Yes", on: asking, tap: func() { c.pb.Answer(true) }},
			{label: "No", on: asking, tap: func() { c.pb.Answer(false) }},
			{label: "Skip", on: true, tap: c.pb.Skip},
			stop,
		}
	case ckDone:
		pass, fail := c.r.Counts()
		prompt = []string{fmt.Sprintf("Finished: %d passed, %d failed.", pass, fail),
			"Share sends the report (Markdown) to another app or copies it; Open issue starts a GitHub issue with it filled in. Nothing is sent until you submit it there."}
		if !c.autoDone.Load() {
			prompt = append(prompt, "The automatic checks are still running.")
		}
		row = []button{
			{label: "Share", on: true, tap: c.share},
			{label: "Open issue", on: true, tap: c.openIssue},
			{label: "Back", on: c.autoDone.Load(), tap: func() { c.closed = true }},
		}
	}
	if c.message != "" {
		prompt = append(prompt, c.message)
	}
	var lines []string
	for _, p := range prompt {
		lines = append(lines, wrap(p, cols)...)
	}
	barY := c.bottom() - btnH - pad - float64(len(lines)*lineH) - pad
	g.rect(screen, 0, barY, float64(c.w), float64(c.h)-barY, color.RGBA{30, 33, 40, 255})
	for i, l := range lines {
		ebitenutil.DebugPrintAt(screen, l, pad, int(barY)+pad+i*lineH)
	}
	bw := (float64(c.w) - pad*float64(len(row)+1)) / float64(len(row))
	for i := range row {
		b := &row[i]
		b.x, b.y, b.w, b.h = pad+float64(i)*(bw+pad), c.bottom()-btnH, bw, btnH
		fill := color.RGBA{70, 110, 170, 255}
		if !b.on {
			fill = color.RGBA{48, 52, 60, 255}
		}
		g.rect(screen, b.x, b.y, b.w, b.h, fill)
		ebitenutil.DebugPrintAt(screen, b.label, int(b.x+(b.w-float64(len(b.label)*charW))/2), int(b.y+(b.h-lineH)/2))
	}
	c.buttons = row

	top := c.top()
	titles := [...]string{"1/2 automatic checks", "2/2 playback", "report"}
	ebitenutil.DebugPrintAt(screen, "hwmediacodec check - "+titles[c.stage], pad, int(top))
	top += lineH + pad

	if c.stage == ckPlayback {
		if p := c.pb.Player(); p != nil {
			if img := p.Image(); img != nil {
				vw, vh := p.Size()
				aw, ah := float64(c.w)-2*pad, barY-top-pad
				s := math.Min(aw/float64(vw), ah/float64(vh))
				op := &ebiten.DrawImageOptions{Filter: ebiten.FilterLinear}
				op.GeoM.Scale(s, s)
				op.GeoM.Translate(pad+(aw-float64(vw)*s)/2, top+(ah-float64(vh)*s)/2)
				return op
			}
		}
		return nil
	}

	// The result list, scrolled within [top, barY).
	var list []string
	section := ""
	for _, res := range c.r.Snapshot() {
		if res.Section != section {
			section = res.Section
			if len(list) > 0 {
				list = append(list, "")
			}
			list = append(list, section)
		}
		list = append(list, wrap(fmt.Sprintf("[%s] %s", res.Status.Mark(), res.Name), cols)...)
		detail := res.Detail
		if res.Status == checker.Running && detail == "" {
			detail = "running..."
		}
		if detail != "" {
			for _, l := range wrap(detail, cols-5) {
				list = append(list, "     "+l)
			}
		}
	}
	c.listH = barY - top
	maxScroll := math.Max(0, float64(len(list)*lineH)-c.listH)
	if c.follow {
		c.scroll = maxScroll
	}
	c.scroll = math.Max(0, math.Min(c.scroll, maxScroll))
	for i, l := range list {
		// Only whole lines, so that none runs into the title or the bar.
		if y := top + float64(i*lineH) - c.scroll; y >= top && y+lineH <= barY {
			ebitenutil.DebugPrintAt(screen, l, pad, int(y))
		}
	}
	return nil
}

// wrap breaks s into lines of at most n characters at spaces.
func wrap(s string, n int) []string {
	n = max(n, 8)
	s = strings.TrimSpace(s)
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
