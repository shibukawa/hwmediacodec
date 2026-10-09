package heif_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/examples/heif"
	"github.com/shibukawa/hwmediacodec/examples/internal/testutil"
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
	if err := heif.Encode(&buf, img, o); err != nil {
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
	img, info, err := heif.Decode(data)
	if err != nil {
		t.Fatalf("decode %s: %v", filepath.Base(path), err)
	}
	return img, info
}

func TestEncodeHEICReadBySips(t *testing.T) {
	testutil.RequireHardware(t, hwmediacodec.HEVC, hwmediacodec.Encode)
	if !testutil.HasSips() {
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
			testutil.Sips(t, out, png, "png")
			ref := testutil.LoadPNG(t, png)
			if ref.Bounds().Size() != src.Bounds().Size() {
				t.Fatalf("ImageIO decodes to %v, want %v", ref.Bounds().Size(), src.Bounds().Size())
			}
			if p := testutil.BlockPSNR(src, ref, 4); p < 35 {
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
			if p := testutil.BlockPSNR(got, ref, 1); !odd && p < 40 {
				t.Errorf("our decode is %.1f dB from ImageIO's, want at least 40", p)
			}
			if p := testutil.BlockPSNR(got, want, 1); p < 35 {
				t.Errorf("our decode is %.1f dB from the source, want at least 35", p)
			}
			wantTiles := 1
			if tc.o.TileSize > 0 {
				wantTiles = ((tc.w + tc.o.TileSize - 1) / tc.o.TileSize) * ((tc.h + tc.o.TileSize - 1) / tc.o.TileSize)
			}
			if info.Codec != hwmediacodec.HEVC || info.Tiles != wantTiles || info.Rotation != tc.o.Rotation%360 {
				t.Errorf("info %+v, want HEVC, %d tiles, rotation %d", info, wantTiles, tc.o.Rotation)
			}
			// ffmpeg reads single pictures too and, unlike ImageIO, applies
			// irot (it becomes a display matrix that autorotate honours),
			// which checks the rotation direction against a second reader.
			// Its clap handling rounds odd sizes to even, so only even
			// pictures are compared.
			if tc.o.TileSize == 0 && !odd {
				testutil.RequireFFmpeg(t)
				fpng := testutil.FFmpegImage(t, out, filepath.Join(dir, tc.name+"_ffmpeg.png"))
				fref := testutil.LoadPNG(t, fpng)
				if fref.Bounds().Size() != want.Bounds().Size() {
					t.Fatalf("ffmpeg decodes to %v, want %v", fref.Bounds().Size(), want.Bounds().Size())
				}
				if p := testutil.BlockPSNR(want, fref, 4); p < 35 {
					t.Errorf("ffmpeg's decode is %.1f dB from the (rotated) source", p)
				}
			}
		})
	}
}

func TestDecodeHEICFromSips(t *testing.T) {
	testutil.RequireHardware(t, hwmediacodec.HEVC, hwmediacodec.Decode)
	if !testutil.HasSips() {
		t.Skip("sips (macOS ImageIO) not available")
	}
	dir := t.TempDir()
	src := testImage(800, 600)
	srcPNG := filepath.Join(dir, "src.png")
	testutil.SavePNG(t, srcPNG, src)
	heic := filepath.Join(dir, "sips.heic")
	testutil.Sips(t, srcPNG, heic, "heic")

	got, info := decodeFile(t, heic)
	if info.Codec != hwmediacodec.HEVC || got.Bounds().Dx() != 800 || got.Bounds().Dy() != 600 {
		t.Fatalf("decoded %v %+v", got.Bounds(), info)
	}
	// Compare with ImageIO's own decode of its file, which isolates our
	// decoding from the encoder's loss.
	refPNG := filepath.Join(dir, "sips_dec.png")
	testutil.Sips(t, heic, refPNG, "png")
	if p := testutil.BlockPSNR(got, testutil.LoadPNG(t, refPNG), 4); p < 40 {
		t.Errorf("%.1f dB from ImageIO's decode, want at least 40", p)
	}
	if p := testutil.BlockPSNR(got, src, 4); p < 35 {
		t.Errorf("%.1f dB from the source, want at least 35", p)
	}
}

func TestDecodeAVIFFromFFmpeg(t *testing.T) {
	testutil.RequireFFmpeg(t)
	testutil.RequireHardware(t, hwmediacodec.AV1, hwmediacodec.Decode)
	if !testutil.HasEncoder(t, "libsvtav1") {
		t.Skip("ffmpeg has no libsvtav1")
	}
	dir := t.TempDir()
	src := testImage(640, 480)
	srcPNG := filepath.Join(dir, "src.png")
	testutil.SavePNG(t, srcPNG, src)
	avif := testutil.FFmpegImage(t, srcPNG, filepath.Join(dir, "ffmpeg.avif"), "-c:v", "libsvtav1", "-pix_fmt", "yuv420p", "-f", "avif")

	got, info := decodeFile(t, avif)
	if info.Codec != hwmediacodec.AV1 || info.Tiles != 1 || got.Bounds().Dx() != 640 || got.Bounds().Dy() != 480 {
		t.Fatalf("decoded %v %+v", got.Bounds(), info)
	}
	ref := testutil.LoadPNG(t, testutil.FFmpegImage(t, avif, filepath.Join(dir, "ffmpeg_dec.png")))
	if p := testutil.BlockPSNR(got, ref, 4); p < 40 {
		t.Errorf("%.1f dB from ffmpeg's decode, want at least 40", p)
	}
	if p := testutil.BlockPSNR(got, src, 4); p < 30 {
		t.Errorf("%.1f dB from the source, want at least 30", p)
	}
}

func TestEncodeAVIFNeedsAnEncoder(t *testing.T) {
	var buf bytes.Buffer
	err := heif.Encode(&buf, testImage(64, 64), heif.Options{Codec: hwmediacodec.AV1})
	if err == nil {
		// A platform with a hardware AV1 encoder: the file must round-trip.
		img, info, derr := heif.Decode(buf.Bytes())
		if derr != nil || info.Codec != hwmediacodec.AV1 || img.Bounds().Dx() != 64 {
			t.Fatalf("AVIF round trip: %v %+v", derr, info)
		}
		return
	}
	if !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Fatalf("want ErrUnsupported without an AV1 encoder, got %v", err)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for _, in := range [][]byte{nil, []byte("not a heif file at all, just text"), bytes.Repeat([]byte{0}, 64)} {
		if _, _, err := heif.Decode(in); err == nil {
			t.Errorf("decoding %q succeeded", in)
		}
	}
}
