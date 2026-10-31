// Package checker is the platform-independent part of hwmediacodec check:
// the automatic checks (decode, encode round trips, audio, once per
// backend) and the report with its Markdown and GitHub issue link. The
// playback stage is in package playback below it, which needs
// Ebitengine. The check command runs them in a desktop window and
// examples/mobile on a phone or tablet.
package checker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/cmd/hwmediacodec/assets"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// The environment variables that restrict the root package to some
// backends; each check runs once per backend that reports the codec.
const (
	backendsEnv      = "HWMEDIACODEC_BACKENDS"
	audioBackendsEnv = "HWMEDIACODEC_AUDIO_BACKENDS"
)

// checkTimeout bounds one check, so that a hung driver shows as a failure
// instead of stopping the run.
const checkTimeout = 60 * time.Second

// The encode round trips code srcFrames pictures of srcW x srcH: the
// bundled clip scaled down by two and cropped to multiples of 32, which
// every encoder takes.
const (
	srcW, srcH = 352, 640
	srcFrames  = 60
	srcFPS     = 30
	srcTick    = 90000 / srcFPS // PTS step in the default 90 kHz time scale
)

// Clip is one encoding of the bundled waterfall.
type Clip struct {
	Codec hwmediacodec.Codec
	Clip  assets.Clip
}

// Clips lists the waterfall in H.264, HEVC and AV1, the order the checks
// and the playback take them in.
func Clips() []Clip {
	all := assets.Clips() // HEVC, H.264
	return []Clip{
		{hwmediacodec.H264, all[1]},
		{hwmediacodec.HEVC, all[0]},
		{hwmediacodec.AV1, assets.AV1()},
	}
}

var videoCodecs = []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC, hwmediacodec.AV1}

// CodecName is the name of c in the report.
func CodecName(c hwmediacodec.Codec) string {
	switch c {
	case hwmediacodec.H264:
		return "H.264"
	case hwmediacodec.HEVC:
		return "HEVC"
	case hwmediacodec.AV1:
		return "AV1"
	case hwmediacodec.AAC:
		return "AAC"
	case hwmediacodec.Opus:
		return "Opus"
	}
	return c.String()
}

// Checker runs the automatic checks and writes their results to R.
type Checker struct {
	R   *Report
	src []*hwmediacodec.Frame // pictures for the encoders, from the first decoder that works
}

// backendsFor lists the backends that report c in direction dir.
func (k *Checker) backendsFor(c hwmediacodec.Codec, dir hwmediacodec.Direction) []string {
	var out []string
	for _, cp := range k.R.Caps() {
		if cp.Codec == c && cp.Direction == dir {
			out = append(out, cp.Backend)
		}
	}
	return out
}

// withBackend runs fn with the root package restricted to backend.
func withBackend(env, backend string, fn func()) {
	old, had := os.LookupEnv(env)
	os.Setenv(env, backend)
	defer func() {
		if had {
			os.Setenv(env, old)
		} else {
			os.Unsetenv(env)
		}
	}()
	fn()
}

// anyBackend runs fn with every backend allowed, for the decoder that
// checks an encoder's output when the encoder's backend has none.
func anyBackend(env string, fn func() error) error {
	old, had := os.LookupEnv(env)
	os.Unsetenv(env)
	defer func() {
		if had {
			os.Setenv(env, old)
		}
	}()
	return fn()
}

// Run probes the machine and runs every automatic check in turn; it
// takes about a minute on a machine with every codec.
func (k *Checker) Run(ctx context.Context) {
	caps, err := hwmediacodec.Probe(ctx)
	k.R.SetProbe(caps, err)

	for _, cl := range Clips() {
		k.each("Decode", cl.Codec, hwmediacodec.Decode, backendsEnv, func(ctx context.Context) (Status, string) {
			return k.decodeClip(ctx, cl)
		})
	}
	if len(k.src) == 0 {
		k.src = synthFrames()
		k.R.Note("No decoder worked, so the encoders coded a synthetic picture instead of the waterfall.")
	}
	for _, c := range videoCodecs {
		k.each("Encode", c, hwmediacodec.Encode, backendsEnv, func(ctx context.Context) (Status, string) {
			return k.roundTrip(ctx, c)
		})
	}
	k.each("Audio", hwmediacodec.AAC, hwmediacodec.Decode, audioBackendsEnv, func(ctx context.Context) (Status, string) {
		return decodeClipAudio(ctx)
	})
	for _, c := range []hwmediacodec.Codec{hwmediacodec.AAC, hwmediacodec.Opus} {
		k.each("Audio", c, hwmediacodec.Encode, audioBackendsEnv, func(ctx context.Context) (Status, string) {
			return audioRoundTrip(ctx, c)
		})
	}
}

// each runs check once per backend that reports c in direction dir, or
// records that none does.
func (k *Checker) each(section string, c hwmediacodec.Codec, dir hwmediacodec.Direction, env string, check func(context.Context) (Status, string)) {
	verb := "decode"
	if dir == hwmediacodec.Encode {
		verb = "encode"
	}
	backends := k.backendsFor(c, dir)
	if len(backends) == 0 {
		res := k.R.Add(section, CodecName(c)+" "+verb)
		k.R.Set(res, Unsupported, "no backend reports it on this machine")
		return
	}
	for _, b := range backends {
		res := k.R.Add(section, CodecName(c)+" "+verb+" / "+b)
		k.R.Set(res, Running, "")
		withBackend(env, b, func() {
			ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
			defer cancel()
			s, detail := check(ctx)
			k.R.Set(res, s, "%s", detail)
		})
	}
}

// Outcome turns an error into a result: ErrUnsupported is not a failure.
func Outcome(err error) (Status, string) {
	if errors.Is(err, hwmediacodec.ErrUnsupported) {
		return Unsupported, err.Error()
	}
	return Failed, err.Error()
}

// StatusOf is the status of a check that ended with err.
func StatusOf(err error) Status {
	s, _ := Outcome(err)
	return s
}

// decodeClip decodes the whole clip and checks the frames: count, size,
// display order and a picture that is not flat. The first clip that
// decodes also provides the encoders' pictures.
func (k *Checker) decodeClip(ctx context.Context, cl Clip) (Status, string) {
	d, err := mp4.NewDemuxer(cl.Clip.Open())
	if err != nil {
		return Failed, err.Error()
	}
	v := d.Video()
	if v == nil {
		return Failed, "no video track in " + cl.Clip.Name
	}
	src := v.PacketSource()
	dec, err := hwmediacodec.NewDecoder(ctx, v.Codec, hwmediacodec.WithTimeScale(src.TimeScale()))
	if err != nil {
		return Outcome(err)
	}
	defer dec.Close()
	frames := hwmediacodec.NewDecodeReader(dec, src)
	keep := len(k.src) == 0
	var kept []*hwmediacodec.Frame
	start := time.Now()
	n, last := 0, int64(-1)
	for {
		f, err := frames.ReadFrame(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			if n == 0 {
				return Outcome(err)
			}
			return Failed, fmt.Sprintf("frame %d: %v", n, err)
		}
		bad := ""
		switch {
		case f.Width != v.Width || f.Height != v.Height:
			bad = fmt.Sprintf("frame %d is %dx%d, the track %dx%d", n, f.Width, f.Height, v.Width, v.Height)
		case f.Format != hwmediacodec.NV12:
			bad = fmt.Sprintf("frame %d is %s, not NV12", n, f.Format)
		case f.PTS <= last:
			bad = fmt.Sprintf("frame %d has PTS %d after %d: not in display order", n, f.PTS, last)
		case n == 0 && flat(f):
			bad = "the first picture is flat (black or one colour)"
		}
		if bad != "" {
			f.Release()
			return Failed, bad
		}
		last = f.PTS
		if keep && len(kept) < srcFrames {
			kept = append(kept, halve(f, len(kept)))
		}
		f.Release()
		n++
	}
	elapsed := time.Since(start)
	if n != v.SampleCount() {
		return Failed, fmt.Sprintf("%d of %d frames came out", n, v.SampleCount())
	}
	if keep && len(kept) == srcFrames {
		k.src = kept
	}
	return Passed, fmt.Sprintf("%d frames %dx%d in %.2fs (%.0f fps)", n, v.Width, v.Height, elapsed.Seconds(), float64(n)/elapsed.Seconds())
}

// flat reports whether the luma of a picture barely varies.
func flat(f *hwmediacodec.Frame) bool {
	lo, hi := byte(255), byte(0)
	y := f.Planes[0]
	for row := 0; row < f.Height; row += 8 {
		line := y[row*f.Strides[0]:][:f.Width]
		for _, v := range line {
			lo, hi = min(lo, v), max(hi, v)
		}
	}
	return hi-lo < 32
}

// halve scales a decoded NV12 picture down by two, averaging the luma so
// that the encoders get no aliasing, and crops it to srcW x srcH around
// its centre.
func halve(f *hwmediacodec.Frame, i int) *hwmediacodec.Frame {
	ox := max(0, (f.Width/2-srcW)/2)
	oy := max(0, (f.Height/2-srcH)/2)
	y := make([]byte, srcW*srcH)
	uv := make([]byte, srcW*srcH/2)
	ys, uvs := f.Strides[0], f.Strides[1]
	for row := range srcH {
		sy := min(2*(row+oy), f.Height-2)
		for col := range srcW {
			sx := min(2*(col+ox), f.Width-2)
			p := f.Planes[0][sy*ys+sx:]
			q := f.Planes[0][(sy+1)*ys+sx:]
			y[row*srcW+col] = byte((int(p[0]) + int(p[1]) + int(q[0]) + int(q[1]) + 2) / 4)
		}
	}
	for row := range srcH / 2 {
		sy := min(2*(row+oy/2), f.Height/2-1)
		for pair := range srcW / 2 {
			sx := min(2*(pair+ox/2), f.Width/2-1)
			uv[row*srcW+2*pair] = f.Planes[1][sy*uvs+2*sx]
			uv[row*srcW+2*pair+1] = f.Planes[1][sy*uvs+2*sx+1]
		}
	}
	return nv12(y, uv, i)
}

func nv12(y, uv []byte, i int) *hwmediacodec.Frame {
	return &hwmediacodec.Frame{
		Width: srcW, Height: srcH, Format: hwmediacodec.NV12,
		Planes: [][]byte{y, uv}, Strides: []int{srcW, srcW},
		PTS: int64(i) * srcTick,
	}
}

// synthFrames draws a calm evening sky with a drifting moon, for machines
// where no decoder produced the waterfall.
func synthFrames() []*hwmediacodec.Frame {
	frames := make([]*hwmediacodec.Frame, srcFrames)
	for i := range frames {
		y := make([]byte, srcW*srcH)
		uv := make([]byte, srcW*srcH/2)
		cx, cy := float64(srcW)*(0.3+0.4*float64(i)/srcFrames), float64(srcH)*0.3
		for row := range srcH {
			for col := range srcW {
				l := 40 + 120*float64(row)/srcH
				if d := math.Hypot(float64(col)-cx, float64(row)-cy); d < 40 {
					l = 220 - d
				}
				y[row*srcW+col] = byte(l)
			}
		}
		for row := range srcH / 2 {
			for pair := range srcW / 2 {
				uv[row*srcW+2*pair] = byte(150 - 30*float64(row)/(srcH/2))
				uv[row*srcW+2*pair+1] = byte(110 + 40*float64(row)/(srcH/2))
			}
		}
		frames[i] = nv12(y, uv, i)
	}
	return frames
}

// packets is a PacketReader over encoded packets.
type packets struct {
	codec hwmediacodec.Codec
	pkts  []hwmediacodec.Packet
	scale int32
}

func (p *packets) Codec() hwmediacodec.Codec { return p.codec }
func (p *packets) TimeScale() int32          { return p.scale }
func (p *packets) ReadPacket() (hwmediacodec.Packet, error) {
	if len(p.pkts) == 0 {
		return hwmediacodec.Packet{}, io.EOF
	}
	pkt := p.pkts[0]
	p.pkts = p.pkts[1:]
	return pkt, nil
}

// roundTrip encodes the source pictures, decodes the stream again (on any
// backend when the encoder's has no decoder) and measures the PSNR of the
// luma against the source.
func (k *Checker) roundTrip(ctx context.Context, c hwmediacodec.Codec) (Status, string) {
	enc, err := hwmediacodec.NewEncoder(ctx, c, srcW, srcH,
		hwmediacodec.WithFrameRate(srcFPS), hwmediacodec.WithBitrate(4_000_000), hwmediacodec.WithKeyframeInterval(srcFPS))
	if err != nil {
		return Outcome(err)
	}
	defer enc.Close()
	var out []hwmediacodec.Packet
	bytes := 0
	w := hwmediacodec.NewEncodeWriter(enc, hwmediacodec.PacketWriterFunc(func(p hwmediacodec.Packet) error {
		out = append(out, p)
		bytes += len(p.Data)
		return nil
	}))
	start := time.Now()
	for _, f := range k.src {
		if err := w.WriteFrame(ctx, f); err != nil {
			return Failed, fmt.Sprintf("frame %d: %v", f.PTS/srcTick, err)
		}
	}
	if err := w.Flush(ctx); err != nil {
		return Failed, "flush: " + err.Error()
	}
	elapsed := time.Since(start)
	encoded := fmt.Sprintf("%d frames %dx%d at %.1f Mbit/s, %.0f fps", len(out), srcW, srcH,
		float64(bytes)*8/(float64(len(k.src))/srcFPS)/1e6, float64(len(k.src))/elapsed.Seconds())
	switch {
	case len(out) != len(k.src):
		return Failed, fmt.Sprintf("%d packets for %d frames", len(out), len(k.src))
	case !out[0].Keyframe:
		return Failed, "the first packet is not a keyframe"
	}

	// next compares each picture with the following source picture: a
	// stream whose frames come back in the wrong place matches that one
	// as well as its own.
	var psnrs, next []float64
	err = func() error {
		dec, err := hwmediacodec.NewDecoder(ctx, c)
		if errors.Is(err, hwmediacodec.ErrUnsupported) {
			err = anyBackend(backendsEnv, func() error {
				dec, err = hwmediacodec.NewDecoder(ctx, c)
				return err
			})
		}
		if err != nil {
			return err
		}
		defer dec.Close()
		frames := hwmediacodec.NewDecodeReader(dec, &packets{codec: c, pkts: out, scale: hwmediacodec.DefaultTimeScale})
		for {
			f, err := frames.ReadFrame(ctx)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			i := int(f.PTS / srcTick)
			if f.PTS%srcTick != 0 || i < 0 || i >= len(k.src) || f.Width != srcW || f.Height != srcH {
				f.Release()
				return fmt.Errorf("decoded a %dx%d frame with PTS %d", f.Width, f.Height, f.PTS)
			}
			psnrs = append(psnrs, lumaPSNR(k.src[i], f))
			if i+1 < len(k.src) {
				next = append(next, lumaPSNR(k.src[i+1], f))
			}
			f.Release()
		}
	}()
	if errors.Is(err, hwmediacodec.ErrUnsupported) {
		return Passed, encoded + "; no decoder here to check the stream"
	}
	if err != nil {
		return Failed, encoded + "; decoding it back: " + err.Error()
	}
	if len(psnrs) != len(k.src) {
		return Failed, fmt.Sprintf("%s; %d of %d frames decoded back", encoded, len(psnrs), len(k.src))
	}
	avg, lo := 0.0, math.Inf(1)
	for _, p := range psnrs {
		avg += p
		lo = math.Min(lo, p)
	}
	avg /= float64(len(psnrs))
	neighbour := 0.0
	for _, p := range next {
		neighbour += p
	}
	neighbour /= float64(max(1, len(next)))
	detail := fmt.Sprintf("%s; decoded back at %.1f dB PSNR (lowest %.1f, %.1f against the next picture)", encoded, avg, lo, neighbour)
	// The waterfall is hard to code: a working encoder reaches 30 dB and
	// more at this rate, a broken stream stays far below 20.
	switch {
	case avg < 25 || lo < 20:
		return Failed, detail + ": the pictures do not match"
	case avg < neighbour+1:
		return Failed, detail + ": the pictures come back in the wrong order"
	}
	return Passed, detail
}

// lumaPSNR compares the luma of two NV12 pictures of the same size.
func lumaPSNR(a, b *hwmediacodec.Frame) float64 {
	var sum float64
	for row := range a.Height {
		ra := a.Planes[0][row*a.Strides[0]:][:a.Width]
		rb := b.Planes[0][row*b.Strides[0]:][:a.Width]
		for i := range ra {
			d := float64(ra[i]) - float64(rb[i])
			sum += d * d
		}
	}
	mse := sum / float64(a.Width*a.Height)
	if mse == 0 {
		return 99
	}
	return 10 * math.Log10(255*255/mse)
}

// decodeAudio feeds every packet of src to dec and returns the number of
// samples per channel and their RMS level (full scale is 1).
func decodeAudio(ctx context.Context, dec hwmediacodec.AudioDecoder, src hwmediacodec.PacketReader) (samples int, rms float64, err error) {
	var sum float64
	var count int
	take := func(f *hwmediacodec.AudioFrame) {
		samples += f.Samples()
		if f.Format == hwmediacodec.S16 {
			for _, v := range f.Int16s() {
				x := float64(v) / 32768
				sum += x * x
			}
		} else {
			for _, v := range f.Float32s() {
				sum += float64(v) * float64(v)
			}
		}
		count += f.Samples() * f.Channels
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
			take(f)
		}
	}
	for {
		p, err := src.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, err
		}
		for {
			err := dec.Send(ctx, p)
			if errors.Is(err, hwmediacodec.ErrAgain) {
				if err := drain(); err != nil {
					return 0, 0, err
				}
				continue
			}
			if err != nil {
				return 0, 0, err
			}
			break
		}
		if err := drain(); err != nil {
			return 0, 0, err
		}
	}
	if err := dec.Flush(ctx); err != nil {
		return 0, 0, err
	}
	if err := drain(); err != nil {
		return 0, 0, err
	}
	if count > 0 {
		rms = math.Sqrt(sum / float64(count))
	}
	return samples, rms, nil
}

func dBFS(rms float64) float64 {
	if rms <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(rms)
}

// decodeClipAudio decodes the AAC track of the bundled clip.
func decodeClipAudio(ctx context.Context) (Status, string) {
	d, err := mp4.NewDemuxer(assets.Clips()[0].Open())
	if err != nil {
		return Failed, err.Error()
	}
	a := d.Audio()
	if a == nil {
		return Failed, "the bundled clip has no audio track"
	}
	format := a.Format()
	dec, err := hwmediacodec.NewAudioDecoder(ctx, format)
	if err != nil {
		return Outcome(err)
	}
	defer dec.Close()
	src := a.PacketSource()
	samples, rms, err := decodeAudio(ctx, dec, src)
	if err != nil {
		return Failed, err.Error()
	}
	secs := float64(samples) / float64(format.SampleRate)
	detail := fmt.Sprintf("%.2fs at %d Hz, %d channels, level %.0f dBFS", secs, format.SampleRate, format.Channels, dBFS(rms))
	switch want := src.Length().Seconds(); {
	case secs < want*0.97 || secs > want*1.05:
		return Failed, fmt.Sprintf("%s; the track lasts %.2fs", detail, want)
	case dBFS(rms) < -50:
		return Failed, detail + ": silent"
	}
	return Passed, detail
}

// audioRoundTrip encodes two seconds of a soft two-tone chord and decodes
// it again (on any backend when the encoder's has no decoder).
func audioRoundTrip(ctx context.Context, c hwmediacodec.Codec) (Status, string) {
	const rate, channels, seconds = 48000, 2, 2
	pcm := make([]int16, rate*channels*seconds)
	for i := 0; i < rate*seconds; i++ {
		t := float64(i) / rate
		pcm[2*i] = int16(8000 * math.Sin(2*math.Pi*440*t))
		pcm[2*i+1] = int16(8000 * math.Sin(2*math.Pi*660*t))
	}
	in := 8000.0 / 32768 / math.Sqrt2
	enc, err := hwmediacodec.NewAudioEncoder(ctx, c, rate, channels)
	if err != nil {
		return Outcome(err)
	}
	defer enc.Close()
	var out []hwmediacodec.Packet
	drain := func() error {
		for {
			p, err := enc.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			out = append(out, p)
		}
	}
	const chunk = rate / 10
	for i := 0; i < rate*seconds; i += chunk {
		f := hwmediacodec.S16Frame(pcm[i*channels:(i+chunk)*channels], rate, channels, int64(i))
		for {
			err := enc.Send(ctx, f)
			if errors.Is(err, hwmediacodec.ErrAgain) {
				if err := drain(); err != nil {
					return Failed, err.Error()
				}
				continue
			}
			if err != nil {
				return Failed, err.Error()
			}
			break
		}
		if err := drain(); err != nil {
			return Failed, err.Error()
		}
	}
	if err := enc.Flush(ctx); err != nil {
		return Failed, "flush: " + err.Error()
	}
	if err := drain(); err != nil {
		return Failed, err.Error()
	}
	format := enc.Format()
	bytes := 0
	for _, p := range out {
		bytes += len(p.Data)
	}
	encoded := fmt.Sprintf("%d packets, %.0f kbit/s", len(out), float64(bytes)*8/seconds/1000)
	if len(out) == 0 {
		return Failed, "no packets"
	}

	var samples int
	var rms float64
	err = func() error {
		dec, err := hwmediacodec.NewAudioDecoder(ctx, format)
		if errors.Is(err, hwmediacodec.ErrUnsupported) {
			err = anyBackend(audioBackendsEnv, func() error {
				dec, err = hwmediacodec.NewAudioDecoder(ctx, format)
				return err
			})
		}
		if err != nil {
			return err
		}
		defer dec.Close()
		samples, rms, err = decodeAudio(ctx, dec, &packets{codec: c, pkts: out, scale: int32(rate)})
		return err
	}()
	if errors.Is(err, hwmediacodec.ErrUnsupported) {
		return Passed, encoded + "; no decoder here to check the stream"
	}
	if err != nil {
		return Failed, encoded + "; decoding it back: " + err.Error()
	}
	detail := fmt.Sprintf("%s; decoded back %.2fs at %.1f dB of the input level", encoded,
		float64(samples)/float64(format.SampleRate), dBFS(rms)-dBFS(in))
	if float64(samples)/float64(format.SampleRate) < seconds || rms < in/2 || rms > in*2 {
		return Failed, detail + ": the sound does not match"
	}
	return Passed, detail
}
