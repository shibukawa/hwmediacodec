package codec

import (
	"fmt"
	"image"
)

// RGBAImage copies an RGBA or BGRA frame into an image. The image owns its
// pixels, so the frame can be released afterwards. Frames in another
// format are refused: open the decoder with WithOutputFormat(RGBA).
func (f *Frame) RGBAImage() (*image.RGBA, error) {
	if f.Format != RGBA && f.Format != BGRA {
		return nil, fmt.Errorf("hwmediacodec: a %s frame is not an RGB picture: %w", f.Format, ErrInvalidData)
	}
	if len(f.Planes) < 1 || len(f.Strides) < 1 || len(f.Planes[0]) < (f.Height-1)*f.Strides[0]+4*f.Width {
		return nil, fmt.Errorf("hwmediacodec: frame has no pixel data (released?): %w", ErrInvalidData)
	}
	img := image.NewRGBA(image.Rect(0, 0, f.Width, f.Height))
	src, stride := f.Planes[0], f.Strides[0]
	for y := 0; y < f.Height; y++ {
		copy(img.Pix[y*img.Stride:(y+1)*img.Stride], src[y*stride:y*stride+f.Width*4])
	}
	if f.Format == BGRA {
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+2] = img.Pix[i+2], img.Pix[i]
		}
	}
	return img, nil
}

// RGBAFrame wraps img as an encoder input frame without copying: the
// frame shares the image's pixels, which must stay untouched until
// Encoder.Send has returned. The alpha channel is ignored by the encoders.
func RGBAFrame(img *image.RGBA, pts int64) *Frame {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	pix := img.Pix
	if off := img.PixOffset(img.Rect.Min.X, img.Rect.Min.Y); off > 0 && off <= len(pix) {
		pix = pix[off:]
	}
	return &Frame{Width: w, Height: h, Format: RGBA, Planes: [][]byte{pix}, Strides: []int{img.Stride}, PTS: pts}
}
