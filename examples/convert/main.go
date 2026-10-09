// Command convert transcodes the video track of an MP4 file with the
// hardware codecs (for example H.264 to HEVC) and copies every other track
// unchanged:
//
//	go run ./convert -codec hevc -bitrate 6M input.mp4 output.mp4
//	go run ./convert -codec h264 -quality 0.7 -bframes input.mp4 output.mp4
//
// The container work is done by the mediacontainer/mp4 package; the
// codec work is the decoder/encoder loop below, which is the whole point of
// the sample. Timestamps are carried through unchanged: the decoder is told
// to use the track's time scale, so frame PTS values are the MP4 sample
// times, and the encoder's packets come back with PTS and DTS in that same
// scale, ready for the muxer.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

type options struct {
	codec    hwmediacodec.Codec
	bitrate  int
	quality  float64
	cbr      bool
	gop      int
	bframes  bool
	profile  hwmediacodec.Profile
	software bool
	quiet    bool
}

func main() {
	var o options
	codecName := flag.String("codec", "hevc", "output codec: h264 or hevc")
	bitrate := flag.String("bitrate", "", "target bitrate, e.g. 6M or 2500k (VBR unless -cbr)")
	flag.Float64Var(&o.quality, "quality", 0, "constant quality in (0, 1] instead of a bitrate")
	flag.BoolVar(&o.cbr, "cbr", false, "constant bitrate")
	flag.IntVar(&o.gop, "gop", 0, "keyframe interval in frames (0 = encoder default)")
	flag.BoolVar(&o.bframes, "bframes", false, "allow B-frames")
	profile := flag.String("profile", "", "coding profile: baseline, main or high")
	flag.BoolVar(&o.software, "software", false, "allow the OS software codec when there is no hardware engine")
	flag.BoolVar(&o.quiet, "q", false, "no progress output")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: convert [flags] input.mp4 output.mp4\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}
	var err error
	if o.codec, err = parseCodec(*codecName); err != nil {
		fatal(err)
	}
	if o.bitrate, err = parseBitrate(*bitrate); err != nil {
		fatal(err)
	}
	if o.profile, err = parseProfile(*profile); err != nil {
		fatal(err)
	}
	progress := io.Writer(os.Stderr)
	if o.quiet {
		progress = io.Discard
	}
	if err := run(context.Background(), o, flag.Arg(0), flag.Arg(1), progress); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "convert:", err)
	os.Exit(1)
}

func parseCodec(s string) (hwmediacodec.Codec, error) {
	switch strings.ToLower(s) {
	case "h264", "avc":
		return hwmediacodec.H264, nil
	case "hevc", "h265":
		return hwmediacodec.HEVC, nil
	}
	return 0, fmt.Errorf("unknown codec %q", s)
}

func parseBitrate(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	mult := 1
	switch {
	case strings.HasSuffix(s, "M"), strings.HasSuffix(s, "m"):
		mult, s = 1_000_000, s[:len(s)-1]
	case strings.HasSuffix(s, "k"), strings.HasSuffix(s, "K"):
		mult, s = 1_000, s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("bad bitrate %q", s)
	}
	return int(v * float64(mult)), nil
}

func parseProfile(s string) (hwmediacodec.Profile, error) {
	switch strings.ToLower(s) {
	case "":
		return hwmediacodec.ProfileDefault, nil
	case "baseline":
		return hwmediacodec.ProfileBaseline, nil
	case "main":
		return hwmediacodec.ProfileMain, nil
	case "high":
		return hwmediacodec.ProfileHigh, nil
	}
	return 0, fmt.Errorf("unknown profile %q", s)
}

func (o options) encoderOptions(timeScale uint32, fps float64) []hwmediacodec.EncoderOption {
	opts := []hwmediacodec.EncoderOption{
		hwmediacodec.WithTimeScale(int32(timeScale)),
		hwmediacodec.WithFrameRate(fps),
	}
	if o.bitrate > 0 {
		opts = append(opts, hwmediacodec.WithBitrate(o.bitrate))
		if o.cbr {
			opts = append(opts, hwmediacodec.WithRateControl(hwmediacodec.CBR))
		}
	}
	if o.quality > 0 {
		opts = append(opts, hwmediacodec.WithQuality(o.quality))
	}
	if o.gop > 0 {
		opts = append(opts, hwmediacodec.WithKeyframeInterval(o.gop))
	}
	if o.bframes {
		opts = append(opts, hwmediacodec.WithBFrames())
	}
	if o.profile != hwmediacodec.ProfileDefault {
		opts = append(opts, hwmediacodec.WithProfile(o.profile))
	}
	if o.software {
		opts = append(opts, hwmediacodec.WithSoftwareFallback())
	}
	return opts
}

// passthrough copies the samples of one non-video track, interleaved with
// the video by time.
type passthrough struct {
	src  *mp4.Track
	dst  *mp4.TrackWriter
	next int
}

func (p *passthrough) copyUpTo(t time.Duration) error {
	for p.next < p.src.SampleCount() {
		dts, _, _ := p.src.Info(p.next)
		if t >= 0 && p.src.TimeOf(dts) > t {
			return nil
		}
		s, err := p.src.Sample(p.next)
		if err != nil {
			return err
		}
		if err := p.dst.WriteSample(s); err != nil {
			return err
		}
		p.next++
	}
	return nil
}

func run(ctx context.Context, o options, inPath, outPath string, progress io.Writer) error {
	in, err := mp4.Open(inPath)
	if err != nil {
		return err
	}
	defer in.Close()
	video := in.Video()
	if video == nil {
		return errors.New("input has no H.264, HEVC or AV1 video track")
	}

	out, err := mp4.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()
	vw, err := out.AddVideoTrack(o.codec, video.TimeScale)
	if err != nil {
		return err
	}
	var others []*passthrough
	for _, t := range in.Others() {
		others = append(others, &passthrough{src: t, dst: out.AddPassthroughTrack(t)})
	}

	decOpts := []hwmediacodec.DecoderOption{hwmediacodec.WithTimeScale(int32(video.TimeScale))}
	if o.software {
		decOpts = append(decOpts, hwmediacodec.WithSoftwareFallback())
	}
	dec, err := hwmediacodec.NewDecoder(ctx, video.Codec, decOpts...)
	if err != nil {
		return fmt.Errorf("open %s decoder: %w", video.Codec, err)
	}
	defer dec.Close()

	var enc hwmediacodec.Encoder
	defer func() {
		if enc != nil {
			enc.Close()
		}
	}()

	frames, start, lastReport := 0, time.Now(), time.Now()
	var width, height int
	var lastDTS int64 = -1

	// drainEncoder moves finished packets to the muxer and keeps the other
	// tracks interleaved up to the video position.
	drainEncoder := func() error {
		for {
			p, err := enc.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if err := vw.WritePacket(p); err != nil {
				return err
			}
			lastDTS = p.DTS
			for _, pt := range others {
				if err := pt.copyUpTo(video.TimeOf(p.DTS)); err != nil {
					return err
				}
			}
		}
	}
	encodeFrame := func(f *hwmediacodec.Frame) error {
		defer f.Release()
		if enc == nil {
			width, height = f.Width, f.Height
			e, err := hwmediacodec.NewEncoder(ctx, o.codec, width, height, o.encoderOptions(video.TimeScale, video.FrameRate())...)
			if err != nil {
				return fmt.Errorf("open %s encoder: %w", o.codec, err)
			}
			enc = e
		}
		if f.Width != width || f.Height != height {
			return fmt.Errorf("picture size changed from %dx%d to %dx%d", width, height, f.Width, f.Height)
		}
		if err := enc.Send(ctx, f); err != nil {
			return err
		}
		frames++
		if now := time.Now(); now.Sub(lastReport) >= time.Second {
			lastReport = now
			fmt.Fprintf(progress, "\r%d/%d frames, %.0f fps", frames, video.SampleCount(), float64(frames)/now.Sub(start).Seconds())
		}
		return drainEncoder()
	}
	drainDecoder := func() error {
		for {
			f, err := dec.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if err := encodeFrame(f); err != nil {
				return err
			}
		}
	}

	for i := 0; i < video.SampleCount(); i++ {
		pkt, err := video.Packet(i)
		if err != nil {
			return err
		}
		for {
			err := dec.Send(ctx, pkt)
			if errors.Is(err, hwmediacodec.ErrAgain) {
				// The decoder's output queue is full (VA-API): make room.
				if err := drainDecoder(); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return fmt.Errorf("decode sample %d: %w", i, err)
			}
			break
		}
		if err := drainDecoder(); err != nil {
			return err
		}
	}
	if err := dec.Flush(ctx); err != nil {
		return err
	}
	if err := drainDecoder(); err != nil {
		return err
	}
	if enc == nil {
		return errors.New("the decoder produced no frames")
	}
	if err := enc.Flush(ctx); err != nil {
		return err
	}
	if err := drainEncoder(); err != nil {
		return err
	}
	for _, pt := range others {
		if err := pt.copyUpTo(-1); err != nil {
			return err
		}
	}
	_ = lastDTS
	if err := out.Close(); err != nil {
		return err
	}
	fmt.Fprintf(progress, "\r%d frames in %.1fs (%.0f fps)\n", frames, time.Since(start).Seconds(), float64(frames)/time.Since(start).Seconds())
	return nil
}
