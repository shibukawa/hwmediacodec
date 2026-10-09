package main

// crtShader bends, ripples and scanlines the video frame. The player's
// image is Images[0] of DrawRectShader; the shader samples it with
// imageSrc0At, so the video is just another texture to Kage.
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
	c *= 1 + 0.10*r2                     // barrel distortion
	uv = (c + 1) / 2
	uv.x += 0.004 * sin(uv.y*40+Time*4)  // a slow ripple
	if uv.x < 0 || uv.x > 1 || uv.y < 0 || uv.y > 1 {
		return vec4(0, 0, 0, 1)
	}
	clr := imageSrc0At(uv*size + origin)
	scan := 0.85 + 0.15*sin(dstPos.y*3.14159) // one scanline per two screen rows
	vignette := 1 - 0.45*r2
	return vec4(clr.rgb*scan*vignette, 1)
}
`
