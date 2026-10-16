package avif_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/image/avif"
	"github.com/shibukawa/hwmediacodec/internal/mediatest"
)

// testImage is a smooth picture with hard edges: gradients, a few flat
// rectangles and a diagonal, so that codec loss stays small and any
// rotation or mirroring mistake is obvious.
func testImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{R: uint8(255 * x / w), G: uint8(255 * y / h), B: uint8(128 + 127*(x+y)/(w+h)), A: 255}
			switch {
			case x < w/4 && y < h/4:
				c = color.RGBA{R: 220, G: 40, B: 40, A: 255} // top-left: red
			case x > 3*w/4 && y > 3*h/4:
				c = color.RGBA{R: 40, G: 40, B: 220, A: 255} // bottom-right: blue
			case abs(x*h-y*w) < 2*h:
				c = color.RGBA{R: 255, G: 255, B: 255, A: 255} // diagonal
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// ffmpegAVIF writes img as an AVIF file with ffmpeg's libsvtav1, the
// reference for files this package reads, and skips the test without it.
func ffmpegAVIF(t *testing.T, dir string, img image.Image) string {
	t.Helper()
	mediatest.RequireFFmpeg(t)
	if !mediatest.HasEncoder(t, "libsvtav1") {
		t.Skip("ffmpeg has no libsvtav1")
	}
	srcPNG := filepath.Join(dir, "src.png")
	mediatest.SavePNG(t, srcPNG, img)
	return mediatest.FFmpegImage(t, srcPNG, filepath.Join(dir, "ffmpeg.avif"), "-c:v", "libsvtav1", "-pix_fmt", "yuv420p", "-f", "avif")
}

// checkImagePackage reads the same file the way a program that only knows
// the image package would: the format is recognised from the file header,
// DecodeConfig and DecodeInfo predict what DecodeBytes returned without
// decoding, and image.Decode returns the same pixels.
func checkImagePackage(t *testing.T, data []byte, want *image.RGBA, wantInfo *avif.Info) {
	t.Helper()
	info, err := avif.DecodeInfo(data)
	if err != nil {
		t.Fatalf("DecodeInfo: %v", err)
	}
	if *info != *wantInfo {
		t.Errorf("DecodeInfo %+v, DecodeBytes reported %+v", *info, *wantInfo)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("image.DecodeConfig: %v", err)
	}
	if format != "avif" {
		t.Errorf("image.DecodeConfig format %q, want avif", format)
	}
	if cfg.Width != want.Rect.Dx() || cfg.Height != want.Rect.Dy() || cfg.ColorModel != color.RGBAModel {
		t.Errorf("image.DecodeConfig %dx%d, decoded %v", cfg.Width, cfg.Height, want.Rect.Size())
	}
	img, format2, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("image.Decode: %v", err)
	}
	rgba, ok := img.(*image.RGBA)
	if !ok || format2 != format || rgba.Rect != want.Rect || !bytes.Equal(rgba.Pix, want.Pix) {
		t.Errorf("image.Decode returned %T %q %v, which differs from DecodeBytes", img, format2, img.Bounds())
	}
}

func TestDecodeAVIFFromFFmpeg(t *testing.T) {
	mediatest.RequireHardware(t, hwmediacodec.AV1, hwmediacodec.Decode)
	dir := t.TempDir()
	src := testImage(640, 480)
	path := ffmpegAVIF(t, dir, src)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, info, err := avif.DecodeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	checkImagePackage(t, data, got, info)
	if info.Tiles != 1 || got.Bounds().Dx() != 640 || got.Bounds().Dy() != 480 {
		t.Fatalf("decoded %v %+v", got.Bounds(), info)
	}
	ref := mediatest.LoadPNG(t, mediatest.FFmpegImage(t, path, filepath.Join(dir, "ffmpeg_dec.png")))
	if p := mediatest.BlockPSNR(got, ref, 4); p < 40 {
		t.Errorf("%.1f dB from ffmpeg's decode, want at least 40", p)
	}
	if p := mediatest.BlockPSNR(got, src, 4); p < 30 {
		t.Errorf("%.1f dB from the source, want at least 30", p)
	}
}

func TestEncodeNeedsAnEncoder(t *testing.T) {
	var buf bytes.Buffer
	err := avif.Encode(&buf, testImage(64, 64), nil)
	if err == nil {
		// A platform with a hardware AV1 encoder: the file must round-trip.
		img, info, derr := avif.DecodeBytes(buf.Bytes())
		if derr != nil || info.Tiles != 1 || img.Bounds().Dx() != 64 {
			t.Fatalf("AVIF round trip: %v %+v", derr, info)
		}
		return
	}
	if !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Fatalf("want ErrUnsupported without an AV1 encoder, got %v", err)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for _, in := range [][]byte{nil, []byte("not an avif file at all, just text"), bytes.Repeat([]byte{0}, 64)} {
		if _, _, err := avif.DecodeBytes(in); err == nil {
			t.Errorf("decoding %q succeeded", in)
		}
		if _, err := avif.Decode(bytes.NewReader(in)); err == nil {
			t.Errorf("Decode of %q succeeded", in)
		}
		if _, err := avif.DecodeConfig(bytes.NewReader(in)); err == nil {
			t.Errorf("DecodeConfig of %q succeeded", in)
		}
	}
}

// TestGenericBrand rewrites the major brand of an AVIF file to mif1, which
// does not name the codec: the image package then reports "heif" and the
// picture is decoded all the same.
func TestGenericBrand(t *testing.T) {
	mediatest.RequireHardware(t, hwmediacodec.AV1, hwmediacodec.Decode)
	data, err := os.ReadFile(ffmpegAVIF(t, t.TempDir(), testImage(320, 240)))
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := avif.DecodeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	copy(data[8:12], "mif1")
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != "heif" {
		t.Fatalf("image.Decode: %q %v", format, err)
	}
	if rgba, ok := img.(*image.RGBA); !ok || !bytes.Equal(rgba.Pix, want.Pix) {
		t.Errorf("the mif1 file decodes to %T, which differs from the avif one", img)
	}
}

// TestRejectsHEIC feeds a HEIC file to this package, which only reads AV1
// pictures: the error says where HEIC is handled. The file comes from
// macOS ImageIO.
func TestRejectsHEIC(t *testing.T) {
	if !mediatest.HasSips() {
		t.Skip("sips (macOS ImageIO) not available")
	}
	dir := t.TempDir()
	srcPNG := filepath.Join(dir, "src.png")
	mediatest.SavePNG(t, srcPNG, testImage(320, 240))
	heic := filepath.Join(dir, "sips.heic")
	mediatest.Sips(t, srcPNG, heic, "heic")
	data, err := os.ReadFile(heic)
	if err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"DecodeInfo":   second(avif.DecodeInfo(data)),
		"DecodeConfig": second(avif.DecodeConfig(bytes.NewReader(data))),
		"Decode":       second(avif.Decode(bytes.NewReader(data))),
	} {
		if err == nil || !strings.Contains(err.Error(), "image/heif") {
			t.Errorf("%s of a HEIC file: %v, want an error naming image/heif", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }

func third[T, U any](_ T, _ U, err error) error { return err }

// TestLimits lowers MaxPixels under a 640x480 picture: decoding refuses
// it with ErrTooLarge before any decoder is opened, so this holds without
// an AV1 decoder, while DecodeInfo still reports the size and a per-call
// Limits gets past the structure check to the decoder.
func TestLimits(t *testing.T) {
	path := ffmpegAVIF(t, t.TempDir(), testImage(640, 480))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func(pixels int) { avif.MaxPixels = pixels }(avif.MaxPixels)
	avif.MaxPixels = 100 * 100
	for name, err := range map[string]error{
		"DecodeBytes":  third(avif.DecodeBytes(data)),
		"Decode":       second(avif.Decode(bytes.NewReader(data))),
		"image.Decode": third(image.Decode(bytes.NewReader(data))),
	} {
		if !errors.Is(err, avif.ErrTooLarge) {
			t.Errorf("%s: %v, want ErrTooLarge", name, err)
		}
	}
	if info, err := avif.DecodeInfo(data); err != nil || info.Width != 640 || info.Height != 480 {
		t.Errorf("DecodeInfo under the limit = %+v, %v; want the declared 640x480", info, err)
	}
	if cfg, err := avif.DecodeConfig(bytes.NewReader(data)); err != nil || cfg.Width != 640 {
		t.Errorf("DecodeConfig under the limit = %+v, %v", cfg, err)
	}
	// Past the limit the decoder is reached: a picture on a machine with
	// an AV1 decoder, ErrUnsupported on one without.
	img, _, err := avif.DecodeBytesWithLimits(data, avif.Limits{MaxPixels: 640 * 480})
	if err != nil && !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Errorf("DecodeBytesWithLimits: %v", err)
	}
	if err == nil && img.Rect.Dx() != 640 {
		t.Errorf("DecodeBytesWithLimits decoded %v", img.Rect)
	}
}
