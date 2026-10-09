package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"io"

	goavif "github.com/gen2brain/avif"
	goheic "github.com/gen2brain/h265/heic"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/image/avif"
	"github.com/shibukawa/hwmediacodec/image/heif"
)

// engine selects who does the codec work.
type engine int

const (
	// engineAuto uses the hardware codec and, on a machine that has none
	// for the format, the cgo-free fallback.
	engineAuto engine = iota
	engineHardware
	engineGo
)

func parseEngine(s string) (engine, error) {
	switch s {
	case "auto":
		return engineAuto, nil
	case "hardware":
		return engineHardware, nil
	case "go":
		return engineGo, nil
	}
	return 0, fmt.Errorf("unknown engine %q (auto, hardware, go)", s)
}

// The fallbacks, for messages: HEIC is github.com/gen2brain/h265, an HEVC
// codec written in Go; AVIF is github.com/gen2brain/avif, libavif (dav1d
// and libaom) compiled to WebAssembly and run by wazero. Neither needs cgo.
const (
	usedHardware = "hardware"
	usedGoHEIC   = "pure Go (gen2brain/h265)"
	usedGoAVIF   = "cgo-free (gen2brain/avif: libavif as WebAssembly)"
)

const (
	formatHEIC = "heic"
	formatAVIF = "avif"
)

// formatOf tells HEIC from AVIF. The two packages of the core module
// answer from the file structure; a file neither can describe is judged by
// its brands, so that the fallback decoders, which read more variants
// (10-bit, alpha), still get it.
func formatOf(data []byte) (string, error) {
	_, herr := heif.DecodeInfo(data)
	if herr == nil {
		return formatHEIC, nil
	}
	if _, err := avif.DecodeInfo(data); err == nil {
		return formatAVIF, nil
	}
	if len(data) < 16 || string(data[4:8]) != "ftyp" {
		return "", herr
	}
	size := int(data[0])<<24 | int(data[1])<<16 | int(data[2])<<8 | int(data[3])
	if size < 16 || size > len(data) {
		return "", herr
	}
	for off := 8; off+4 <= size; off += 4 {
		if off == 12 { // minor version
			continue
		}
		switch string(data[off : off+4]) {
		case "avif", "avis":
			return formatAVIF, nil
		}
	}
	return formatHEIC, nil
}

// decodeHEIF decodes a HEIC or AVIF file. It reports who decoded it.
func decodeHEIF(data []byte, e engine, software bool) (image.Image, string, error) {
	format, err := formatOf(data)
	if err != nil {
		return nil, "", err
	}
	if e != engineGo {
		var opts []hwmediacodec.DecoderOption
		if software {
			opts = append(opts, hwmediacodec.WithSoftwareFallback())
		}
		var img *image.RGBA
		if format == formatAVIF {
			img, _, err = avif.DecodeBytes(data, opts...)
		} else {
			img, _, err = heif.DecodeBytes(data, opts...)
		}
		if err == nil {
			return img, usedHardware, nil
		}
		if e == engineHardware || !errors.Is(err, hwmediacodec.ErrUnsupported) {
			return nil, "", err
		}
	}
	if format == formatAVIF {
		img, err := goavif.Decode(bytes.NewReader(data), goavif.Options{AutoRotate: true})
		return img, usedGoAVIF, err
	}
	img, err := goheic.Decode(bytes.NewReader(data), goheic.Options{AutoRotate: true})
	return img, usedGoHEIC, err
}

// encodeOptions are the picture options of the command line.
type encodeOptions struct {
	quality  float64 // (0, 1]; 0 = the encoder's default
	tile     int
	rotation int // degrees counter-clockwise
	software bool
}

// encodeHEIF writes img as HEIC or AVIF. It reports who encoded it.
func encodeHEIF(w io.Writer, img image.Image, format string, o encodeOptions, e engine) (string, error) {
	if e != engineGo {
		// Into a buffer: nothing must reach w when the fallback takes over.
		var buf bytes.Buffer
		var err error
		if format == formatAVIF {
			err = avif.Encode(&buf, img, &avif.Options{Quality: o.quality, TileSize: o.tile, Rotation: o.rotation, Software: o.software})
		} else {
			err = heif.Encode(&buf, img, &heif.Options{Quality: o.quality, TileSize: o.tile, Rotation: o.rotation, Software: o.software})
		}
		if err == nil {
			_, err = w.Write(buf.Bytes())
			return usedHardware, err
		}
		if e == engineHardware || !errors.Is(err, hwmediacodec.ErrUnsupported) {
			return "", err
		}
	}
	// The fallback encoders store no rotation, so it goes into the pixels:
	// the picture a viewer shows is the same.
	if o.rotation%360 != 0 {
		if o.rotation%90 != 0 {
			return "", fmt.Errorf("rotation must be a multiple of 90, got %d", o.rotation)
		}
		img = rotateCCW(img, o.rotation)
	}
	quality := int(o.quality*100 + 0.5)
	if format == formatAVIF {
		// 4:2:0 is what hardware AV1 decoders read; the library's default
		// is 4:4:4.
		opts := goavif.Options{Quality: quality, Speed: 8, ChromaSubsampling: image.YCbCrSubsampleRatio420}
		if quality == 0 {
			opts.Quality = goavif.DefaultQuality
		}
		return usedGoAVIF, goavif.Encode(w, img, opts)
	}
	opts := goheic.EncodeOptions{Quality: quality, Tile: o.tile}
	if quality == 0 {
		opts.Quality = goheic.DefaultQuality
	}
	return usedGoHEIC, goheic.Encode(w, img, opts)
}

// rotateCCW turns img counter-clockwise by a multiple of 90 degrees.
func rotateCCW(src image.Image, angle int) *image.RGBA {
	b := src.Bounds()
	in := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(in, in.Rect, src, b.Min, draw.Src)
	w, h := b.Dx(), b.Dy()
	angle = ((angle % 360) + 360) % 360
	if angle == 0 {
		return in
	}
	ow, oh := w, h
	if angle != 180 {
		ow, oh = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch angle {
			case 90:
				dx, dy = y, w-1-x
			case 180:
				dx, dy = w-1-x, h-1-y
			default: // 270
				dx, dy = h-1-y, x
			}
			copy(out.Pix[out.PixOffset(dx, dy):], in.Pix[in.PixOffset(x, y):in.PixOffset(x, y)+4])
		}
	}
	return out
}
