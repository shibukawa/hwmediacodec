package fireworks

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestGopherPointsCoverTheFigure(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	pts := gopherPoints(rnd, 0.13)
	if len(pts) < 500 || len(pts) > 1600 {
		t.Fatalf("%d points", len(pts))
	}
	colors := map[rgb]int{}
	for _, p := range pts {
		if p.x < 5 || p.x > 95 || p.y < 10 || p.y > 100 {
			t.Fatalf("point (%.1f, %.1f) outside the box", p.x, p.y)
		}
		if pupilL.contains(p.x, p.y) || pupilR.contains(p.x, p.y) {
			t.Fatalf("point (%.1f, %.1f) inside a pupil", p.x, p.y)
		}
		colors[p.color]++
	}
	for _, c := range []rgb{gopherBlue, gopherBlue.scale(0.7), gopherTan, gopherWhite, gopherNose} {
		if colors[c] == 0 {
			t.Errorf("no points of colour %v", c)
		}
	}
	if colors[gopherWhite] < 100 {
		t.Errorf("only %d eye/teeth points", colors[gopherWhite])
	}
}

// TestGopherBurstFormsTheFigure integrates the stars with the particle
// physics for the formation time and checks that they land on a gopher of
// the intended size, centred on the burst.
func TestGopherBurstFormsTheFigure(t *testing.T) {
	rnd := rand.New(rand.NewPCG(3, 4))
	const scale = 1.0
	stars := gopherBurst(rnd, 600, 300, 0, 0, scale, 60)
	if len(stars) < 500 {
		t.Fatalf("%d stars", len(stars))
	}
	const tps = 60.0
	for i := 0; i < int(gopherFormTime*tps); i++ {
		for k := range stars {
			stars[k].step(1 / tps)
		}
	}
	var minX, maxX, minY, maxY float32 = 1e9, -1e9, 1e9, -1e9
	var cx, cy float32
	for _, p := range stars {
		minX, maxX = min(minX, p.x), max(maxX, p.x)
		minY, maxY = min(minY, p.y), max(maxY, p.y)
		cx += p.x
		cy += p.y
	}
	cx /= float32(len(stars))
	cy /= float32(len(stars))
	// The box is 100 units = 310 px; ears to feet span about 90 units.
	if w, h := maxX-minX, maxY-minY; w < 230 || w > 340 || h < 240 || h > 340 {
		t.Errorf("figure is %.0fx%.0f px, want about 280x290", w, h)
	}
	// Centred on the burst point give or take the distortion; the figure
	// hangs slightly below because gravity keeps acting after formation
	// is only compensated up to formTime.
	if math.Abs(float64(cx-600)) > 25 || math.Abs(float64(cy-300)) > 40 {
		t.Errorf("figure centre (%.0f, %.0f), want near (600, 300)", cx, cy)
	}
	// The eyes are white and sit in the upper half.
	var eyeY, eyeN float32
	for _, p := range stars {
		if p.color == gopherWhite && p.y < cy {
			eyeY += p.y
			eyeN++
		}
	}
	if eyeN < 50 || eyeY/eyeN > cy-30 {
		t.Errorf("%d white stars above the centre at mean y %.0f (centre %.0f)", int(eyeN), eyeY/eyeN, cy)
	}
}

func TestDisplacementMatchesIntegration(t *testing.T) {
	// The solver integrates with the scene's step, so a star aimed with it
	// lands within a pixel.
	travel, fall := displacementForUnitVelocity(gopherDrag, 45, gopherFormTime, 60)
	p := particle{vx: float32(200 / travel), vy: float32((-150 - fall) / travel), drag: gopherDrag, gravity: 45, life: 10}
	for i := 0; i < int(gopherFormTime*60); i++ {
		p.step(1.0 / 60)
	}
	if math.Abs(float64(p.x)-200) > 1 || math.Abs(float64(p.y)+150) > 1 {
		t.Errorf("landed at (%.2f, %.2f), want (200, -150)", p.x, p.y)
	}
}

func TestBurstsHaveStars(t *testing.T) {
	rnd := rand.New(rand.NewPCG(5, 6))
	for k := Peony; k < kindCount; k++ {
		stars := burst(rnd, k, 100, 100, 0, 0, 1, 60)
		if len(stars) < 100 {
			t.Errorf("%s: %d stars", k, len(stars))
		}
		for _, p := range stars {
			if p.total <= 0 || p.life <= 0 || p.size <= 0 {
				t.Fatalf("%s: star %+v", k, p)
			}
		}
	}
}
