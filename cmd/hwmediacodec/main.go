// Command hwmediacodec probes the hardware codecs on this machine, decodes
// elementary streams, encodes raw frames and transcodes between the two.
//
//	hwmediacodec probe
//	hwmediacodec decode [-codec h264|hevc|av1] [-format nv12|rgba|bgra] [-decode-order] [-o out.raw] [-hash] file
//	hwmediacodec encode -size WxH [-format nv12|rgba|bgra] [-codec h264|hevc] [encode flags] -o out.h264 in.raw
//	hwmediacodec transcode [-in h264|hevc|av1] [-codec h264|hevc] [encode flags] -o out.hevc in.h264
//
// H.264 and HEVC input is a raw Annex-B stream; AV1 input is an IVF file
// (one temporal unit per frame, as ffmpeg -f ivf writes it).
//
// Encode flags: -rate 30 -bitrate 4M -cbr -quality 0.7 -gop 60 -bframes
// -lowlatency -profile baseline|main|high -software.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/bitstream/annexb"
	"github.com/shibukawa/hwmediacodec/mediacontainer/ivf"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "probe":
		err = probe()
	case "decode":
		err = decode(os.Args[2:])
	case "encode":
		err = encode(os.Args[2:])
	case "transcode":
		err = transcode(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hwmediacodec:", strings.TrimPrefix(err.Error(), "hwmediacodec: "))
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  hwmediacodec probe
  hwmediacodec decode [-codec h264|hevc|av1] [-format nv12|rgba|bgra] [-decode-order] [-o out.raw] [-hash] file
  hwmediacodec encode -size WxH [-format nv12|rgba|bgra] [-codec h264|hevc] [encode flags] -o out.h264 in.raw
  hwmediacodec transcode [-in h264|hevc|av1] [-codec h264|hevc] [encode flags] -o out.hevc in.h264
h264/hevc input is a raw Annex-B stream, av1 input an IVF file
encode flags: -rate 30 -bitrate 4M -cbr -quality 0.7 -gop 60 -bframes -lowlatency -profile baseline|main|high -software`)
}

func probe() error {
	caps, err := hwmediacodec.Probe(context.Background())
	if err != nil {
		return err
	}
	if len(caps) == 0 {
		fmt.Println("no hardware codec backend available on this machine")
		return nil
	}
	for _, c := range caps {
		hw := "software"
		if c.Hardware {
			hw = "hardware"
		}
		fmt.Printf("%-14s %-5s %-7s %s\n", c.Backend, c.Codec, c.Direction, hw)
	}
	return nil
}

func parseCodec(name string) (hwmediacodec.Codec, error) {
	switch strings.ToLower(name) {
	case "h264", "avc":
		return hwmediacodec.H264, nil
	case "hevc", "h265":
		return hwmediacodec.HEVC, nil
	case "av1":
		return hwmediacodec.AV1, nil
	}
	return 0, fmt.Errorf("unknown codec %q", name)
}

// packetReader yields the packets of an input file: Annex-B access units
// for H.264 and HEVC, stamped as 30 fps, or IVF frames (temporal units) for
// AV1 with the file's own timestamps rescaled to DefaultTimeScale.
type packetReader struct {
	next func() (data []byte, pts int64, err error)
}

func newPacketReader(in io.Reader, c hwmediacodec.Codec) (*packetReader, error) {
	if c != hwmediacodec.AV1 {
		r := annexb.NewReader(in, c)
		var pts int64
		return &packetReader{next: func() ([]byte, int64, error) {
			au, err := r.Next()
			if err != nil {
				return nil, 0, err
			}
			p := pts
			pts += ptsStep(30)
			return au, p, nil
		}}, nil
	}
	r, err := ivf.NewReader(in)
	if err != nil {
		return nil, err
	}
	h := r.Header()
	var index int64
	return &packetReader{next: func() ([]byte, int64, error) {
		tu, ts, err := r.Next()
		if err != nil {
			return nil, 0, err
		}
		pts := index * ptsStep(30)
		if h.TimebaseDen > 0 {
			pts = int64(ts) * int64(h.TimebaseNum) * int64(hwmediacodec.DefaultTimeScale) / int64(h.TimebaseDen)
		}
		index++
		return tu, pts, nil
	}}, nil
}

// Next returns the next packet, or io.EOF.
func (r *packetReader) Next() ([]byte, int64, error) { return r.next() }

func parseFormat(name string) (hwmediacodec.PixelFormat, error) {
	switch strings.ToLower(name) {
	case "nv12":
		return hwmediacodec.NV12, nil
	case "rgba":
		return hwmediacodec.RGBA, nil
	case "bgra":
		return hwmediacodec.BGRA, nil
	}
	return 0, fmt.Errorf("unknown pixel format %q", name)
}

// rawFrame wraps one tightly packed raw frame.
func rawFrame(buf []byte, f hwmediacodec.PixelFormat, width, height int, pts int64) *hwmediacodec.Frame {
	fr := &hwmediacodec.Frame{Width: width, Height: height, Format: f, PTS: pts}
	off := 0
	for i := 0; i < f.PlaneCount(); i++ {
		rows, rowBytes := f.PlaneLayout(i, width, height)
		fr.Planes = append(fr.Planes, buf[off:off+rows*rowBytes])
		fr.Strides = append(fr.Strides, rowBytes)
		off += rows * rowBytes
	}
	return fr
}

func parseSize(s string) (int, int, error) {
	w, h, ok := strings.Cut(strings.ToLower(s), "x")
	if !ok {
		return 0, 0, fmt.Errorf("size must be WxH, got %q", s)
	}
	width, err1 := strconv.Atoi(w)
	height, err2 := strconv.Atoi(h)
	if err1 != nil || err2 != nil || width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("size must be WxH, got %q", s)
	}
	return width, height, nil
}

// parseBitrate accepts plain bits per second or a k/M suffix (e.g. 800k, 4M).
func parseBitrate(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	mult := 1
	switch s[len(s)-1] {
	case 'k', 'K':
		mult, s = 1000, s[:len(s)-1]
	case 'm', 'M':
		mult, s = 1000000, s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid bitrate %q", s)
	}
	return int(v * float64(mult)), nil
}

// encodeFlags holds the encoder controls shared by encode and transcode.
type encodeFlags struct {
	codec      string
	rate       float64
	bitrate    string
	cbr        bool
	quality    float64
	gop        int
	bframes    bool
	lowLatency bool
	profile    string
	software   bool
}

func addEncodeFlags(fs *flag.FlagSet) *encodeFlags {
	f := &encodeFlags{}
	fs.StringVar(&f.codec, "codec", "h264", "output codec: h264 or hevc")
	fs.Float64Var(&f.rate, "rate", 30, "frame rate used for timestamps and rate control")
	fs.StringVar(&f.bitrate, "bitrate", "", "target bitrate in bit/s (suffix k or M); default: backend choice")
	fs.BoolVar(&f.cbr, "cbr", false, "constant bitrate instead of variable")
	fs.Float64Var(&f.quality, "quality", 0, "constant quality in (0, 1] instead of a bitrate")
	fs.IntVar(&f.gop, "gop", 0, "keyframe every N frames (0: backend choice)")
	fs.BoolVar(&f.bframes, "bframes", false, "allow B-frames")
	fs.BoolVar(&f.lowLatency, "lowlatency", false, "low-latency live streaming mode")
	fs.StringVar(&f.profile, "profile", "", "coding profile: baseline, main or high")
	fs.BoolVar(&f.software, "software", false, "allow the OS software encoder when no hardware engine exists")
	return f
}

func (f *encodeFlags) options() ([]hwmediacodec.EncoderOption, error) {
	if f.rate <= 0 {
		return nil, errors.New("frame rate must be positive")
	}
	opts := []hwmediacodec.EncoderOption{hwmediacodec.WithFrameRate(f.rate)}
	bitrate, err := parseBitrate(f.bitrate)
	if err != nil {
		return nil, err
	}
	if bitrate > 0 {
		opts = append(opts, hwmediacodec.WithBitrate(bitrate))
	}
	if f.cbr {
		opts = append(opts, hwmediacodec.WithRateControl(hwmediacodec.CBR))
	}
	if f.quality > 0 {
		opts = append(opts, hwmediacodec.WithQuality(f.quality))
	}
	if f.gop > 0 {
		opts = append(opts, hwmediacodec.WithKeyframeInterval(f.gop))
	}
	if f.bframes {
		opts = append(opts, hwmediacodec.WithBFrames())
	}
	if f.lowLatency {
		opts = append(opts, hwmediacodec.WithLowLatency())
	}
	switch strings.ToLower(f.profile) {
	case "":
	case "baseline":
		opts = append(opts, hwmediacodec.WithProfile(hwmediacodec.ProfileBaseline))
	case "main":
		opts = append(opts, hwmediacodec.WithProfile(hwmediacodec.ProfileMain))
	case "high":
		opts = append(opts, hwmediacodec.WithProfile(hwmediacodec.ProfileHigh))
	default:
		return nil, fmt.Errorf("unknown profile %q", f.profile)
	}
	if f.software {
		opts = append(opts, hwmediacodec.WithSoftwareFallback())
	}
	return opts, nil
}

// ptsStep returns the PTS increment per frame for the given rate.
func ptsStep(rate float64) int64 {
	return int64(float64(hwmediacodec.DefaultTimeScale)/rate + 0.5)
}

func openOutput(path string) (*bufio.Writer, func() error, error) {
	if path == "" {
		return bufio.NewWriter(io.Discard), func() error { return nil }, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return bufio.NewWriter(f), f.Close, nil
}

// packetSink writes encoded packets and keeps statistics.
type packetSink struct {
	w         io.Writer
	packets   int
	keyframes int
	bytes     int
}

func (s *packetSink) drain(ctx context.Context, enc hwmediacodec.Encoder) error {
	for {
		p, err := enc.Receive(ctx)
		if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := s.w.Write(p.Data); err != nil {
			return err
		}
		s.packets++
		s.bytes += len(p.Data)
		if p.Keyframe {
			s.keyframes++
		}
	}
}

func (s *packetSink) report(what string, width, height int, elapsed time.Duration, rate float64) {
	seconds := float64(s.packets) / rate
	fmt.Fprintf(os.Stderr, "%s %d frames (%dx%d) in %s (%.1f fps); %d bytes, %d keyframes, %.0f kbit/s\n",
		what, s.packets, width, height, elapsed.Round(time.Millisecond), float64(s.packets)/elapsed.Seconds(),
		s.bytes, s.keyframes, float64(s.bytes)*8/seconds/1000)
}

func encode(args []string) error {
	fs := flag.NewFlagSet("encode", flag.ExitOnError)
	size := fs.String("size", "", "input picture size as WxH (required)")
	formatName := fs.String("format", "nv12", "input pixel format: nv12, rgba or bgra")
	out := fs.String("o", "", "write the Annex-B stream to this file")
	ef := addEncodeFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("encode needs exactly one input file of raw frames")
	}
	width, height, err := parseSize(*size)
	if err != nil {
		return err
	}
	format, err := parseFormat(*formatName)
	if err != nil {
		return err
	}
	c, err := parseCodec(ef.codec)
	if err != nil {
		return err
	}
	opts, err := ef.options()
	if err != nil {
		return err
	}
	opts = append(opts, hwmediacodec.WithInputFormat(format))
	in, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer in.Close()
	w, closeOut, err := openOutput(*out)
	if err != nil {
		return err
	}
	defer closeOut()

	ctx := context.Background()
	enc, err := hwmediacodec.NewEncoder(ctx, c, width, height, opts...)
	if err != nil {
		return err
	}
	defer enc.Close()

	sink := &packetSink{w: w}
	frameSize := format.FrameSize(width, height)
	buf := make([]byte, frameSize)
	r := bufio.NewReaderSize(in, 1<<20)
	step := ptsStep(ef.rate)
	start := time.Now()
	for i := 0; ; i++ {
		if _, err := io.ReadFull(r, buf); err != nil {
			if err == io.EOF {
				break
			}
			if err == io.ErrUnexpectedEOF {
				return fmt.Errorf("input ends inside frame %d (frame size %d bytes)", i, frameSize)
			}
			return err
		}
		f := rawFrame(buf, format, width, height, int64(i)*step)
		if err := enc.Send(ctx, f); err != nil {
			return fmt.Errorf("send frame %d: %w", i, err)
		}
		if err := sink.drain(ctx, enc); err != nil {
			return err
		}
	}
	if err := enc.Flush(ctx); err != nil {
		return err
	}
	if err := sink.drain(ctx, enc); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	sink.report("encoded", width, height, time.Since(start), ef.rate)
	return nil
}

// transcode decodes an elementary stream and re-encodes it. Frames are
// re-encoded in the order the decoder returns them (decode order), so sources
// with B-frames come out with their pictures reordered; see the README.
func transcode(args []string) error {
	fs := flag.NewFlagSet("transcode", flag.ExitOnError)
	inCodec := fs.String("in", "h264", "input codec: h264 or hevc (Annex-B file) or av1 (IVF file)")
	out := fs.String("o", "", "write the Annex-B stream to this file")
	ef := addEncodeFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("transcode needs exactly one input file")
	}
	ic, err := parseCodec(*inCodec)
	if err != nil {
		return err
	}
	oc, err := parseCodec(ef.codec)
	if err != nil {
		return err
	}
	opts, err := ef.options()
	if err != nil {
		return err
	}
	in, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer in.Close()
	w, closeOut, err := openOutput(*out)
	if err != nil {
		return err
	}
	defer closeOut()

	ctx := context.Background()
	var decOpts []hwmediacodec.DecoderOption
	if ef.software {
		decOpts = append(decOpts, hwmediacodec.WithSoftwareFallback())
	}
	dec, err := hwmediacodec.NewDecoder(ctx, ic, decOpts...)
	if err != nil {
		return err
	}
	defer dec.Close()

	var enc hwmediacodec.Encoder
	defer func() {
		if enc != nil {
			enc.Close()
		}
	}()
	sink := &packetSink{w: w}
	step := ptsStep(ef.rate)
	var width, height, frames int
	start := time.Now()
	encodeFrame := func(f *hwmediacodec.Frame) error {
		defer f.Release()
		if enc == nil {
			width, height = f.Width, f.Height
			e, err := hwmediacodec.NewEncoder(ctx, oc, width, height, opts...)
			if err != nil {
				return err
			}
			enc = e
		}
		if f.Width != width || f.Height != height {
			return fmt.Errorf("picture size changed from %dx%d to %dx%d; transcode needs a constant size", width, height, f.Width, f.Height)
		}
		f.PTS = int64(frames) * step
		frames++
		if err := enc.Send(ctx, f); err != nil {
			return err
		}
		return sink.drain(ctx, enc)
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
	r, err := newPacketReader(bufio.NewReaderSize(in, 1<<20), ic)
	if err != nil {
		return err
	}
	for {
		au, pts, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := dec.Send(ctx, hwmediacodec.Packet{Data: au, PTS: pts}); err != nil {
			return fmt.Errorf("decode: %w", err)
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
		return errors.New("the input produced no frames")
	}
	if err := enc.Flush(ctx); err != nil {
		return err
	}
	if err := sink.drain(ctx, enc); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	sink.report("transcoded", width, height, time.Since(start), ef.rate)
	return nil
}

func decode(args []string) error {
	fs := flag.NewFlagSet("decode", flag.ExitOnError)
	codecName := fs.String("codec", "h264", "input codec: h264 or hevc (Annex-B file) or av1 (IVF file)")
	formatName := fs.String("format", "nv12", "output pixel format: nv12, rgba or bgra")
	decodeOrder := fs.Bool("decode-order", false, "return frames in decode order instead of display order")
	out := fs.String("o", "", "write decoded raw frames to this file")
	hash := fs.Bool("hash", false, "print the SHA-256 of every frame")
	software := fs.Bool("software", false, "allow the OS software decoder when no hardware engine exists")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("decode needs exactly one input file")
	}
	c, err := parseCodec(*codecName)
	if err != nil {
		return err
	}
	format, err := parseFormat(*formatName)
	if err != nil {
		return err
	}

	in, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer in.Close()
	w, closeOut, err := openOutput(*out)
	if err != nil {
		return err
	}
	defer closeOut()

	ctx := context.Background()
	opts := []hwmediacodec.DecoderOption{hwmediacodec.WithOutputFormat(format)}
	if *software {
		opts = append(opts, hwmediacodec.WithSoftwareFallback())
	}
	if *decodeOrder {
		opts = append(opts, hwmediacodec.WithDecodeOrder())
	}
	dec, err := hwmediacodec.NewDecoder(ctx, c, opts...)
	if err != nil {
		return err
	}
	defer dec.Close()

	start := time.Now()
	frames := 0
	var width, height int
	emit := func(f *hwmediacodec.Frame) error {
		defer f.Release()
		width, height = f.Width, f.Height
		h := sha256.New()
		for i, p := range f.Planes {
			rows, rowBytes := f.Format.PlaneLayout(i, f.Width, f.Height)
			for r := 0; r < rows; r++ {
				row := p[r*f.Strides[i] : r*f.Strides[i]+rowBytes]
				if _, err := w.Write(row); err != nil {
					return err
				}
				if *hash {
					h.Write(row)
				}
			}
		}
		if *hash {
			fmt.Printf("frame %5d pts %10d %dx%d %x\n", frames, f.PTS, f.Width, f.Height, h.Sum(nil))
		}
		frames++
		return nil
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
			if err := emit(f); err != nil {
				return err
			}
		}
	}

	r, err := newPacketReader(bufio.NewReaderSize(in, 1<<20), c)
	if err != nil {
		return err
	}
	for {
		au, pts, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		for {
			err := dec.Send(ctx, hwmediacodec.Packet{Data: au, PTS: pts})
			if errors.Is(err, hwmediacodec.ErrAgain) {
				// The decoder needs its output drained before it can take
				// more input.
				if err := drain(); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return fmt.Errorf("send: %w", err)
			}
			break
		}
		if err := drain(); err != nil {
			return err
		}
	}
	if err := dec.Flush(ctx); err != nil {
		return err
	}
	if err := drain(); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	elapsed := time.Since(start)
	fmt.Fprintf(os.Stderr, "decoded %d frames (%dx%d) in %s (%.1f fps)\n", frames, width, height, elapsed.Round(time.Millisecond), float64(frames)/elapsed.Seconds())
	return nil
}
