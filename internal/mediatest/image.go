package mediatest

import (
	"image"
	"image/png"
	"math"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// LoadPNG decodes a PNG file.
func LoadPNG(t testing.TB, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return img
}

// SavePNG writes img as a PNG file.
func SavePNG(t testing.TB, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// BlockPSNR compares two images of the same size after averaging block x
// block pixel blocks (block 1 compares pixels), which hides the chroma
// interpolation differences between converters. It returns +Inf for
// identical images and -1 when the sizes differ.
func BlockPSNR(a, b image.Image, block int) float64 {
	if a.Bounds().Size() != b.Bounds().Size() {
		return -1
	}
	w, h := a.Bounds().Dx(), a.Bounds().Dy()
	ax, ay := a.Bounds().Min.X, a.Bounds().Min.Y
	bx, by := b.Bounds().Min.X, b.Bounds().Min.Y
	var se, n float64
	for y0 := 0; y0+block <= h; y0 += block {
		for x0 := 0; x0+block <= w; x0 += block {
			var ma, mb [3]float64
			for y := y0; y < y0+block; y++ {
				for x := x0; x < x0+block; x++ {
					r1, g1, b1, _ := a.At(ax+x, ay+y).RGBA()
					r2, g2, b2, _ := b.At(bx+x, by+y).RGBA()
					ma[0] += float64(r1 >> 8)
					ma[1] += float64(g1 >> 8)
					ma[2] += float64(b1 >> 8)
					mb[0] += float64(r2 >> 8)
					mb[1] += float64(g2 >> 8)
					mb[2] += float64(b2 >> 8)
				}
			}
			for c := range ma {
				d := (ma[c] - mb[c]) / float64(block*block)
				se += d * d
				n++
			}
		}
	}
	if se == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(se/n))
}

// HasSips reports whether macOS's sips (ImageIO) is available.
func HasSips() bool {
	_, err := exec.LookPath("sips")
	return err == nil
}

// Sips converts in to out in the given format (png, heic, jpeg, ...) with
// macOS's ImageIO, the reference for HEIC files.
func Sips(t testing.TB, in, out, format string) {
	t.Helper()
	o, err := exec.Command("sips", "-s", "format", format, in, "--out", out).CombinedOutput()
	if err != nil || strings.Contains(string(o), "Error") {
		t.Fatalf("sips %s -> %s (%s): %v\n%s", in, out, format, err, o)
	}
}

// FFmpegImage converts an image file with ffmpeg (for example AVIF to
// PNG), returning out.
func FFmpegImage(t testing.TB, in, out string, args ...string) string {
	t.Helper()
	all := append([]string{"-hide_banner", "-loglevel", "error", "-y", "-i", in}, args...)
	all = append(all, out)
	if o, err := exec.Command("ffmpeg", all...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", all, err, o)
	}
	return out
}
