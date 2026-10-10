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

// interleaved writes the video packets and keeps the other tracks
// interleaved up to the video position.
type interleaved struct {
	video  *mp4.VideoWriter
	scale  time.Duration // video time scale
	others []*passthrough
}

func (w *interleaved) WritePacket(p hwmediacodec.Packet) error {
	if err := w.video.WritePacket(p); err != nil {
		return err
	}
	for _, pt := range w.others {
		if err := pt.copyUpTo(time.Duration(p.DTS) * time.Second / w.scale); err != nil {
			return err
		}
	}
	return nil
}

// encoding is the frame writer of the conversion: it opens the encoder
// for the size of the first decoded picture and reports progress.
type encoding struct {
	o      options
	scale  uint32
	fps    float64
	dst    hwmediacodec.PacketWriter
	enc    hwmediacodec.Encoder
	w      *hwmediacodec.EncodeWriter
	width  int
	height int
	frames int
	total  int
	start  time.Time
	last   time.Time
	out    io.Writer
}

func (e *encoding) WriteFrame(ctx context.Context, f *hwmediacodec.Frame) error {
	if e.enc == nil {
		e.width, e.height = f.Width, f.Height
		enc, err := hwmediacodec.NewEncoder(ctx, e.o.codec, e.width, e.height, e.o.encoderOptions(e.scale, e.fps)...)
		if err != nil {
			return fmt.Errorf("open %s encoder: %w", e.o.codec, err)
		}
		e.enc, e.w = enc, hwmediacodec.NewEncodeWriter(enc, e.dst)
	}
	if f.Width != e.width || f.Height != e.height {
		return fmt.Errorf("picture size changed from %dx%d to %dx%d", e.width, e.height, f.Width, f.Height)
	}
	if err := e.w.WriteFrame(ctx, f); err != nil {
		return err
	}
	e.frames++
	if now := time.Now(); now.Sub(e.last) >= time.Second {
		e.last = now
		fmt.Fprintf(e.out, "\r%d/%d frames, %.0f fps", e.frames, e.total, float64(e.frames)/now.Sub(e.start).Seconds())
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
	dst := &interleaved{video: vw, scale: time.Duration(video.TimeScale)}
	for _, t := range in.Others() {
		dst.others = append(dst.others, &passthrough{src: t, dst: out.AddPassthroughTrack(t)})
	}

	// The whole conversion: packets from the track into the decoder, its
	// frames into the encoder, the encoder's packets into the muxer.
	src := video.PacketSource()
	decOpts := []hwmediacodec.DecoderOption{hwmediacodec.WithTimeScale(src.TimeScale())}
	if o.software {
		decOpts = append(decOpts, hwmediacodec.WithSoftwareFallback())
	}
	dec, err := hwmediacodec.NewDecoder(ctx, src.Codec(), decOpts...)
	if err != nil {
		return fmt.Errorf("open %s decoder: %w", video.Codec, err)
	}
	defer dec.Close()
	enc := &encoding{o: o, scale: video.TimeScale, fps: video.FrameRate(), dst: dst,
		total: video.SampleCount(), start: time.Now(), last: time.Now(), out: progress}
	defer func() {
		if enc.enc != nil {
			enc.enc.Close()
		}
	}()
	if _, err := hwmediacodec.CopyFrames(ctx, enc, hwmediacodec.NewDecodeReader(dec, src)); err != nil {
		return err
	}
	if enc.w == nil {
		return errors.New("the decoder produced no frames")
	}
	if err := enc.w.Flush(ctx); err != nil {
		return err
	}
	for _, pt := range dst.others {
		if err := pt.copyUpTo(-1); err != nil {
			return err
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	elapsed := time.Since(enc.start).Seconds()
	fmt.Fprintf(progress, "\r%d frames in %.1fs (%.0f fps)\n", enc.frames, elapsed, float64(enc.frames)/elapsed)
	return nil
}
