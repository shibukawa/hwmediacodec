// Package fireworks is the scene the recording and streaming samples
// capture: a fireworks show over water, drawn with additive particles into
// a persistence buffer and finished by a Kage shader (sky, stars, bloom,
// skyline, rippling reflection). Several shell types can be launched from
// the keyboard, among them a gopher-shaped one.
package fireworks

import (
	"math"
	"math/rand/v2"
)

// Kind is a type of shell.
type Kind int

const (
	Peony Kind = iota
	Chrysanthemum
	Willow
	Ring
	Palm
	Crackle
	Gopher
	kindCount
)

var kindNames = [...]string{"peony", "chrysanthemum", "willow", "ring", "palm", "crackle", "gopher"}

// String names the kind for overlays.
func (k Kind) String() string { return kindNames[k] }

// ParseKind looks a kind up by name.
func ParseKind(name string) (Kind, bool) {
	for i, n := range kindNames {
		if n == name {
			return Kind(i), true
		}
	}
	return 0, false
}

type rgb struct{ r, g, b float32 }

func (c rgb) scale(s float32) rgb { return rgb{c.r * s, c.g * s, c.b * s} }

var palette = []rgb{
	{1.0, 0.35, 0.35}, // red
	{1.0, 0.75, 0.25}, // gold
	{0.45, 1.0, 0.55}, // green
	{0.4, 0.7, 1.0},   // blue
	{0.95, 0.45, 1.0}, // violet
	{0.5, 1.0, 1.0},   // cyan
	{1.0, 1.0, 0.9},   // white
}

// particle is a glowing point with simple physics. Positions are in
// pixels, velocities in pixels per second.
type particle struct {
	x, y    float32
	vx, vy  float32
	drag    float32 // velocity factor per second (0.1 keeps 10 % after one second)
	gravity float32
	life    float32 // seconds left
	total   float32 // initial life, for fading
	size    float32
	color   rgb
	flicker bool
	trail   bool    // emits sparks while flying (shells, palm arms)
	splitAt float32 // crackle: life at which the particle bursts into sparks (0 = never)
	kind    Kind    // of the shell that produced it (shells carry what they will become)
	shell   bool    // a rising shell, bursts at the apex
	apex    float32 // y at which a shell bursts
}

// step advances a particle by dt seconds.
func (p *particle) step(dt float32) {
	p.vy += p.gravity * dt
	k := float32(math.Pow(float64(p.drag), float64(dt)))
	p.vx *= k
	p.vy *= k
	p.x += p.vx * dt
	p.y += p.vy * dt
	p.life -= dt
}

// brightness is the light a particle gives off now, 0..1.
func (p *particle) brightness(t float64) float32 {
	f := p.life / p.total
	b := f * f * (3 - 2*f) // smoothstep fade
	if f > 0.85 {
		b = 1 // full for the first part of its life
	}
	if p.flicker {
		b *= 0.55 + 0.45*float32(math.Sin(t*47+float64(p.x)*0.37+float64(p.y)*0.11))
	}
	return b
}

func randRange(rnd *rand.Rand, lo, hi float32) float32 {
	return lo + (hi-lo)*rnd.Float32()
}

// sphereDir returns a random direction on the unit sphere projected to the
// screen plane (x, y) with its depth z, so that bursts look round.
func sphereDir(rnd *rand.Rand) (x, y, z float32) {
	u := rnd.Float64()*2 - 1
	a := rnd.Float64() * 2 * math.Pi
	r := math.Sqrt(1 - u*u)
	return float32(r * math.Cos(a)), float32(r * math.Sin(a)), float32(u)
}

// burst creates the stars of a shell exploding at (x, y) with the given
// residual velocity. scale adapts sizes to the screen height (1 at 720p).
func burst(rnd *rand.Rand, kind Kind, x, y, vx, vy, scale float32, tps float64) []particle {
	var out []particle
	add := func(p particle) {
		p.x, p.y = x, y
		p.vx += vx * 0.3
		p.vy += vy * 0.3
		p.total = p.life
		p.kind = kind
		out = append(out, p)
	}
	color := palette[rnd.IntN(len(palette))]
	switch kind {
	case Peony:
		speed := randRange(rnd, 230, 290) * scale
		for i := 0; i < 320; i++ {
			dx, dy, dz := sphereDir(rnd)
			s := speed * randRange(rnd, 0.9, 1.0)
			add(particle{vx: dx * s, vy: dy * s, drag: 0.08, gravity: 70 * scale, life: randRange(rnd, 1.5, 2.2),
				size: (1.6 + 0.6*dz) * scale, color: color})
		}
	case Chrysanthemum:
		gold := rgb{1.0, 0.8, 0.35}
		for i := 0; i < 420; i++ {
			dx, dy, dz := sphereDir(rnd)
			s := randRange(rnd, 250, 320) * scale
			add(particle{vx: dx * s, vy: dy * s, drag: 0.12, gravity: 80 * scale, life: randRange(rnd, 1.8, 2.6),
				size: (1.4 + 0.5*dz) * scale, color: gold, flicker: true, trail: true})
		}
	case Willow:
		amber := rgb{1.0, 0.72, 0.3}
		for i := 0; i < 260; i++ {
			dx, dy, _ := sphereDir(rnd)
			s := randRange(rnd, 160, 210) * scale
			add(particle{vx: dx * s, vy: dy*s - 40*scale, drag: 0.35, gravity: 110 * scale, life: randRange(rnd, 3.2, 4.2),
				size: 1.3 * scale, color: amber, trail: true})
		}
	case Ring:
		// A circle in a randomly tilted plane: an ellipse on screen.
		tilt := rnd.Float64() * math.Pi
		squash := 0.35 + 0.65*rnd.Float64()
		speed := randRange(rnd, 260, 300) * scale
		c2 := palette[rnd.IntN(len(palette))]
		for i := 0; i < 220; i++ {
			a := float64(i) / 220 * 2 * math.Pi
			px, py := math.Cos(a), math.Sin(a)*squash
			rx := px*math.Cos(tilt) - py*math.Sin(tilt)
			ry := px*math.Sin(tilt) + py*math.Cos(tilt)
			c := color
			if i%2 == 1 {
				c = c2
			}
			add(particle{vx: float32(rx) * speed, vy: float32(ry) * speed, drag: 0.1, gravity: 60 * scale, life: randRange(rnd, 1.6, 2.0),
				size: 1.8 * scale, color: c})
		}
	case Palm:
		orange := rgb{1.0, 0.55, 0.2}
		arms := 9 + rnd.IntN(4)
		for i := 0; i < arms; i++ {
			a := float64(i)/float64(arms)*2*math.Pi + rnd.Float64()*0.3
			for k := 0; k < 22; k++ {
				s := randRange(rnd, 170, 330) * scale
				jx, jy := randRange(rnd, -12, 12)*scale, randRange(rnd, -12, 12)*scale
				add(particle{vx: float32(math.Cos(a))*s + jx, vy: float32(math.Sin(a))*s + jy, drag: 0.25, gravity: 120 * scale,
					life: randRange(rnd, 1.8, 2.4), size: 2.0 * scale, color: orange, trail: true})
			}
		}
	case Crackle:
		for i := 0; i < 160; i++ {
			dx, dy, _ := sphereDir(rnd)
			s := randRange(rnd, 200, 260) * scale
			add(particle{vx: dx * s, vy: dy * s, drag: 0.1, gravity: 70 * scale, life: randRange(rnd, 1.4, 1.9),
				size: 1.8 * scale, color: rgb{1, 1, 0.85}, splitAt: randRange(rnd, 0.6, 1.1)})
		}
	case Gopher:
		out = append(out, gopherBurst(rnd, x, y, vx, vy, scale, tps)...)
	}
	return out
}

// sparks are the small secondary stars of a crackle.
func sparks(rnd *rand.Rand, p particle, scale float32) []particle {
	out := make([]particle, 0, 6)
	for i := 0; i < 6; i++ {
		dx, dy, _ := sphereDir(rnd)
		s := randRange(rnd, 40, 90) * scale
		out = append(out, particle{x: p.x, y: p.y, vx: p.vx*0.5 + dx*s, vy: p.vy*0.5 + dy*s, drag: 0.2, gravity: 90 * scale,
			life: randRange(rnd, 0.35, 0.6), total: 0.6, size: 1.2 * scale, color: rgb{1, 0.95, 0.8}, flicker: true})
	}
	return out
}
