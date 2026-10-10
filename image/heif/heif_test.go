package heif_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/image/heif"
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

func toRGBA(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return r
	}
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Rect, src, b.Min, draw.Src)
	return out
}

func rotateCCW(src image.Image, angle int) *image.RGBA {
	img := toRGBA(src)
	w, h := img.Rect.Dx(), img.Rect.Dy()
	var out *image.RGBA
	switch angle {
	case 90:
		out = image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				out.SetRGBA(y, w-1-x, img.RGBAAt(x, y))
			}
		}
	case 180:
		out = image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				out.SetRGBA(w-1-x, h-1-y, img.RGBAAt(x, y))
			}
		}
	case 270:
		out = image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				out.SetRGBA(h-1-y, x, img.RGBAAt(x, y))
			}
		}
	default:
		return img
	}
	return out
}

func encodeFile(t *testing.T, path string, img image.Image, o heif.Options) {
	t.Helper()
	var buf bytes.Buffer
	if err := heif.Encode(&buf, img, &o); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func decodeFile(t *testing.T, path string) (*image.RGBA, *heif.Info) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, info, err := heif.DecodeBytes(data)
	if err != nil {
		t.Fatalf("decode %s: %v", filepath.Base(path), err)
	}
	checkImagePackage(t, data, img, info)
	return img, info
}

// checkImagePackage reads the same file the way a program that only knows
// the image package would: the format is recognised from the file header,
// DecodeConfig and DecodeInfo predict what DecodeBytes returned without
// decoding, and image.Decode returns the same pixels.
func checkImagePackage(t *testing.T, data []byte, want *image.RGBA, wantInfo *heif.Info) {
	t.Helper()
	info, err := heif.DecodeInfo(data)
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
	if format != "heic" {
		t.Errorf("image.DecodeConfig format %q, want heic", format)
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

func TestEncodeHEICReadBySips(t *testing.T) {
	mediatest.RequireHardware(t, hwmediacodec.HEVC, hwmediacodec.Encode)
	if !mediatest.HasSips() {
		t.Skip("sips (macOS ImageIO) not available")
	}
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		w, h int
		o    heif.Options
	}{
		{"single", 640, 480, heif.Options{Quality: 0.8}},
		{"odd_size", 321, 203, heif.Options{Quality: 0.8}},
		{"grid", 640, 480, heif.Options{Quality: 0.8, TileSize: 256}},
		{"rotated", 640, 480, heif.Options{Quality: 0.8, Rotation: 90}},
		{"grid_rotated", 500, 300, heif.Options{Quality: 0.8, TileSize: 128, Rotation: 270}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := testImage(tc.w, tc.h)
			out := filepath.Join(dir, tc.name+".heic")
			encodeFile(t, out, src, tc.o)
			want := rotateCCW(src, tc.o.Rotation)

			// ImageIO is the reference for the coded picture: it must open
			// the file and agree on size and content. It keeps irot as
			// orientation metadata rather than rotating the pixels, so its
			// output is the stored, unrotated picture.
			png := filepath.Join(dir, tc.name+"_sips.png")
			mediatest.Sips(t, out, png, "png")
			ref := mediatest.LoadPNG(t, png)
			if ref.Bounds().Size() != src.Bounds().Size() {
				t.Fatalf("ImageIO decodes to %v, want %v", ref.Bounds().Size(), src.Bounds().Size())
			}
			if p := mediatest.BlockPSNR(src, ref, 4); p < 35 {
				t.Errorf("ImageIO's decode is %.1f dB from the source, want at least 35", p)
			}
			ref = rotateCCW(ref, tc.o.Rotation)
			// Our own decoder agrees with ImageIO bit for bit on even
			// pictures. Odd pictures are padded and cropped through clap;
			// ImageIO resamples those instead of cropping (it lands 1 dB
			// further from the source than our crop does), so they are
			// checked against the source.
			got, info := decodeFile(t, out)
			if got.Bounds().Size() != want.Bounds().Size() {
				t.Fatalf("decoded %v, want %v", got.Bounds().Size(), want.Bounds().Size())
			}
			odd := tc.w%2 == 1 || tc.h%2 == 1
			if p := mediatest.BlockPSNR(got, ref, 1); !odd && p < 40 {
				t.Errorf("our decode is %.1f dB from ImageIO's, want at least 40", p)
			}
			if p := mediatest.BlockPSNR(got, want, 1); p < 35 {
				t.Errorf("our decode is %.1f dB from the source, want at least 35", p)
			}
			wantTiles := 1
			if tc.o.TileSize > 0 {
				wantTiles = ((tc.w + tc.o.TileSize - 1) / tc.o.TileSize) * ((tc.h + tc.o.TileSize - 1) / tc.o.TileSize)
			}
			if info.Tiles != wantTiles || info.Rotation != tc.o.Rotation%360 {
				t.Errorf("info %+v, want %d tiles, rotation %d", info, wantTiles, tc.o.Rotation)
			}
			// ffmpeg reads single pictures too and, unlike ImageIO, applies
			// irot (it becomes a display matrix that autorotate honours),
			// which checks the rotation direction against a second reader.
			// Its clap handling rounds odd sizes to even, so only even
			// pictures are compared.
			if tc.o.TileSize == 0 && !odd {
				mediatest.RequireFFmpeg(t)
				fpng := mediatest.FFmpegImage(t, out, filepath.Join(dir, tc.name+"_ffmpeg.png"))
				fref := mediatest.LoadPNG(t, fpng)
				if fref.Bounds().Size() != want.Bounds().Size() {
					t.Fatalf("ffmpeg decodes to %v, want %v", fref.Bounds().Size(), want.Bounds().Size())
				}
				if p := mediatest.BlockPSNR(want, fref, 4); p < 35 {
					t.Errorf("ffmpeg's decode is %.1f dB from the (rotated) source", p)
				}
			}
		})
	}
}

func TestDecodeHEICFromSips(t *testing.T) {
	mediatest.RequireHardware(t, hwmediacodec.HEVC, hwmediacodec.Decode)
	if !mediatest.HasSips() {
		t.Skip("sips (macOS ImageIO) not available")
	}
	dir := t.TempDir()
	src := testImage(800, 600)
	srcPNG := filepath.Join(dir, "src.png")
	mediatest.SavePNG(t, srcPNG, src)
	heic := filepath.Join(dir, "sips.heic")
	mediatest.Sips(t, srcPNG, heic, "heic")

	got, info := decodeFile(t, heic)
	if info.Tiles < 1 || got.Bounds().Dx() != 800 || got.Bounds().Dy() != 600 {
		t.Fatalf("decoded %v %+v", got.Bounds(), info)
	}
	// Compare with ImageIO's own decode of its file, which isolates our
	// decoding from the encoder's loss.
	refPNG := filepath.Join(dir, "sips_dec.png")
	mediatest.Sips(t, heic, refPNG, "png")
	if p := mediatest.BlockPSNR(got, mediatest.LoadPNG(t, refPNG), 4); p < 40 {
		t.Errorf("%.1f dB from ImageIO's decode, want at least 40", p)
	}
	if p := mediatest.BlockPSNR(got, src, 4); p < 35 {
		t.Errorf("%.1f dB from the source, want at least 35", p)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for _, in := range [][]byte{nil, []byte("not a heif file at all, just text"), bytes.Repeat([]byte{0}, 64)} {
		if _, _, err := heif.DecodeBytes(in); err == nil {
			t.Errorf("decoding %q succeeded", in)
		}
		if _, err := heif.Decode(bytes.NewReader(in)); err == nil {
			t.Errorf("Decode of %q succeeded", in)
		}
		if _, err := heif.DecodeConfig(bytes.NewReader(in)); err == nil {
			t.Errorf("DecodeConfig of %q succeeded", in)
		}
		if _, _, err := image.Decode(bytes.NewReader(in)); err == nil {
			t.Errorf("image.Decode of %q succeeded", in)
		}
	}
}

// TestEncodeDefaults writes with nil options, as jpeg.Encode allows, and
// reads the file back through the image package alone.
func TestEncodeDefaults(t *testing.T) {
	mediatest.RequireHardware(t, hwmediacodec.HEVC, hwmediacodec.Encode)
	mediatest.RequireHardware(t, hwmediacodec.HEVC, hwmediacodec.Decode)
	src := testImage(320, 240)
	var buf bytes.Buffer
	if err := heif.Encode(&buf, src, nil); err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(buf.Bytes()))
	if err != nil || format != "heic" || cfg.Width != 320 || cfg.Height != 240 {
		t.Fatalf("image.DecodeConfig: %q %dx%d %v", format, cfg.Width, cfg.Height, err)
	}
	img, format, err := image.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil || format != "heic" {
		t.Fatalf("image.Decode: %q %v", format, err)
	}
	if p := mediatest.BlockPSNR(img, src, 4); p < 30 {
		t.Errorf("%.1f dB from the source, want at least 30", p)
	}
}

// TestDecodeConfigNeedsNoDecoder checks the structure-only path: the size
// after rotation, the tile count and the codec of a rotated grid come from
// the boxes, and only an encoder is needed to make the file.
func TestDecodeConfigNeedsNoDecoder(t *testing.T) {
	mediatest.RequireHardware(t, hwmediacodec.HEVC, hwmediacodec.Encode)
	var buf bytes.Buffer
	if err := heif.Encode(&buf, testImage(500, 300), &heif.Options{TileSize: 128, Rotation: 90}); err != nil {
		t.Fatal(err)
	}
	info, err := heif.DecodeInfo(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	want := heif.Info{Width: 300, Height: 500, Tiles: 12, Rotation: 90}
	if *info != want {
		t.Errorf("DecodeInfo %+v, want %+v", *info, want)
	}
}

// TestGenericBrand rewrites the major brand of a HEIC file to mif1, which
// does not name the codec: the image package then reports "heif" and the
// picture is decoded all the same.
func TestGenericBrand(t *testing.T) {
	mediatest.RequireHardware(t, hwmediacodec.HEVC, hwmediacodec.Encode)
	mediatest.RequireHardware(t, hwmediacodec.HEVC, hwmediacodec.Decode)
	var buf bytes.Buffer
	if err := heif.Encode(&buf, testImage(320, 240), nil); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	want, _, err := heif.DecodeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	copy(data[8:12], "mif1")
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "heif" || cfg.Width != 320 || cfg.Height != 240 {
		t.Fatalf("image.DecodeConfig: %q %dx%d %v", format, cfg.Width, cfg.Height, err)
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != "heif" {
		t.Fatalf("image.Decode: %q %v", format, err)
	}
	if rgba, ok := img.(*image.RGBA); !ok || !bytes.Equal(rgba.Pix, want.Pix) {
		t.Errorf("the mif1 file decodes to %T, which differs from the heic one", img)
	}
}

// TestRejectsAVIF feeds an AVIF file to this package, which only reads
// HEVC pictures, and to image.Decode in a program that does not link
// image/avif: both say where AVIF is handled.
func TestRejectsAVIF(t *testing.T) {
	mediatest.RequireFFmpeg(t)
	if !mediatest.HasEncoder(t, "libsvtav1") {
		t.Skip("ffmpeg has no libsvtav1")
	}
	dir := t.TempDir()
	srcPNG := filepath.Join(dir, "src.png")
	mediatest.SavePNG(t, srcPNG, testImage(320, 240))
	path := mediatest.FFmpegImage(t, srcPNG, filepath.Join(dir, "ffmpeg.avif"), "-c:v", "libsvtav1", "-pix_fmt", "yuv420p", "-f", "avif")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"DecodeInfo":   second(heif.DecodeInfo(data)),
		"DecodeConfig": second(heif.DecodeConfig(bytes.NewReader(data))),
		"Decode":       second(heif.Decode(bytes.NewReader(data))),
	} {
		if err == nil || !strings.Contains(err.Error(), "image/avif") {
			t.Errorf("%s of an AVIF file: %v, want an error naming image/avif", name, err)
		}
	}
	// With its own brand nothing in this test binary claims the file.
	if _, _, err := image.Decode(bytes.NewReader(data)); !errors.Is(err, image.ErrFormat) {
		t.Errorf("image.Decode of an avif-branded file: %v, want image.ErrFormat", err)
	}
	// As mif1 the generic entry takes it and reports the missing package.
	copy(data[8:12], "mif1")
	if _, _, err := image.Decode(bytes.NewReader(data)); err == nil || !strings.Contains(err.Error(), "image/avif") {
		t.Errorf("image.Decode of a mif1 file with AV1 pictures: %v, want an error naming image/avif", err)
	}
	// The structure is readable without the codec's package.
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || format != "heif" || cfg.Width != 320 {
		t.Errorf("image.DecodeConfig of the mif1 file: %q %dx%d %v", format, cfg.Width, cfg.Height, err)
	}
}

func second[T any](_ T, err error) error { return err }
