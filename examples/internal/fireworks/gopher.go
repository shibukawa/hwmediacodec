package fireworks

import (
	"math"
	"math/rand/v2"
)

// The gopher shell: its stars are given velocities that bring them onto a
// gopher silhouette after formTime seconds, drawn front on in a 100x100
// box. The shape is built from a few ellipses; the eyes and snout cut
// holes into the body so that the additive stars keep their colours, and
// the pupils are holes in the eyes. Nothing is pixel-perfect on purpose:
// per-star jitter, a random tilt and gravity bend the figure a little, as
// a real set-piece shell would.

type ellipse struct {
	cx, cy, rx, ry float64
}

func (e ellipse) contains(x, y float64) bool {
	dx, dy := (x-e.cx)/e.rx, (y-e.cy)/e.ry
	return dx*dx+dy*dy <= 1
}

func (e ellipse) area() float64 { return math.Pi * e.rx * e.ry }

var (
	gopherBlue  = rgb{0.42, 0.84, 0.92}
	gopherTan   = rgb{0.98, 0.84, 0.64}
	gopherWhite = rgb{1, 1, 1}
	gopherNose  = rgb{0.55, 0.3, 0.2}

	body   = ellipse{50, 58, 32, 38}
	earL   = ellipse{22, 22, 9, 9}
	earR   = ellipse{78, 22, 9, 9}
	eyeL   = ellipse{36, 40, 12, 12}
	eyeR   = ellipse{64, 40, 12, 12}
	pupilL = ellipse{40, 42, 5.5, 5.5}
	pupilR = ellipse{68, 42, 5.5, 5.5}
	snout  = ellipse{50, 57, 10, 7}
	nose   = ellipse{50, 53, 3.5, 2.8}
	toothL = ellipse{47.5, 64, 2.2, 3.2}
	toothR = ellipse{52.5, 64, 2.2, 3.2}
	armL   = ellipse{18, 70, 7, 5}
	armR   = ellipse{82, 70, 7, 5}
	footL  = ellipse{38, 94, 9, 5}
	footR  = ellipse{62, 94, 9, 5}
)

// gopherPoint is a star's target offset (in the 100x100 box) and colour.
type gopherPoint struct {
	x, y  float64
	color rgb
}

// gopherPoints samples the silhouette: about density points per unit of
// area for the body, the small bright features denser and the body a
// little sparser, so that eyes, snout and teeth read through the glow.
func gopherPoints(rnd *rand.Rand, density float64) []gopherPoint {
	var pts []gopherPoint
	fill := func(e ellipse, c rgb, mult float64, reject ...ellipse) {
		n := int(e.area() * density * mult)
		for i := 0; i < n; {
			// Uniform in the ellipse: polar with sqrt radius.
			r := math.Sqrt(rnd.Float64())
			a := rnd.Float64() * 2 * math.Pi
			x, y := e.cx+r*e.rx*math.Cos(a), e.cy+r*e.ry*math.Sin(a)
			skip := false
			for _, h := range reject {
				if h.contains(x, y) {
					skip = true
					break
				}
			}
			i++
			if !skip {
				pts = append(pts, gopherPoint{x, y, c})
			}
		}
	}
	outline := func(e ellipse, c rgb, step float64) {
		perimeter := math.Pi * (3*(e.rx+e.ry) - math.Sqrt((3*e.rx+e.ry)*(e.rx+3*e.ry)))
		n := int(perimeter / step)
		for i := 0; i < n; i++ {
			a := float64(i) / float64(n) * 2 * math.Pi
			pts = append(pts, gopherPoint{e.cx + e.rx*math.Cos(a), e.cy + e.ry*math.Sin(a), c})
		}
	}
	dim := gopherBlue.scale(0.7)
	fill(body, dim, 0.55, eyeL, eyeR, snout, toothL, toothR)
	outline(body, gopherBlue, 1.5)
	fill(earL, dim, 0.8)
	fill(earR, dim, 0.8)
	outline(earL, gopherBlue, 1.8)
	outline(earR, gopherBlue, 1.8)
	fill(eyeL, gopherWhite, 1.3, pupilL)
	fill(eyeR, gopherWhite, 1.3, pupilR)
	fill(snout, gopherTan, 1.1, nose)
	fill(nose, gopherNose, 1.5)
	fill(toothL, gopherWhite, 1.6)
	fill(toothR, gopherWhite, 1.6)
	fill(armL, gopherTan, 0.9)
	fill(armR, gopherTan, 0.9)
	fill(footL, gopherTan, 0.9)
	fill(footR, gopherTan, 0.9)
	return pts
}

// Physics the gopher stars share, so that the solver below matches step.
const (
	gopherFormTime = 1.1  // seconds from burst to the finished figure
	gopherDrag     = 0.05 // velocity factor per second
	gopherLife     = 3.6
)

// displacementForUnitVelocity returns how far a star with unit initial
// velocity travels in formTime under the drag, and how far gravity pulls
// a resting star down meanwhile, integrated with the same step the scene
// uses at tps ticks per second, so the figure lands where it is aimed.
func displacementForUnitVelocity(drag, gravity, formTime, tps float64) (travel, fall float64) {
	p := particle{vx: 1, drag: float32(drag), life: 1e9}
	q := particle{gravity: float32(gravity), drag: float32(drag), life: 1e9}
	for i := 0; i < int(formTime*tps+0.5); i++ {
		p.step(float32(1 / tps))
		q.step(float32(1 / tps))
	}
	return float64(p.x), float64(q.y)
}

// gopherBurst builds the stars of a gopher shell exploding at (x, y).
func gopherBurst(rnd *rand.Rand, x, y, vx, vy, scale float32, tps float64) []particle {
	pts := gopherPoints(rnd, 0.13)
	size := 3.1 * float64(scale) // box units to pixels: a 310 px tall gopher at 720p
	tilt := (rnd.Float64() - 0.5) * 0.3
	sx, sy := 0.92+0.16*rnd.Float64(), 0.92+0.16*rnd.Float64()
	gravity := 45 * float64(scale)
	travel, fall := displacementForUnitVelocity(gopherDrag, gravity, gopherFormTime, tps)
	out := make([]particle, 0, len(pts))
	for _, p := range pts {
		// Centre the box, distort a little, convert to pixels.
		ox, oy := (p.x-50)*sx, (p.y-55)*sy
		ox, oy = ox*math.Cos(tilt)-oy*math.Sin(tilt), ox*math.Sin(tilt)+oy*math.Cos(tilt)
		ox += (rnd.Float64() - 0.5) * 2.5
		oy += (rnd.Float64() - 0.5) * 2.5
		tx, ty := ox*size, oy*size
		// Velocity that lands the star at (tx, ty) after formTime, gravity
		// compensated; the figure then keeps sinking slowly.
		v0x := tx / travel
		v0y := (ty - fall) / travel
		life := float32(gopherLife + rnd.Float64()*0.6)
		out = append(out, particle{
			x: x, y: y, vx: float32(v0x) + vx*0.2, vy: float32(v0y) + vy*0.2,
			drag: gopherDrag, gravity: float32(gravity), life: life, total: life,
			size: 1.45 * scale, color: p.color, kind: Gopher,
		})
	}
	return out
}
