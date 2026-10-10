// Command thumbnails decodes keyframes of an MP4 file with the hardware
// decoder and writes them as JPEG or PNG images:
//
//	go run ./thumbnails -every 10s -width 320 -o thumbs movie.mp4
//	go run ./thumbnails -all movie.mp4            # every keyframe, full size
//
// Only sync samples are sent to the decoder: an IDR (H.264), IRAP (HEVC) or
// AV1 keyframe decodes on its own, so the decoder never sees the frames in
// between and the whole job costs one decode per thumbnail. Frames are
// requested as RGBA, which maps straight onto image.RGBA.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

type options struct {
	every    time.Duration
	all      bool
	width    int
	format   string
	quality  int
	outDir   string
	software bool
}

func main() {
	var o options
	flag.DurationVar(&o.every, "every", 10*time.Second, "one thumbnail per interval, taken from the keyframe at or before each instant")
	flag.BoolVar(&o.all, "all", false, "one thumbnail per keyframe instead of -every")
	flag.IntVar(&o.width, "width", 0, "scale thumbnails to this width (0 = full size)")
	flag.StringVar(&o.format, "format", "jpg", "jpg or png")
	flag.IntVar(&o.quality, "quality", 90, "JPEG quality")
	flag.StringVar(&o.outDir, "o", ".", "output directory")
	flag.BoolVar(&o.software, "software", false, "allow the OS software decoder")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: thumbnails [flags] movie.mp4\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	files, err := run(context.Background(), o, flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "thumbnails:", err)
		os.Exit(1)
	}
	for _, f := range files {
		fmt.Println(f)
	}
}

// pick chooses the sample indexes to decode.
func pick(v *mp4.VideoTrack, o options) []int {
	if o.all {
		return v.Keyframes()
	}
	var out []int
	last := -1
	end := v.TimeOf(v.Duration())
	for t := time.Duration(0); t < end; t += o.every {
		i := v.KeyframeAtOrBefore(t)
		if i >= 0 && i != last {
			out = append(out, i)
			last = i
		}
	}
	return out
}

func run(ctx context.Context, o options, path string) ([]string, error) {
	in, err := mp4.Open(path)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	video := in.Video()
	if video == nil {
		return nil, errors.New("no H.264, HEVC or AV1 video track")
	}
	if err := os.MkdirAll(o.outDir, 0o755); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	// Decode order equals display order for a sequence of keyframes, and
	// asking for it means each frame comes out as soon as it is decoded
	// instead of waiting in the reorder buffer.
	opts := []hwmediacodec.DecoderOption{
		hwmediacodec.WithOutputFormat(hwmediacodec.RGBA),
		hwmediacodec.WithDecodeOrder(),
		hwmediacodec.WithTimeScale(int32(video.TimeScale)),
	}
	if o.software {
		opts = append(opts, hwmediacodec.WithSoftwareFallback())
	}
	dec, err := hwmediacodec.NewDecoder(ctx, video.Codec, opts...)
	if err != nil {
		return nil, err
	}
	defer dec.Close()

	var files []string
	save := func(f *hwmediacodec.Frame) error {
		defer f.Release()
		img, err := f.RGBAImage()
		if err != nil {
			return err
		}
		if o.width > 0 && o.width < f.Width {
			img = downscale(img, o.width)
		}
		at := video.PresentationTime(f.PTS)
		name := filepath.Join(o.outDir, fmt.Sprintf("%s_%s.%s", base, stamp(at), o.format))
		w, err := os.Create(name)
		if err != nil {
			return err
		}
		defer w.Close()
		switch o.format {
		case "png":
			err = png.Encode(w, img)
		default:
			err = jpeg.Encode(w, img, &jpeg.Options{Quality: o.quality})
		}
		if err == nil {
			files = append(files, name)
		}
		return err
	}
	drain := func() error {
		for {
			f, err := dec.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if err := save(f); err != nil {
				return err
			}
		}
	}
	for _, i := range pick(video, o) {
		pkt, err := video.Packet(i)
		if err != nil {
			return nil, err
		}
		for {
			err := dec.Send(ctx, pkt)
			if errors.Is(err, hwmediacodec.ErrAgain) {
				if err := drain(); err != nil {
					return nil, err
				}
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("decode keyframe %d: %w", i, err)
			}
			break
		}
		if err := drain(); err != nil {
			return nil, err
		}
	}
	if err := dec.Flush(ctx); err != nil {
		return nil, err
	}
	if err := drain(); err != nil {
		return nil, err
	}
	return files, nil
}

// stamp formats a position as hh-mm-ss.mmm for a file name.
func stamp(d time.Duration) string {
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d-%02d-%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
