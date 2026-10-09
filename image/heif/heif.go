// Package heif reads and writes HEIC still images (HEIF with HEVC pictures) with the hardware
// codecs, in the manner of image/jpeg and image/png: Decode and
// DecodeConfig take an io.Reader, Encode an image.Image, and importing the
// package registers the format with the image package, so that
// image.Decode understands HEIC files:
//
//	import _ "github.com/shibukawa/hwmediacodec/image/heif"
//
//	img, format, err := image.Decode(file) // format is "heic"
//
// Decoding needs a hardware HEVC decoder on the machine and fails with an
// error that wraps hwmediacodec.ErrUnsupported without one; encoding needs
// a hardware HEVC encoder in the same way. DecodeConfig and DecodeInfo
// only read the file structure and work everywhere. Pictures are 8-bit
// 4:2:0 and opaque: alpha planes are neither read nor written.
//
// AVIF, the same container with AV1 pictures, is the image/avif package;
// this one rejects such files.
//
// Files whose major brand is the generic "mif1" or "msf1" do not say how
// their pictures are coded. Either package registers them with the image
// package as "heif", and image.Decode reads them when the package for
// their codec is linked into the program.
package heif

import (
	"fmt"
	"image"
	"io"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/imageitem"
)

const codec = hwmediacodec.HEVC

func init() {
	// The major brand follows the box size and "ftyp".
	for _, b := range []string{"heic", "heix", "hevc", "hevx", "heim", "heis", "hevm", "hevs"} {
		image.RegisterFormat("heic", "????ftyp"+b, Decode, DecodeConfig)
	}
	imageitem.Register(codec)
}

// Info describes the primary image of a file.
type Info struct {
	Width    int // of the displayed picture, after cropping and rotation
	Height   int
	Tiles    int // 1 for a single coded picture, more for a grid
	Rotation int // degrees counter-clockwise applied from irot
	Mirror   bool
}

func publicInfo(i *imageitem.Info) *Info {
	return &Info{Width: i.Width, Height: i.Height, Tiles: i.Tiles, Rotation: i.Rotation, Mirror: i.Mirror}
}

// Options control Encode.
type Options struct {
	// Quality in (0, 1]; 0 uses the encoder's default.
	Quality float64
	// TileSize splits pictures larger than it into a grid of square tiles,
	// the way phone cameras write their photos; 0 writes one picture.
	TileSize int
	// Rotation in degrees counter-clockwise (0, 90, 180, 270) is stored
	// as an irot property: the pixels stay as given, viewers rotate.
	Rotation int
	// Software allows the OS software encoder.
	Software bool
}

// Decode reads a HEIC file from r and returns its primary image as an
// *image.RGBA. It is DecodeBytes without options, in the form the image
// package registers.
func Decode(r io.Reader) (image.Image, error) {
	img, err := imageitem.Decode(r, codec)
	if err != nil {
		return nil, fmt.Errorf("heif: %w", err)
	}
	return img, nil
}

// DecodeConfig returns the colour model and the dimensions of the image
// Decode would return (after cropping and rotation) without decoding it,
// so it needs no hardware codec.
func DecodeConfig(r io.Reader) (image.Config, error) {
	cfg, err := imageitem.DecodeConfig(r, codec)
	if err != nil {
		return cfg, fmt.Errorf("heif: %w", err)
	}
	return cfg, nil
}

// DecodeBytes decodes the primary image of a HEIC file held in data.
// Grid images are decoded tile by tile and stitched; clap, irot and imir
// are applied. The options are passed to the decoder
// (hwmediacodec.WithSoftwareFallback, for example).
func DecodeBytes(data []byte, opts ...hwmediacodec.DecoderOption) (*image.RGBA, *Info, error) {
	img, info, err := imageitem.DecodeBytes(data, codec, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("heif: %w", err)
	}
	return img, publicInfo(info), nil
}

// DecodeInfo describes the primary image of a HEIC file held in data
// from the file structure alone: nothing is decoded.
func DecodeInfo(data []byte) (*Info, error) {
	info, err := imageitem.DecodeInfo(data, codec)
	if err != nil {
		return nil, fmt.Errorf("heif: %w", err)
	}
	return publicInfo(info), nil
}

// Encode writes img as a HEIC file. A nil *Options means the defaults,
// as with jpeg.Encode. Nothing is written to w unless encoding succeeded.
func Encode(w io.Writer, img image.Image, o *Options) error {
	opts := imageitem.Options{Codec: codec}
	if o != nil {
		opts.Quality, opts.TileSize, opts.Rotation, opts.Software = o.Quality, o.TileSize, o.Rotation, o.Software
	}
	if err := imageitem.Encode(w, img, opts); err != nil {
		return fmt.Errorf("heif: %w", err)
	}
	return nil
}
