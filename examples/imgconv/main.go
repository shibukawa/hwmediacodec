// Command imgconv converts between HEIC/AVIF and PNG/JPEG:
//
//	go run ./imgconv photo.heic photo.png          # decode (HEVC or AV1 items, grids, rotation)
//	go run ./imgconv -quality 0.8 picture.png picture.heic
//	go run ./imgconv -tile 512 -rotate 90 picture.jpg picture.heic
//	go run ./imgconv picture.png picture.avif
//	go run ./imgconv -info photo.heic
//
// The output format follows the extension. The hardware codecs do the work
// through the image/heif and image/avif packages; on a machine without one
// for the format (no Apple Silicon chip encodes AV1, for example) the
// program falls back to codecs that need neither hardware nor cgo:
// github.com/gen2brain/h265, an HEVC codec written in Go, and
// github.com/gen2brain/avif, libavif compiled to WebAssembly. -engine
// hardware or -engine go forces one side.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/shibukawa/hwmediacodec/image/avif"
	"github.com/shibukawa/hwmediacodec/image/heif"
)

func main() {
	quality := flag.Float64("quality", 0, "quality in (0, 1] for HEIC/AVIF output (0 = encoder default)")
	tile := flag.Int("tile", 0, "write pictures larger than this as a grid of square tiles (0 = one picture)")
	rotate := flag.Int("rotate", 0, "counter-clockwise rotation of 90, 180 or 270 degrees for HEIC/AVIF output")
	info := flag.Bool("info", false, "print what a HEIC/AVIF file contains and exit")
	engineName := flag.String("engine", "auto", "auto (hardware, else the cgo-free fallback), hardware or go")
	software := flag.Bool("software", false, "allow the OS software codec on the hardware path")
	jpegQuality := flag.Int("jpeg-quality", 92, "JPEG output quality")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: imgconv [flags] input output\n       imgconv -info input\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if (*info && flag.NArg() != 1) || (!*info && flag.NArg() != 2) {
		flag.Usage()
		os.Exit(2)
	}
	e, err := parseEngine(*engineName)
	if err == nil {
		o := encodeOptions{quality: *quality, tile: *tile, rotation: *rotate, software: *software}
		err = run(flag.Arg(0), flag.Arg(1), *info, o, *jpegQuality, e)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "imgconv:", err)
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

// describe prints the structure of a HEIC or AVIF file; no decoder is
// involved.
func describe(path string, data []byte) error {
	format, err := formatOf(data)
	if err != nil {
		return err
	}
	if format == formatAVIF {
		i, err := avif.DecodeInfo(data)
		if err != nil {
			return err
		}
		fmt.Printf("%s: avif, %dx%d, %d tile(s), rotation %d, mirror %v\n", path, i.Width, i.Height, i.Tiles, i.Rotation, i.Mirror)
		return nil
	}
	i, err := heif.DecodeInfo(data)
	if err != nil {
		return err
	}
	fmt.Printf("%s: heic, %dx%d, %d tile(s), rotation %d, mirror %v\n", path, i.Width, i.Height, i.Tiles, i.Rotation, i.Mirror)
	return nil
}

func run(in, out string, infoOnly bool, o encodeOptions, jpegQuality int, e engine) error {
	var img image.Image
	if isHEIF(in) {
		data, err := os.ReadFile(in)
		if err != nil {
			return err
		}
		if infoOnly {
			return describe(in, data)
		}
		var used string
		if img, used, err = decodeHEIF(data, e, o.software); err != nil {
			return fmt.Errorf("%s: %w", in, err)
		}
		if used != usedHardware {
			fmt.Fprintf(os.Stderr, "imgconv: %s decoded with the %s decoder\n", in, used)
		}
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

	ext := strings.ToLower(filepath.Ext(out))
	switch ext {
	case ".heic", ".heif", ".hif", ".avif":
		format := formatHEIC
		if ext == ".avif" {
			format = formatAVIF
		}
		// Encoded in memory first, so that a failure leaves no file behind.
		var buf bytes.Buffer
		used, err := encodeHEIF(&buf, img, format, o, e)
		if err != nil {
			return fmt.Errorf("%s: %w", out, err)
		}
		if used != usedHardware {
			fmt.Fprintf(os.Stderr, "imgconv: %s encoded with the %s encoder\n", out, used)
		}
		return os.WriteFile(out, buf.Bytes(), 0o644)
	case ".png", ".jpg", ".jpeg":
	default:
		return fmt.Errorf("unknown output format %q", ext)
	}
	w, err := os.Create(out)
	if err != nil {
		return err
	}
	defer w.Close()
	if ext == ".png" {
		return png.Encode(w, img)
	}
	return jpeg.Encode(w, img, &jpeg.Options{Quality: jpegQuality})
}
