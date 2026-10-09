package main

import (
	"math"

	"github.com/hajimehoshi/ebiten/v2"
)

// The cube is textured with DrawTriangles. Ebitengine interpolates texture
// coordinates affinely (there is no perspective divide per pixel), so every
// face is split into a grid of small quads whose corners are projected
// individually; with 8x8 cells the distortion is invisible.

type vec3 struct{ x, y, z float64 }

func (a vec3) add(b vec3) vec3      { return vec3{a.x + b.x, a.y + b.y, a.z + b.z} }
func (a vec3) scale(s float64) vec3 { return vec3{a.x * s, a.y * s, a.z * s} }
func (a vec3) dot(b vec3) float64   { return a.x*b.x + a.y*b.y + a.z*b.z }
func (a vec3) cross(b vec3) vec3 {
	return vec3{a.y*b.z - a.z*b.y, a.z*b.x - a.x*b.z, a.x*b.y - a.y*b.x}
}
func lerp3(a, b vec3, t float64) vec3 { return a.add(b.add(a.scale(-1)).scale(t)) }

// rotate applies a rotation about the Y axis then the X axis.
func rotate(p vec3, ay, ax float64) vec3 {
	sy, cy := math.Sincos(ay)
	p = vec3{p.x*cy + p.z*sy, p.y, -p.x*sy + p.z*cy}
	sx, cx := math.Sincos(ax)
	return vec3{p.x, p.y*cx - p.z*sx, p.y*sx + p.z*cx}
}

// camera projects points in front of a pinhole camera at the origin
// looking down +z; the scene is translated by distance first.
type camera struct {
	cx, cy   float64 // screen centre
	focal    float64 // pixels per unit at distance 1
	distance float64
}

func (c camera) project(p vec3) (x, y float64) {
	z := p.z + c.distance
	return c.cx + c.focal*p.x/z, c.cy + c.focal*p.y/z
}

// face is one quad of the cube: four corners counter-clockwise when seen
// from outside, with texture coordinates (0,0) (1,0) (1,1) (0,1).
type face [4]vec3

var cubeFaces = []face{
	{{-1, -1, -1}, {1, -1, -1}, {1, 1, -1}, {-1, 1, -1}}, // front (towards the camera, z = -1)
	{{1, -1, 1}, {-1, -1, 1}, {-1, 1, 1}, {1, 1, 1}},     // back
	{{1, -1, -1}, {1, -1, 1}, {1, 1, 1}, {1, 1, -1}},     // right
	{{-1, -1, 1}, {-1, -1, -1}, {-1, 1, -1}, {-1, 1, 1}}, // left
	{{-1, -1, 1}, {1, -1, 1}, {1, -1, -1}, {-1, -1, -1}}, // top (y = -1 is up on screen)
	{{-1, 1, -1}, {1, 1, -1}, {1, 1, 1}, {-1, 1, 1}},     // bottom
}

// mesh accumulates vertices and indices for one DrawTriangles call.
type mesh struct {
	vertices []ebiten.Vertex
	indices  []uint16
}

// addFace appends a textured, lit face unless it faces away from the
// camera. texW and texH are the texture size in pixels, grid the number of
// cells per side.
func (m *mesh) addFace(f face, cam camera, ay, ax float64, texW, texH float64, grid int, light vec3) bool {
	var p [4]vec3
	for i := range f {
		p[i] = rotate(f[i], ay, ax)
	}
	// Back-face culling on the projected winding: the corners are listed
	// counter-clockwise seen from outside, which on screen (y down) is a
	// positive cross product when the face points at the camera.
	x0, y0 := cam.project(p[0])
	x1, y1 := cam.project(p[1])
	x2, y2 := cam.project(p[2])
	if (x1-x0)*(y2-y0)-(x2-x0)*(y1-y0) <= 0 {
		return false
	}
	// Outward normal, lit by a directional light (light points towards
	// the light source).
	normal := p[3].add(p[0].scale(-1)).cross(p[1].add(p[0].scale(-1)))
	n := math.Sqrt(normal.dot(normal))
	shade := 0.35 + 0.65*math.Max(0, normal.scale(1/n).dot(light))

	base := uint16(len(m.vertices))
	for j := 0; j <= grid; j++ {
		v := float64(j) / float64(grid)
		left := lerp3(p[0], p[3], v)
		right := lerp3(p[1], p[2], v)
		for i := 0; i <= grid; i++ {
			u := float64(i) / float64(grid)
			x, y := cam.project(lerp3(left, right, u))
			m.vertices = append(m.vertices, ebiten.Vertex{
				DstX: float32(x), DstY: float32(y),
				SrcX: float32(u * texW), SrcY: float32(v * texH),
				ColorR: float32(shade), ColorG: float32(shade), ColorB: float32(shade), ColorA: 1,
			})
		}
	}
	stride := uint16(grid + 1)
	for j := 0; j < grid; j++ {
		for i := 0; i < grid; i++ {
			a := base + uint16(j)*stride + uint16(i)
			m.indices = append(m.indices, a, a+1, a+stride, a+1, a+stride+1, a+stride)
		}
	}
	return true
}

// cubeMesh builds the visible faces of the cube for the given rotation.
func cubeMesh(cam camera, ay, ax float64, texW, texH float64, grid int) mesh {
	var m mesh
	light := vec3{0.3, -0.5, -0.8}
	l := math.Sqrt(light.dot(light))
	light = light.scale(1 / l)
	for _, f := range cubeFaces {
		m.addFace(f, cam, ay, ax, texW, texH, grid, light)
	}
	return m
}
