// Package demo draws the animation the recording and streaming samples
// capture: bouncing balls over drifting colour bars with a running clock,
// enough motion to make an encoder work and easy to recognise in a player.
// Everything is drawn with DrawImage so that the scene itself costs
// nothing and the measurements of the capture path are not polluted
// (anti-aliased vector fills are far more expensive per frame).
package demo

import (
	"fmt"
	"image/color"
	"math"
	"math/rand/v2"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

type ball struct {
	x, y, vx, vy, r float32
	c               color.RGBA
}

// Scene is a self-contained animation of the given size.
type Scene struct {
	W, H   int
	balls  []ball
	tick   int
	tps    float64
	circle *ebiten.Image // white disc, drawn scaled and tinted per ball
	pixel  *ebiten.Image // 1x1 white, for rectangles
}

const spriteSize = 128

// NewScene creates a scene for a screen of w x h pixels updated tps times
// per second.
func NewScene(w, h int, tps float64) *Scene {
	s := &Scene{W: w, H: h, tps: tps}
	rnd := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 24; i++ {
		s.balls = append(s.balls, ball{
			x: rnd.Float32() * float32(w), y: rnd.Float32() * float32(h),
			vx: (rnd.Float32() - 0.5) * 12, vy: (rnd.Float32() - 0.5) * 12,
			r: 12 + rnd.Float32()*40,
			c: color.RGBA{R: uint8(80 + rnd.IntN(176)), G: uint8(80 + rnd.IntN(176)), B: uint8(80 + rnd.IntN(176)), A: 255},
		})
	}
	s.circle = ebiten.NewImage(spriteSize, spriteSize)
	vector.FillCircle(s.circle, spriteSize/2, spriteSize/2, spriteSize/2-2, color.White, true)
	vector.StrokeCircle(s.circle, spriteSize/2, spriteSize/2, spriteSize/2-2, 3, color.RGBA{30, 30, 30, 255}, true)
	s.pixel = ebiten.NewImage(1, 1)
	s.pixel.Fill(color.White)
	return s
}

// Update advances the animation by one tick.
func (s *Scene) Update() {
	s.tick++
	for i := range s.balls {
		b := &s.balls[i]
		b.x += b.vx
		b.y += b.vy
		if b.x < b.r || b.x > float32(s.W)-b.r {
			b.vx = -b.vx
		}
		if b.y < b.r || b.y > float32(s.H)-b.r {
			b.vy = -b.vy
		}
	}
}

func (s *Scene) rect(dst *ebiten.Image, x, y, w, h float64, c color.RGBA) {
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(w, h)
	op.GeoM.Translate(x, y)
	op.ColorScale.ScaleWithColor(c)
	dst.DrawImage(s.pixel, op)
}

// Draw renders the current frame into dst.
func (s *Scene) Draw(dst *ebiten.Image) {
	// Colour bars that drift slowly, so even the background changes.
	bars := []color.RGBA{{235, 235, 235, 255}, {235, 235, 0, 255}, {0, 235, 235, 255}, {0, 235, 0, 255}, {235, 0, 235, 255}, {235, 0, 0, 255}, {0, 0, 235, 255}, {20, 20, 20, 255}}
	bw := float64(s.W) / float64(len(bars))
	shift := math.Mod(float64(s.tick)*0.5, bw)
	for i := -1; i <= len(bars); i++ {
		c := bars[((i%len(bars))+len(bars))%len(bars)]
		s.rect(dst, float64(i)*bw+shift, 0, bw+1, float64(s.H), c)
	}
	for _, b := range s.balls {
		op := &ebiten.DrawImageOptions{}
		scale := float64(b.r) * 2 / spriteSize
		op.GeoM.Scale(scale, scale)
		op.GeoM.Translate(float64(b.x-b.r), float64(b.y-b.r))
		op.ColorScale.ScaleWithColor(b.c)
		op.Filter = ebiten.FilterLinear
		dst.DrawImage(s.circle, op)
	}
	// A frame counter and clock: easy to read in a thumbnail or a browser
	// tab to judge latency.
	secs := float64(s.tick) / s.tps
	msg := fmt.Sprintf("frame %d\n%02d:%02d.%03d", s.tick, int(secs)/60, int(secs)%60, int(secs*1000)%1000)
	s.rect(dst, 8, 8, 120, 40, color.RGBA{0, 0, 0, 180})
	ebitenutil.DebugPrintAt(dst, msg, 12, 10)
}
