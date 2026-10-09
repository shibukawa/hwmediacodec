package main

import "image"

// downscale shrinks img to the given width with a box filter (each output
// pixel is the mean of the source pixels it covers), keeping the aspect
// ratio. It is enough for thumbnails and keeps the sample free of image
// libraries.
func downscale(img *image.RGBA, width int) *image.RGBA {
	sw, sh := img.Rect.Dx(), img.Rect.Dy()
	height := (sh*width + sw/2) / sw
	if height < 1 {
		height = 1
	}
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		y0, y1 := y*sh/height, (y+1)*sh/height
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < width; x++ {
			x0, x1 := x*sw/width, (x+1)*sw/width
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, n uint32
			for sy := y0; sy < y1; sy++ {
				row := img.Pix[sy*img.Stride:]
				for sx := x0; sx < x1; sx++ {
					p := row[sx*4 : sx*4+4]
					r += uint32(p[0])
					g += uint32(p[1])
					b += uint32(p[2])
					n++
				}
			}
			o := out.Pix[y*out.Stride+x*4:]
			o[0], o[1], o[2], o[3] = byte(r/n), byte(g/n), byte(b/n), 255
		}
	}
	return out
}
