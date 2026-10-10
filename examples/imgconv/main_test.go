package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/image/avif"
	"github.com/shibukawa/hwmediacodec/image/heif"
)

// testImage is a smooth picture with flat blocks and a diagonal, so that
// codec loss stays small and a rotation mistake is obvious.
func testImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{R: uint8(40 + 160*x/w), G: uint8(60 + 140*y/h), B: uint8(200 - 120*x/w), A: 255}
			if x < w/4 && y < h/4 {
				c = color.RGBA{R: 220, G: 40, B: 40, A: 255} // red: top left
			}
			if x > 3*w/4 && y > 3*h/4 {
				c = color.RGBA{R: 40, G: 200, B: 60, A: 255} // green: bottom right
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// psnr compares the colour channels after averaging 4x4 blocks, which
// removes the differences between chroma upsampling methods.
func psnr(a, b image.Image) float64 {
	ab, bb := a.Bounds(), b.Bounds()
	if ab.Dx() != bb.Dx() || ab.Dy() != bb.Dy() {
		return 0
	}
	const block = 4
	var se, n float64
	for y := 0; y+block <= ab.Dy(); y += block {
		for x := 0; x+block <= ab.Dx(); x += block {
			var sa, sb [3]float64
			for j := 0; j < block; j++ {
				for i := 0; i < block; i++ {
					r1, g1, b1, _ := a.At(ab.Min.X+x+i, ab.Min.Y+y+j).RGBA()
					r2, g2, b2, _ := b.At(bb.Min.X+x+i, bb.Min.Y+y+j).RGBA()
					sa[0], sa[1], sa[2] = sa[0]+float64(r1>>8), sa[1]+float64(g1>>8), sa[2]+float64(b1>>8)
					sb[0], sb[1], sb[2] = sb[0]+float64(r2>>8), sb[1]+float64(g2>>8), sb[2]+float64(b2>>8)
				}
			}
			for c := range sa {
				d := (sa[c] - sb[c]) / (block * block)
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

func hasHardware(t *testing.T, c hwmediacodec.Codec, dir hwmediacodec.Direction) bool {
	t.Helper()
	return hwmediacodec.HasHardware(context.Background(), c, dir)
}

func codecOf(format string) hwmediacodec.Codec {
	if format == formatAVIF {
		return hwmediacodec.AV1
	}
	return hwmediacodec.HEVC
}

// TestFallbackRoundTrip runs both formats through the cgo-free codecs
// alone, which is what a machine without hardware codecs gets, and then
// lets the hardware decoder read the same files where there is one.
func TestFallbackRoundTrip(t *testing.T) {
	src := testImage(320, 240)
	for format, wantUsed := range map[string]string{formatHEIC: usedGoHEIC, formatAVIF: usedGoAVIF} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			used, err := encodeHEIF(&buf, src, format, encodeOptions{quality: 0.8}, engineGo)
			if err != nil {
				t.Fatal(err)
			}
			if used != wantUsed {
				t.Errorf("encoded by %q, want %q", used, wantUsed)
			}
			if got, err := formatOf(buf.Bytes()); err != nil || got != format {
				t.Fatalf("formatOf: %q %v, want %q", got, err, format)
			}
			img, used, err := decodeHEIF(buf.Bytes(), engineGo, false)
			if err != nil {
				t.Fatal(err)
			}
			if used != wantUsed {
				t.Errorf("decoded by %q, want %q", used, wantUsed)
			}
			if p := psnr(src, img); p < 32 {
				t.Errorf("fallback round trip is %.1f dB from the source, want at least 32", p)
			}
			if !hasHardware(t, codecOf(format), hwmediacodec.Decode) {
				t.Logf("no hardware %s decoder: interoperability not checked", format)
				return
			}
			hw, used, err := decodeHEIF(buf.Bytes(), engineHardware, false)
			if err != nil {
				t.Fatalf("the hardware decoder rejects the fallback encoder's file: %v", err)
			}
			if used != usedHardware {
				t.Errorf("decoded by %q, want hardware", used)
			}
			if p := psnr(src, hw); p < 32 {
				t.Errorf("hardware decode of the fallback file is %.1f dB from the source, want at least 32", p)
			}
		})
	}
}

// TestAutoPrefersHardware checks the choice the default engine makes for
// each format and direction against what Probe reports, and that forcing
// the hardware where there is none is an ErrUnsupported.
func TestAutoPrefersHardware(t *testing.T) {
	src := testImage(320, 240)
	for format, goUsed := range map[string]string{formatHEIC: usedGoHEIC, formatAVIF: usedGoAVIF} {
		t.Run(format, func(t *testing.T) {
			want := goUsed
			hwEncode := hasHardware(t, codecOf(format), hwmediacodec.Encode)
			if hwEncode {
				want = usedHardware
			}
			var buf bytes.Buffer
			used, err := encodeHEIF(&buf, src, format, encodeOptions{quality: 0.8}, engineAuto)
			if err != nil {
				t.Fatal(err)
			}
			if used != want {
				t.Errorf("auto encoded with %q, want %q", used, want)
			}
			if !hwEncode {
				var none bytes.Buffer
				if _, err := encodeHEIF(&none, src, format, encodeOptions{}, engineHardware); !errors.Is(err, hwmediacodec.ErrUnsupported) || none.Len() != 0 {
					t.Errorf("forcing the hardware encoder: %v with %d bytes written, want ErrUnsupported and nothing", err, none.Len())
				}
			}

			want = goUsed
			if hasHardware(t, codecOf(format), hwmediacodec.Decode) {
				want = usedHardware
			}
			img, used, err := decodeHEIF(buf.Bytes(), engineAuto, false)
			if err != nil {
				t.Fatal(err)
			}
			if used != want {
				t.Errorf("auto decoded with %q, want %q", used, want)
			}
			if p := psnr(src, img); p < 32 {
				t.Errorf("%.1f dB from the source, want at least 32", p)
			}
		})
	}
}

// TestFallbackReadsHardwareFiles gives the fallback decoders what the
// hardware encoder wrote, a rotated grid included: both sides must agree
// on the picture, its size and the direction of the rotation.
func TestFallbackReadsHardwareFiles(t *testing.T) {
	if !hasHardware(t, hwmediacodec.HEVC, hwmediacodec.Encode) {
		t.Skip("no hardware HEVC encoder")
	}
	src := testImage(500, 300)
	for _, o := range []encodeOptions{{quality: 0.8}, {quality: 0.8, tile: 128, rotation: 90}} {
		var buf bytes.Buffer
		if used, err := encodeHEIF(&buf, src, formatHEIC, o, engineHardware); err != nil || used != usedHardware {
			t.Fatalf("hardware encode: %q %v", used, err)
		}
		want := rotateCCW(src, o.rotation)
		img, used, err := decodeHEIF(buf.Bytes(), engineGo, false)
		if err != nil {
			t.Fatalf("the fallback decoder rejects the hardware encoder's file (%+v): %v", o, err)
		}
		if used != usedGoHEIC {
			t.Errorf("decoded by %q", used)
		}
		if img.Bounds().Size() != want.Bounds().Size() {
			t.Fatalf("fallback decode of %+v is %v, want %v", o, img.Bounds().Size(), want.Bounds().Size())
		}
		if p := psnr(want, img); p < 32 {
			t.Errorf("fallback decode of %+v is %.1f dB from the source, want at least 32", o, p)
		}
	}
}

// TestFallbackBakesRotation: the fallback encoders store no irot, so the
// rotation goes into the pixels and the displayed picture is the same as
// on the hardware path.
func TestFallbackBakesRotation(t *testing.T) {
	src := testImage(320, 240)
	var buf bytes.Buffer
	if _, err := encodeHEIF(&buf, src, formatHEIC, encodeOptions{quality: 0.8, rotation: 270}, engineGo); err != nil {
		t.Fatal(err)
	}
	info, err := heif.DecodeInfo(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 240 || info.Height != 320 || info.Rotation != 0 {
		t.Errorf("file is %dx%d with rotation %d, want 240x320 and none", info.Width, info.Height, info.Rotation)
	}
	img, _, err := decodeHEIF(buf.Bytes(), engineGo, false)
	if err != nil {
		t.Fatal(err)
	}
	if p := psnr(rotateCCW(src, 270), img); p < 32 {
		t.Errorf("%.1f dB from the rotated source, want at least 32", p)
	}
}

func TestFormatOf(t *testing.T) {
	if _, err := formatOf([]byte("not a heif file at all, just text")); err == nil {
		t.Error("text passes for a HEIF file")
	}
	if _, _, err := decodeHEIF(nil, engineAuto, false); err == nil {
		t.Error("an empty file decodes")
	}
	// The packages of the core module agree with the answer.
	var buf bytes.Buffer
	if _, err := encodeHEIF(&buf, testImage(64, 64), formatAVIF, encodeOptions{}, engineGo); err != nil {
		t.Fatal(err)
	}
	if _, err := avif.DecodeInfo(buf.Bytes()); err != nil {
		t.Errorf("image/avif does not describe the fallback encoder's file: %v", err)
	}
	if _, err := heif.DecodeInfo(buf.Bytes()); err == nil {
		t.Error("image/heif accepts an AVIF file")
	}
}
