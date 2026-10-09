package main

import (
	"math"
	"testing"
)

func TestCubeMeshCullsBackFaces(t *testing.T) {
	cam := camera{cx: 640, cy: 360, focal: 600, distance: 4}
	// Looking straight at the cube only the front face is visible.
	m := cubeMesh(cam, 0, 0, 320, 240, 4)
	if want := (4 + 1) * (4 + 1); len(m.vertices) != want {
		t.Fatalf("%d vertices, want %d (one face of 4x4 cells)", len(m.vertices), want)
	}
	if want := 4 * 4 * 6; len(m.indices) != want {
		t.Fatalf("%d indices, want %d", len(m.indices), want)
	}
	// Texture coordinates span the texture exactly once.
	var minU, maxU, minV, maxV float32 = 1e9, -1e9, 1e9, -1e9
	for _, v := range m.vertices {
		minU, maxU = min(minU, v.SrcX), max(maxU, v.SrcX)
		minV, maxV = min(minV, v.SrcY), max(maxV, v.SrcY)
	}
	if minU != 0 || maxU != 320 || minV != 0 || maxV != 240 {
		t.Errorf("texture coordinates span %g..%g x %g..%g", minU, maxU, minV, maxV)
	}
	// The front face projects to a centred square.
	x0, y0 := cam.project(rotate(cubeFaces[0][0], 0, 0))
	x2, y2 := cam.project(rotate(cubeFaces[0][2], 0, 0))
	if math.Abs((x0+x2)/2-640) > 1e-9 || math.Abs((y0+y2)/2-360) > 1e-9 || math.Abs((x2-x0)-(y2-y0)) > 1e-9 {
		t.Errorf("front face projects to (%g,%g)-(%g,%g)", x0, y0, x2, y2)
	}

	// Turned by 45 degrees around Y, two faces show.
	m = cubeMesh(cam, math.Pi/4, 0, 320, 240, 2)
	if got := len(m.vertices) / 9; got != 2 {
		t.Errorf("%d faces visible at 45 degrees, want 2", got)
	}
	// Tilted as well: three faces.
	m = cubeMesh(cam, math.Pi/4, math.Pi/5, 320, 240, 2)
	if got := len(m.vertices) / 9; got != 3 {
		t.Errorf("%d faces visible when tilted, want 3", got)
	}
	// Every index addresses a vertex.
	for _, i := range m.indices {
		if int(i) >= len(m.vertices) {
			t.Fatalf("index %d out of range", i)
		}
	}
}
