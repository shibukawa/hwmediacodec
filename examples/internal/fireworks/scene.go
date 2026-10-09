package fireworks

import (
	"bytes"
	"fmt"
	"image/color"
	"math"
	"math/rand/v2"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"golang.org/x/image/font/gofont/goregular"
)

// Scene is the fireworks show. Create it with NewScene, call HandleInput
// and Update once per tick and Draw once per frame. It starts on a title
// screen: A starts the automatic show, any other key starts manual play
// (Start does the same from code).
type Scene struct {
	W, H    int
	tps     float64
	scale   float32 // 1 at 720p
	horizon float32
	rnd     *rand.Rand
	time    float64

	particles []particle
	launched  int

	started   bool
	auto      bool
	nextAuto  float64
	nextShow  float64 // next gopher in the automatic show
	lastKind  Kind
	lastLabel string
	lastAt    float64

	cur    *ebiten.Image // this frame's stars at full brightness
	acc    *ebiten.Image // persistence buffer: dim copies of past frames, fading
	mask   *ebiten.Image // skyline silhouette and lit windows
	glow   *ebiten.Image // radial sprite
	pixel  *ebiten.Image
	shader *ebiten.Shader

	titleFace, lineFace, smallFace text.Face
}

const glowSize = 32

// NewScene creates a show for a w x h screen updated tps times per second.
func NewScene(w, h int, tps float64) *Scene {
	s := &Scene{
		W: w, H: h, tps: tps,
		scale:   float32(h) / 720,
		horizon: float32(h) * 0.8,
		rnd:     rand.New(rand.NewPCG(uint64(w), uint64(h))),
	}
	s.nextShow = 6
	src, err := text.NewGoTextFaceSource(bytes.NewReader(goregular.TTF))
	if err != nil {
		panic(fmt.Sprintf("fireworks: font: %v", err))
	}
	s.titleFace = &text.GoTextFace{Source: src, Size: 72 * float64(s.scale)}
	s.lineFace = &text.GoTextFace{Source: src, Size: 30 * float64(s.scale)}
	s.smallFace = &text.GoTextFace{Source: src, Size: 20 * float64(s.scale)}
	s.cur = ebiten.NewImage(w, h)
	s.acc = ebiten.NewImage(w, h)
	s.pixel = ebiten.NewImage(1, 1)
	s.pixel.Fill(color.White)
	s.glow = newGlow()
	s.mask = s.newSkyline()
	sh, err := ebiten.NewShader([]byte(postShader))
	if err != nil {
		panic(fmt.Sprintf("fireworks: shader: %v", err))
	}
	s.shader = sh
	return s
}

// newGlow draws a soft radial falloff into a small sprite.
func newGlow() *ebiten.Image {
	pix := make([]byte, 4*glowSize*glowSize)
	c := float64(glowSize-1) / 2
	for y := 0; y < glowSize; y++ {
		for x := 0; x < glowSize; x++ {
			d := math.Hypot(float64(x)-c, float64(y)-c) / c
			v := math.Max(0, 1-d)
			v = v * v * (0.35 + 0.65*v) // bright core, soft halo
			b := byte(255 * v)
			i := 4 * (y*glowSize + x)
			pix[i], pix[i+1], pix[i+2], pix[i+3] = b, b, b, b
		}
	}
	img := ebiten.NewImage(glowSize, glowSize)
	img.WritePixels(pix)
	return img
}

// newSkyline builds the silhouette mask: buildings along the horizon in
// the alpha channel, a few lit windows in the colour channels.
func (s *Scene) newSkyline() *ebiten.Image {
	img := ebiten.NewImage(s.W, s.H)
	rnd := rand.New(rand.NewPCG(7, 11))
	hz := float64(s.horizon)
	// Ground strip.
	s.fill(img, 0, hz-2, float64(s.W), float64(s.H)-hz+2, color.RGBA{0, 0, 0, 255})
	for x := -10.0; x < float64(s.W); {
		w := 18 + rnd.Float64()*50*float64(s.scale)
		h := (25 + rnd.Float64()*130) * float64(s.scale)
		if rnd.Float64() < 0.15 {
			h *= 1.6 // the odd tower
		}
		s.fill(img, x, hz-h, w, h+2, color.RGBA{0, 0, 0, 255})
		// Windows: small warm rectangles, premultiplied colour, alpha stays 1.
		for wy := hz - h + 6; wy < hz-8; wy += 9 * float64(s.scale) {
			for wx := x + 4; wx < x+w-5; wx += 7 * float64(s.scale) {
				if rnd.Float64() < 0.35 {
					s.fill(img, wx, wy, 3*float64(s.scale), 4*float64(s.scale), color.RGBA{70, 55, 25, 255})
				}
			}
		}
		x += w + 2 + rnd.Float64()*14
	}
	return img
}

func (s *Scene) fill(dst *ebiten.Image, x, y, w, h float64, c color.RGBA) {
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(w, h)
	op.GeoM.Translate(x, y)
	op.ColorScale.ScaleWithColor(c)
	dst.DrawImage(s.pixel, op)
}

// Help lists the keys.
func Help() string {
	return "[1] peony [2] chrysanthemum [3] willow [4] ring [5] palm [6] crackle [G] gopher [space] random [F] finale [A] auto on/off"
}

// Start leaves the title screen: with auto the show runs by itself,
// otherwise shells are launched from the keyboard only.
func (s *Scene) Start(auto bool) {
	s.started = true
	s.auto = auto
	s.nextAuto = s.time + 0.3
	s.nextShow = s.time + float64(randRange(s.rnd, 5, 9))
}

// Started reports whether the title screen has been left.
func (s *Scene) Started() bool { return s.started }

// HandleInput launches shells from the keyboard; call it from Update. On
// the title screen A starts the automatic show and any other key starts
// manual play (and is handled as a launch key when it is one).
func (s *Scene) HandleInput() {
	if !s.started {
		pressed := inpututil.AppendJustPressedKeys(nil)
		if len(pressed) == 0 {
			return
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyA) {
			s.Start(true)
			s.lastLabel, s.lastAt = "auto show", s.time
			return
		}
		s.Start(false)
	}
	keys := map[ebiten.Key]Kind{ebiten.Key1: Peony, ebiten.Key2: Chrysanthemum, ebiten.Key3: Willow, ebiten.Key4: Ring, ebiten.Key5: Palm, ebiten.Key6: Crackle, ebiten.KeyG: Gopher}
	for k, kind := range keys {
		if inpututil.IsKeyJustPressed(k) {
			s.Launch(kind, -1)
		}
	}
	if inpututil.IsKeyJustPressed(ebiten.KeySpace) {
		s.Launch(Kind(s.rnd.IntN(int(Gopher))), -1)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF) {
		s.finale()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyA) {
		s.auto = !s.auto
		s.lastLabel, s.lastAt = fmt.Sprintf("auto show %v", map[bool]string{true: "on", false: "off"}[s.auto]), s.time
	}
}

// Auto reports whether the automatic show is running.
func (s *Scene) Auto() bool { return s.auto }

// Launch fires a shell of the given kind from x (a fraction of the width;
// negative picks a spot).
func (s *Scene) Launch(kind Kind, x float32) {
	if x < 0 {
		x = randRange(s.rnd, 0.12, 0.88)
		if kind == Gopher {
			x = randRange(s.rnd, 0.3, 0.7)
		}
	}
	// Apex height sets the launch speed: v = sqrt(2 g h).
	top := randRange(s.rnd, 0.18, 0.45)
	if kind == Gopher || kind == Willow {
		top = randRange(s.rnd, 0.16, 0.26)
	}
	apex := float32(s.H) * top
	g := 420 * s.scale
	v := float32(math.Sqrt(float64(2 * g * (s.horizon - apex))))
	s.particles = append(s.particles, particle{
		x: x * float32(s.W), y: s.horizon, vx: randRange(s.rnd, -25, 25) * s.scale, vy: -v,
		drag: 1, gravity: g, life: 10, total: 10, size: 1.4 * s.scale,
		color: rgb{1, 0.85, 0.6}, shell: true, apex: apex, kind: kind, trail: true,
	})
	s.launched++
	s.lastKind, s.lastLabel, s.lastAt = kind, kind.String(), s.time
}

// finale fires a volley.
func (s *Scene) finale() {
	for i := 0; i < 6; i++ {
		s.Launch(Kind(s.rnd.IntN(int(Gopher))), float32(i+1)/7+randRange(s.rnd, -0.04, 0.04))
	}
	s.lastLabel = "finale"
}

// Update advances the show by one tick.
func (s *Scene) Update() {
	dt := float32(1 / s.tps)
	s.time += 1 / s.tps
	if s.started && s.auto {
		if s.time >= s.nextAuto {
			s.Launch(Kind(s.rnd.IntN(int(Gopher))), -1)
			s.nextAuto = s.time + float64(randRange(s.rnd, 0.5, 1.6))
		}
		if s.time >= s.nextShow {
			s.Launch(Gopher, -1)
			s.nextShow = s.time + float64(randRange(s.rnd, 10, 16))
		}
	}
	var spawned []particle
	live := s.particles[:0]
	for i := range s.particles {
		p := &s.particles[i]
		if p.trail && s.rnd.Float32() < 0.9 {
			spawned = append(spawned, particle{
				x: p.x, y: p.y, vx: p.vx*0.1 + randRange(s.rnd, -15, 15)*s.scale, vy: p.vy*0.1 + randRange(s.rnd, -15, 15)*s.scale,
				drag: 0.3, gravity: 30 * s.scale, life: randRange(s.rnd, 0.25, 0.5), total: 0.5,
				size: 0.8 * s.scale, color: rgb{1, 0.75, 0.45}, kind: p.kind,
			})
		}
		p.step(dt)
		if p.shell && (p.vy >= 0 || p.y <= p.apex) {
			spawned = append(spawned, burst(s.rnd, p.kind, p.x, p.y, p.vx, p.vy, s.scale, s.tps)...)
			continue
		}
		if p.splitAt > 0 && p.life <= p.splitAt {
			spawned = append(spawned, sparks(s.rnd, *p, s.scale)...)
			continue
		}
		if p.life <= 0 || p.y > float32(s.H)+20 {
			continue
		}
		live = append(live, *p)
	}
	s.particles = append(live, spawned...)
}

// Particles is the number of live stars, for overlays.
func (s *Scene) Particles() int { return len(s.particles) }

// Status is a short line about the last launch.
func (s *Scene) Status() string {
	if s.lastLabel == "" || s.time-s.lastAt > 3 {
		return ""
	}
	return strings.ToUpper(s.lastLabel[:1]) + s.lastLabel[1:]
}

// subtractBlend takes a constant off the colour channels (dst - src) and
// leaves alpha alone: it finishes off the trails that a multiplicative
// fade would leave as a faint permanent ghost in 8-bit buffers.
var subtractBlend = ebiten.Blend{
	BlendFactorSourceRGB:        ebiten.BlendFactorOne,
	BlendFactorDestinationRGB:   ebiten.BlendFactorOne,
	BlendOperationRGB:           ebiten.BlendOperationReverseSubtract,
	BlendFactorSourceAlpha:      ebiten.BlendFactorZero,
	BlendFactorDestinationAlpha: ebiten.BlendFactorOne,
	BlendOperationAlpha:         ebiten.BlendOperationAdd,
}

// Draw renders the frame into dst.
func (s *Scene) Draw(dst *ebiten.Image) {
	// This frame's stars, additive, full brightness.
	s.cur.Clear()
	op := &ebiten.DrawImageOptions{Blend: ebiten.BlendLighter, Filter: ebiten.FilterLinear}
	for i := range s.particles {
		p := &s.particles[i]
		b := p.brightness(s.time)
		if b <= 0.01 {
			continue
		}
		size := float64(p.size)
		if p.shell {
			size = 1.4 * float64(s.scale)
		}
		op.GeoM.Reset()
		op.GeoM.Translate(-glowSize/2, -glowSize/2)
		op.GeoM.Scale(size*0.3, size*0.3)
		op.GeoM.Translate(float64(p.x), float64(p.y))
		op.ColorScale.Reset()
		op.ColorScale.Scale(p.color.r*b, p.color.g*b, p.color.b*b, b)
		s.cur.DrawImage(s.glow, op)
	}

	// Persistence: fade what is there (multiply, then take a little off so
	// that it really reaches black), then add a dim copy of this frame.
	fade := &ebiten.DrawImageOptions{}
	fade.GeoM.Scale(float64(s.W), float64(s.H))
	fade.ColorScale.Scale(0, 0, 0, 0.09)
	s.acc.DrawImage(s.pixel, fade)
	sub := &ebiten.DrawImageOptions{Blend: subtractBlend}
	sub.GeoM.Scale(float64(s.W), float64(s.H))
	sub.ColorScale.Scale(2.0/255, 2.0/255, 2.0/255, 0)
	s.acc.DrawImage(s.pixel, sub)
	trail := &ebiten.DrawImageOptions{Blend: ebiten.BlendLighter}
	trail.ColorScale.Scale(0.14, 0.14, 0.14, 0.14)
	s.acc.DrawImage(s.cur, trail)

	post := &ebiten.DrawRectShaderOptions{}
	post.Images[0] = s.cur
	post.Images[1] = s.acc
	post.Images[2] = s.mask
	post.Uniforms = map[string]any{"Time": float32(s.time), "Horizon": s.horizon}
	dst.DrawRectShader(s.W, s.H, s.shader, post)

	if !s.started {
		s.drawTitle(dst)
	}
}

// drawTitle shows the attract screen: the name, a blinking prompt and the
// keys.
func (s *Scene) drawTitle(dst *ebiten.Image) {
	cx, cy := float64(s.W)/2, float64(s.H)*0.36
	draw := func(str string, face text.Face, y float64, c color.RGBA, alpha float32) {
		op := &text.DrawOptions{}
		op.PrimaryAlign = text.AlignCenter
		op.GeoM.Translate(cx, y)
		op.ColorScale.ScaleWithColor(c)
		op.ColorScale.ScaleAlpha(alpha)
		text.Draw(dst, str, face, op)
	}
	// Shadow then text, so it reads over the stars.
	for _, pass := range []struct {
		dx, dy float64
		c      color.RGBA
	}{{3, 3, color.RGBA{0, 0, 0, 200}}, {0, 0, color.RGBA{255, 240, 210, 255}}} {
		op := &text.DrawOptions{}
		op.PrimaryAlign = text.AlignCenter
		op.GeoM.Translate(cx+pass.dx, cy+pass.dy)
		op.ColorScale.ScaleWithColor(pass.c)
		text.Draw(dst, "HANABI", s.titleFace, op)
	}
	blink := float32(0.55 + 0.45*math.Sin(s.time*4))
	draw("PRESS  A  FOR THE AUTO SHOW", s.lineFace, cy+110*float64(s.scale), color.RGBA{255, 220, 120, 255}, blink)
	draw("ANY OTHER KEY: LAUNCH YOUR OWN", s.lineFace, cy+150*float64(s.scale), color.RGBA{200, 220, 255, 255}, 1)
	draw("1 PEONY   2 CHRYSANTHEMUM   3 WILLOW   4 RING   5 PALM   6 CRACKLE", s.smallFace, cy+200*float64(s.scale), color.RGBA{180, 190, 210, 255}, 1)
	draw("G GOPHER   SPACE RANDOM   F VOLLEY   A AUTO ON/OFF", s.smallFace, cy+228*float64(s.scale), color.RGBA{180, 190, 210, 255}, 1)
}
