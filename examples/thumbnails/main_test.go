package main

import (
	"context"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/mediatest"
)

func loadPNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// blockPSNR compares two images after averaging 8x8 blocks, which hides the
// chroma interpolation differences between VideoToolbox and ffmpeg.
func blockPSNR(a, b image.Image) float64 {
	w, h := a.Bounds().Dx(), a.Bounds().Dy()
	var se, n float64
	for by := 0; by+8 <= h; by += 8 {
		for bx := 0; bx+8 <= w; bx += 8 {
			var ma, mb [3]float64
			for y := by; y < by+8; y++ {
				for x := bx; x < bx+8; x++ {
					ra, ga, ba, _ := a.At(x, y).RGBA()
					rb, gb, bb, _ := b.At(x, y).RGBA()
					ma[0] += float64(ra >> 8)
					ma[1] += float64(ga >> 8)
					ma[2] += float64(ba >> 8)
					mb[0] += float64(rb >> 8)
					mb[1] += float64(gb >> 8)
					mb[2] += float64(bb >> 8)
				}
			}
			for c := range ma {
				d := (ma[c] - mb[c]) / 64
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

func TestThumbnails(t *testing.T) {
	mediatest.RequireFFmpeg(t)
	mediatest.RequireHardware(t, hwmediacodec.H264, hwmediacodec.Decode)
	dir := t.TempDir()
	// 90 frames at 30 fps with a keyframe every 30 frames: keyframes at 0 s,
	// 1 s and 2 s.
	src := mediatest.GenerateMP4(t, dir, mediatest.MP4Options{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 90, BFrames: 2, GOP: 30})

	t.Run("all", func(t *testing.T) {
		out := filepath.Join(dir, "all")
		files, err := run(context.Background(), options{all: true, format: "png", outDir: out}, src)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 3 {
			t.Fatalf("%d thumbnails, want 3: %v", len(files), files)
		}
		for i, name := range []string{"00-00-00.000", "00-00-01.000", "00-00-02.000"} {
			if filepath.Base(files[i]) != "test_h264_160x120_90_b2_"+name+".png" {
				t.Errorf("file %d is %s", i, filepath.Base(files[i]))
			}
			got := loadPNG(t, files[i])
			if b := got.Bounds(); b.Dx() != 160 || b.Dy() != 120 {
				t.Errorf("thumbnail %d is %v", i, b)
			}
			want := loadPNG(t, mediatest.ExtractFrame(t, src, i*30, dir))
			if p := blockPSNR(got, want); p < 30 {
				t.Errorf("thumbnail %d: %.1f dB against ffmpeg's frame, want at least 30", i, p)
			}
		}
	})
	t.Run("every", func(t *testing.T) {
		files, err := run(context.Background(), options{every: 1500 * time.Millisecond, format: "jpg", quality: 90, outDir: filepath.Join(dir, "every")}, src)
		if err != nil {
			t.Fatal(err)
		}
		// Instants 0 s and 1.5 s map to the keyframes at 0 s and 1 s; 3 s is
		// past the end.
		if len(files) != 2 {
			t.Fatalf("%d thumbnails, want 2: %v", len(files), files)
		}
	})
	t.Run("width", func(t *testing.T) {
		files, err := run(context.Background(), options{all: true, width: 80, format: "png", outDir: filepath.Join(dir, "small")}, src)
		if err != nil {
			t.Fatal(err)
		}
		if b := loadPNG(t, files[0]).Bounds(); b.Dx() != 80 || b.Dy() != 60 {
			t.Errorf("scaled thumbnail is %v, want 80x60", b)
		}
	})
}
