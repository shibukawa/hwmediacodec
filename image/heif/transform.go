package heif

import "image"

// crop returns img with its origin at (0, 0) and a tight pixel buffer.
func crop(img *image.RGBA) *image.RGBA {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if img.Rect.Min == (image.Point{}) && img.Stride == 4*w {
		return img
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		off := img.PixOffset(img.Rect.Min.X, img.Rect.Min.Y+y)
		copy(out.Pix[y*out.Stride:(y+1)*out.Stride], img.Pix[off:off+4*w])
	}
	return out
}

// rotate turns img counter-clockwise by angle degrees (0, 90, 180, 270),
// the direction HEIF's irot specifies.
func rotate(img *image.RGBA, angle int) *image.RGBA {
	img = crop(img)
	w, h := img.Rect.Dx(), img.Rect.Dy()
	var out *image.RGBA
	switch angle % 360 {
	case 90:
		out = image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				// (x, y) moves to (y, w-1-x).
				copy(out.Pix[(w-1-x)*out.Stride+y*4:][:4], img.Pix[y*img.Stride+x*4:][:4])
			}
		}
	case 180:
		out = image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				copy(out.Pix[(h-1-y)*out.Stride+(w-1-x)*4:][:4], img.Pix[y*img.Stride+x*4:][:4])
			}
		}
	case 270:
		out = image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				// (x, y) moves to (h-1-y, x).
				copy(out.Pix[x*out.Stride+(h-1-y)*4:][:4], img.Pix[y*img.Stride+x*4:][:4])
			}
		}
	default:
		return img
	}
	return out
}

// mirror reflects img: about the vertical axis (left-right) when
// horizontalAxis is false, about the horizontal axis (top-bottom) when
// true, matching imir's axis bit.
func mirror(img *image.RGBA, horizontalAxis bool) *image.RGBA {
	img = crop(img)
	w, h := img.Rect.Dx(), img.Rect.Dy()
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := w-1-x, y
			if horizontalAxis {
				dx, dy = x, h-1-y
			}
			copy(out.Pix[dy*out.Stride+dx*4:][:4], img.Pix[y*img.Stride+x*4:][:4])
		}
	}
	return out
}
