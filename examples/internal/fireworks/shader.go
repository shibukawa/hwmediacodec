package fireworks

// postShader composes the frame: a night sky with procedural twinkling
// stars and a warm glow over the city, this frame's stars (Images[0]) and
// their fading trails (Images[1]) with a cheap bloom, the skyline mask
// (Images[2]: alpha occludes, rgb adds lit windows), and below the horizon
// a rippling, dimmed reflection of all that.
const postShader = `
//kage:unit pixels

package main

var Time float
var Horizon float

func hash(p vec2) float {
	return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453)
}

func stars(p vec2) vec3 {
	cell := floor(p / 7)
	h := hash(cell)
	if h < 0.975 {
		return vec3(0)
	}
	off := vec2(hash(cell+vec2(1.3, 9.1)), hash(cell+vec2(7.1, 2.3))) * 7
	d := length(p - (cell*7 + off))
	tw := 0.55 + 0.45*sin(Time*(1.5+5*hash(cell+vec2(3.3, 4.4)))+h*60)
	mag := (h - 0.975) / 0.025
	return vec3(1, 0.96, 0.88) * tw * (0.35 + 0.65*mag) * smoothstep(1.3, 0.0, d)
}

func scene(p vec2) vec3 {
	t := clamp(p.y/Horizon, 0, 1)
	sky := mix(vec3(0.015, 0.02, 0.07), vec3(0.07, 0.06, 0.14), t*t)
	sky += vec3(0.16, 0.09, 0.05) * pow(t, 8) // city glow at the horizon
	col := sky + stars(p)
	src := p + imageSrc0Origin()
	fw := imageSrc0At(src).rgb + imageSrc1At(src).rgb*0.9
	bloom := vec3(0)
	for i := 0; i < 8; i++ {
		a := float(i) * 0.7853982
		o := vec2(cos(a), sin(a))
		bloom += imageSrc0At(src+o*6).rgb + imageSrc1At(src+o*6).rgb*0.5
		bloom += (imageSrc0At(src+o*14).rgb + imageSrc1At(src+o*14).rgb*0.5) * 0.6
	}
	col += fw + bloom*0.06
	m := imageSrc2At(src)
	col = mix(col, vec3(0.005, 0.006, 0.012), m.a) + m.rgb
	return col
}

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := dstPos.xy - imageDstOrigin()
	if p.y < Horizon {
		return vec4(scene(p), 1)
	}
	d := p.y - Horizon
	wobble := sin(d*0.3+Time*2.2+p.x*0.015)*(1.5+d*0.06) + sin(d*0.9-Time*3.1)*0.8
	rp := vec2(p.x+wobble, max(Horizon-d*1.05-1, 0))
	c := scene(rp)*vec3(0.32, 0.38, 0.48) + vec3(0.0, 0.01, 0.03)
	c *= 1 - smoothstep(0, 120, d)*0.35 // darker further from the shore
	return vec4(c, 1)
}
`
