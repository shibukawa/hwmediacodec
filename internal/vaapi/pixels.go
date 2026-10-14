package vaapi

import (
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

// fillImage writes the NV12 frame into a mapped image of at least the frame
// size, padding with edge pixels.
func fillImage(dst []byte, img *sys.Image, f *codec.Frame, imgW, imgH int) error {
	if err := f.Validate(); err != nil {
		return err
	}
	if f.Format != codec.NV12 || imgW < f.Width || imgH < f.Height {
		return imageLayoutError("upload frame")
	}
	for p := 0; p < 2; p++ {
		pitch := int(img.Pitches[p])
		off := int(img.Offsets[p])
		rows, rowBytes := f.Height, f.Width
		imgRows := imgH
		if p == 1 {
			rows, rowBytes = (f.Height+1)/2, (f.Width+1)/2*2
			imgRows = (imgH + 1) / 2
		}
		if _, _, err := imageRegion(dst, img, p, 0, 0, imgRows, pitch); err != nil || rowBytes > pitch {
			return imageLayoutError("upload frame")
		}
		padBytes := imgW - rowBytes
		if padBytes > pitch-rowBytes {
			padBytes = pitch - rowBytes
		}
		src := f.Planes[p]
		stride := f.Strides[p]
		for r := 0; r < rows; r++ {
			row := dst[off+r*pitch : off+r*pitch+pitch]
			copy(row, src[r*stride:r*stride+rowBytes])
			if padBytes > 0 {
				if p == 0 {
					last := row[rowBytes-1]
					for i := rowBytes; i < rowBytes+padBytes; i++ {
						row[i] = last
					}
				} else {
					cb, cr := row[rowBytes-2], row[rowBytes-1]
					for i := rowBytes; i+1 < rowBytes+padBytes; i += 2 {
						row[i], row[i+1] = cb, cr
					}
				}
			}
		}
		if rows > 0 {
			last := dst[off+(rows-1)*pitch : off+(rows-1)*pitch+rowBytes+padBytes]
			for r := rows; r < imgRows; r++ {
				copy(dst[off+r*pitch:], last)
			}
		}
	}
	return nil
}

func imageLayoutError(op string) error {
	return &codec.BackendError{Backend: Name, Op: op, Message: "image smaller than the picture or invalid plane layout"}
}

// imageRegion validates a rectangle before any offset multiplication. Its
// returned slice starts at the rectangle's first byte and retains row padding.
func imageRegion(data []byte, img *sys.Image, p, x, y, rows, rowBytes int) ([]byte, int, error) {
	if p < 0 || p >= 2 || img.NumPlanes < 2 || x < 0 || y < 0 || rows <= 0 || rowBytes <= 0 {
		return nil, 0, imageLayoutError("copy frame")
	}
	pitch, off := int(img.Pitches[p]), int(img.Offsets[p])
	if pitch <= 0 || off < 0 || off > len(data) || x > pitch || rowBytes > pitch-x || y > (len(data)-off)/pitch {
		return nil, 0, imageLayoutError("copy frame")
	}
	off += y * pitch
	if x > len(data)-off || rowBytes > len(data)-off-x {
		return nil, 0, imageLayoutError("copy frame")
	}
	off += x
	if rows-1 > (len(data)-off-rowBytes)/pitch {
		return nil, 0, imageLayoutError("copy frame")
	}
	return data[off:], pitch, nil
}
