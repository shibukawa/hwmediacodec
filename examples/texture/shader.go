package main

// crtShader gently bends and ripples the video frame and adds faint
// scanlines and a vignette, like an old display. The player's image is
// Images[0] of DrawRectShader; the shader samples it with imageSrc0At, so
// the video is just another texture to Kage. The amounts are kept small
// so that real footage still looks like itself.
const crtShader = `
//kage:unit pixels

package main

var Time float

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	origin := imageSrc0Origin()
	size := imageSrc0Size()
	uv := (srcPos - origin) / size
	c := uv*2 - 1
	r2 := dot(c, c)
	c *= 1 + 0.05*r2                     // slight barrel distortion
	uv = (c + 1) / 2
	uv.x += 0.002 * sin(uv.y*30+Time*3)  // a slow, shallow ripple
	if uv.x < 0 || uv.x > 1 || uv.y < 0 || uv.y > 1 {
		return vec4(0, 0, 0, 1)
	}
	clr := imageSrc0At(uv*size + origin)
	scan := 0.93 + 0.07*sin(dstPos.y*3.14159) // faint scanline per two screen rows
	vignette := 1 - 0.25*r2
	return vec4(clr.rgb*scan*vignette, 1)
}
`
