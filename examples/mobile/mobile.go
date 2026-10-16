// Package mobile is the hwmediacodec video player as an Ebitengine mobile
// game. `ebitenmobile bind` turns it into an iOS xcframework that the Xcode
// project in ios/ embeds (see README.md in this directory); the Android
// project in android/ embeds the same code as an aar. The game plays the
// bundled waterfall clip in a loop through the device's hardware decoder
// and shows which backend serves it; a tap pauses and resumes. The Run
// check button starts the machine check of examples/check (check.go).
package mobile

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"math"
	"runtime"
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

// Dummy exists for gomobile, which binds a package only when it exports
// something. Besides it only the check's Host, SetHost and SetDeviceInfo
// are exported, on purpose: gobind would try to bind the game type and
// fail on the ebiten.Game method signatures.
func Dummy() {}

// game plays the bundled clip. It is also usable from a desktop main
// through ebiten.RunGame.
type game struct {
	w, h   int
	player *ebitenvideo.Player
	name   string
	vw, vh int
	info   string // the Probe summary shown at the top
	err    error  // a fatal error shown instead of the video
	pixel  *ebiten.Image
	status string
	at     time.Time
	opened bool
	check  *check // the machine check, while it runs
}

// newGame returns the game; the clip is opened on the first Update so that
// errors show on screen instead of aborting the application.
func newGame() *game {
	return &game{}
}

// textTop keeps the status lines clear of the notch or Dynamic Island at
// the top of recent iPhones (Ebitengine does not report the safe area).
const textTop = 56

func (g *game) say(s string) { g.status, g.at = s, time.Now() }

// open probes the codecs, then opens the bundled clip with the hardware
// decoder, falling back to the OS software decoder (and saying so) where
// there is none, as in the iOS Simulator.
func (g *game) open() {
	g.opened = true
	ctx := context.Background()
	caps, err := hwmediacodec.Probe(ctx)
	if err != nil {
		g.err = err
		return
	}
	var parts []string
	backend := "no hardware codec"
	for _, c := range caps {
		if c.Codec.IsAudio() {
			continue // the summary is about the video engines
		}
		parts = append(parts, fmt.Sprintf("%s %s", c.Codec, c.Direction))
		if len(parts) == 1 {
			backend = c.Backend
		}
	}
	if len(parts) > 0 {
		backend += ": " + strings.Join(parts, ", ")
	}
	g.info = fmt.Sprintf("%s/%s  %s", runtime.GOOS, runtime.GOARCH, backend)

	d, err := mp4.NewDemuxer(assets.Waterfall())
	if err != nil {
		g.err = err
		return
	}
	v := d.Video()
	if v == nil {
		g.err = errors.New("the bundled clip has no video track")
		return
	}
	g.name, g.vw, g.vh = assets.WaterfallName, v.Width, v.Height
	opts := []ebitenvideo.Option{ebitenvideo.WithLoop()}
	g.player, err = ebitenvideo.NewPlayerFromSource(v.PacketSource(), opts...)
	if errors.Is(err, hwmediacodec.ErrUnsupported) {
		g.info += "  (software decoder)"
		g.player, err = ebitenvideo.NewPlayerFromSource(v.PacketSource(), append(opts, ebitenvideo.WithSoftwareFallback())...)
	}
	if err != nil {
		g.err = err
		return
	}
	g.player.Play()
	g.say("playing")
}

// checkButton is the tap target that starts the check, above the
// progress bar and the home indicator.
func (g *game) checkButton() button {
	return button{x: float64(g.w) - 120, y: float64(g.h) - 96, w: 108, h: btnH, label: "Run check", on: true}
}

// Update implements ebiten.Game.
func (g *game) Update() error {
	if g.check != nil {
		g.check.update()
		if g.check.closed {
			g.check.close()
			g.check, g.opened = nil, false // reopen the player
		}
		return nil
	}
	if !g.opened {
		g.open()
	}
	if g.startCheck() {
		return nil
	}
	if g.player == nil {
		return nil
	}
	tapped := len(inpututil.AppendJustPressedTouchIDs(nil)) > 0 ||
		inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) ||
		inpututil.IsKeyJustPressed(ebiten.KeySpace)
	if tapped {
		if g.player.IsPlaying() {
			g.player.Pause()
			g.say("paused")
		} else {
			g.player.Play()
			g.say("playing")
		}
	}
	return g.player.Update()
}

// startCheck starts the check when its button was tapped; the player is
// closed so that the check has the decoders to itself.
func (g *game) startCheck() bool {
	b := g.checkButton()
	var taps [][2]int
	for _, id := range inpututil.AppendJustPressedTouchIDs(nil) {
		x, y := ebiten.TouchPosition(id)
		taps = append(taps, [2]int{x, y})
	}
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		x, y := ebiten.CursorPosition()
		taps = append(taps, [2]int{x, y})
	}
	for _, t := range taps {
		if b.hit(t[0], t[1]) {
			g.Close()
			g.err = nil
			g.check = newCheck()
			// This frame's Draw comes before the next Layout.
			g.check.layout(g.w, g.h)
			return true
		}
	}
	return false
}

// Draw implements ebiten.Game.
func (g *game) Draw(screen *ebiten.Image) {
	screen.Fill(color.RGBA{18, 18, 22, 255})
	if g.pixel == nil {
		g.pixel = ebiten.NewImage(1, 1)
		g.pixel.Fill(color.White)
	}
	if g.check != nil {
		g.check.draw(screen, g)
		return
	}
	defer g.drawCheckButton(screen)
	if g.err != nil {
		ebitenutil.DebugPrintAt(screen, g.info+"\n\n"+g.err.Error(), 0, textTop)
		return
	}
	if g.player == nil {
		return
	}
	img := g.player.Image()
	if img == nil {
		ebitenutil.DebugPrintAt(screen, g.info+"\ndecoding...", 0, textTop)
		return
	}
	vw, vh := g.player.Size()
	scale := math.Min(float64(g.w)/float64(vw), float64(g.h)/float64(vh))
	op := &ebiten.DrawImageOptions{Filter: ebiten.FilterLinear}
	op.GeoM.Scale(scale, scale)
	op.GeoM.Translate((float64(g.w)-float64(vw)*scale)/2, (float64(g.h)-float64(vh)*scale)/2)
	screen.DrawImage(img, op)

	if l := g.player.Length(); l > 0 {
		frac := math.Min(1, g.player.Position().Seconds()/l.Seconds())
		g.rect(screen, 0, float64(g.h)-6, float64(g.w), 6, color.RGBA{255, 255, 255, 60})
		g.rect(screen, 0, float64(g.h)-6, float64(g.w)*frac, 6, color.RGBA{255, 200, 80, 220})
	}
	status := g.status
	if time.Since(g.at) > 2*time.Second {
		status = ""
	}
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%s\n%s  %dx%d  %.2fs / %.1fs  %.0f fps  skipped %d  %s\ntap to pause and resume",
		g.info, g.name, vw, vh, g.player.Position().Seconds(), g.player.Length().Seconds(), ebiten.ActualFPS(), g.player.Skipped(), status), 0, textTop)
}

func (g *game) drawCheckButton(screen *ebiten.Image) {
	b := g.checkButton()
	g.rect(screen, b.x, b.y, b.w, b.h, color.RGBA{40, 44, 52, 200})
	ebitenutil.DebugPrintAt(screen, b.label, int(b.x+(b.w-float64(len(b.label)*charW))/2), int(b.y+(b.h-lineH)/2))
}

func (g *game) rect(dst *ebiten.Image, x, y, w, h float64, c color.RGBA) {
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(w, h)
	op.GeoM.Translate(x, y)
	op.ColorScale.ScaleWithColor(c)
	dst.DrawImage(g.pixel, op)
}

// Layout implements ebiten.Game: the game fills the screen in points.
func (g *game) Layout(outsideWidth, outsideHeight int) (int, int) {
	g.w, g.h = outsideWidth, outsideHeight
	if g.check != nil {
		g.check.layout(outsideWidth, outsideHeight)
	}
	return outsideWidth, outsideHeight
}

// Close releases the decoder (the player's; a running check keeps its own).
func (g *game) Close() {
	if g.player != nil {
		g.player.Close()
		g.player = nil
	}
}
