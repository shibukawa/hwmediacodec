// Command heifconv converts between HEIC/AVIF and PNG/JPEG with the
// hardware codecs:
//
//	go run ./heifconv photo.heic photo.png          # decode (HEVC or AV1 items, grids, rotation)
//	go run ./heifconv -quality 0.8 picture.png picture.heic
//	go run ./heifconv -tile 512 -rotate 90 picture.jpg picture.heic
//	go run ./heifconv picture.png picture.avif      # needs a hardware AV1 encoder
//	go run ./heifconv -info photo.heic
//
// The output format follows the extension.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/image/heif"
)

func main() {
	quality := flag.Float64("quality", 0, "constant quality in (0, 1] for HEIC/AVIF output (0 = encoder default)")
	tile := flag.Int("tile", 0, "write pictures larger than this as a grid of square tiles (0 = one picture)")
	rotate := flag.Int("rotate", 0, "store a counter-clockwise rotation of 90, 180 or 270 degrees (irot)")
	info := flag.Bool("info", false, "print what a HEIC/AVIF file contains and exit")
	software := flag.Bool("software", false, "allow the OS software codec")
	jpegQuality := flag.Int("jpeg-quality", 92, "JPEG output quality")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: heifconv [flags] input output\n       heifconv -info input\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if (*info && flag.NArg() != 1) || (!*info && flag.NArg() != 2) {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(flag.Arg(0), flag.Arg(1), *info, heif.Options{Quality: *quality, TileSize: *tile, Rotation: *rotate, Software: *software}, *jpegQuality, *software); err != nil {
		fmt.Fprintln(os.Stderr, "heifconv:", err)
		os.Exit(1)
	}
}

func isHEIF(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".heic", ".heif", ".hif", ".avif":
		return true
	}
	return false
}

func run(in, out string, infoOnly bool, o heif.Options, jpegQuality int, software bool) error {
	var img image.Image
	if isHEIF(in) {
		data, err := os.ReadFile(in)
		if err != nil {
			return err
		}
		if infoOnly {
			// The file structure alone: no decoder needed.
			info, err := heif.DecodeInfo(data)
			if err != nil {
				return err
			}
			fmt.Printf("%s: %s, %dx%d, %d tile(s), rotation %d, mirror %v\n", in, info.Codec, info.Width, info.Height, info.Tiles, info.Rotation, info.Mirror)
			return nil
		}
		var opts []hwmediacodec.DecoderOption
		if software {
			opts = append(opts, hwmediacodec.WithSoftwareFallback())
		}
		decoded, _, err := heif.DecodeBytes(data, opts...)
		if err != nil {
			return err
		}
		img = decoded
	} else {
		if infoOnly {
			return fmt.Errorf("%s is not a HEIF file", in)
		}
		f, err := os.Open(in)
		if err != nil {
			return err
		}
		defer f.Close()
		if img, _, err = image.Decode(f); err != nil {
			return fmt.Errorf("%s: %w", in, err)
		}
	}

	w, err := os.Create(out)
	if err != nil {
		return err
	}
	defer w.Close()
	switch ext := strings.ToLower(filepath.Ext(out)); ext {
	case ".heic", ".heif", ".hif":
		o.Codec = hwmediacodec.HEVC
		return heif.Encode(w, img, &o)
	case ".avif":
		o.Codec = hwmediacodec.AV1
		return heif.Encode(w, img, &o)
	case ".png":
		return png.Encode(w, img)
	case ".jpg", ".jpeg":
		return jpeg.Encode(w, img, &jpeg.Options{Quality: jpegQuality})
	default:
		return fmt.Errorf("unknown output format %q", ext)
	}
}
