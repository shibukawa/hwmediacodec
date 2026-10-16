package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/encoding/adts"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// audioInput is an audio stream of a file: an ADTS file (.aac) or the
// audio track of an MP4 file.
type audioInput interface {
	hwmediacodec.PacketReader
	Format() hwmediacodec.AudioFormat
}

func openAudioInput(path string) (audioInput, io.Closer, error) {
	if strings.EqualFold(filepath.Ext(path), ".aac") {
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, err
		}
		r, err := adts.NewReader(f)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		return r, f, nil
	}
	d, err := mp4.Open(path)
	if err != nil {
		return nil, nil, err
	}
	a := d.Audio()
	if a == nil {
		d.Close()
		return nil, nil, fmt.Errorf("%s has no AAC or Opus audio track", path)
	}
	return a.PacketSource(), d, nil
}

func parseSampleFormat(name string) (hwmediacodec.SampleFormat, error) {
	switch strings.ToLower(name) {
	case "s16":
		return hwmediacodec.S16, nil
	case "f32":
		return hwmediacodec.F32, nil
	}
	return 0, fmt.Errorf("unknown sample format %q (s16 or f32)", name)
}

// decodeAudio decodes an .aac file or the audio track of an MP4 file to
// raw interleaved samples: every sample the stream codes, the encoder's
// delay and padding included, as ffmpeg gives them with -flags2
// +skip_manual.
func decodeAudio(args []string) error {
	fs := flag.NewFlagSet("decode-audio", flag.ExitOnError)
	formatName := fs.String("format", "s16", "sample format of the output: s16 or f32")
	out := fs.String("o", "", "write the raw samples to this file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("decode-audio needs exactly one input file (.aac, or an MP4 file with an audio track)")
	}
	sf, err := parseSampleFormat(*formatName)
	if err != nil {
		return err
	}
	src, closer, err := openAudioInput(fs.Arg(0))
	if err != nil {
		return err
	}
	defer closer.Close()
	w, closeOut, err := openOutput(*out)
	if err != nil {
		return err
	}
	defer closeOut()

	ctx := context.Background()
	format := src.Format()
	dec, err := hwmediacodec.NewAudioDecoder(ctx, format, hwmediacodec.WithSampleFormat(sf))
	if err != nil {
		return err
	}
	defer dec.Close()
	packets, frames, samples := 0, 0, 0
	rate, channels := 0, 0
	drain := func() error {
		for {
			f, err := dec.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			frames++
			samples += f.Samples()
			rate, channels = f.SampleRate, f.Channels
			if _, err := w.Write(f.Data); err != nil {
				return err
			}
		}
	}
	for {
		p, err := src.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		packets++
		for {
			err := dec.Send(ctx, p)
			if errors.Is(err, hwmediacodec.ErrAgain) {
				if err := drain(); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return fmt.Errorf("packet %d: %w", packets-1, err)
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
	fmt.Fprintf(os.Stderr, "decoded %s: %d packets, %d frames, %d samples at %d Hz with %d channels (%s), delay %d\n",
		format.Codec, packets, frames, samples, rate, channels, sf, format.Delay)
	return nil
}

// encodeAudio encodes raw interleaved 16-bit samples into an .aac file
// (ADTS) or an MP4 file with one audio track.
func encodeAudio(args []string) error {
	fs := flag.NewFlagSet("encode-audio", flag.ExitOnError)
	codecName := fs.String("codec", "aac", "aac or opus")
	rate := fs.Int("rate", 48000, "sample rate of the input")
	channels := fs.Int("channels", 2, "channel count of the input")
	bitrate := fs.String("bitrate", "", "target bitrate, for example 128k")
	cbr := fs.Bool("cbr", false, "constant bitrate")
	out := fs.String("o", "", "output file: .aac for ADTS, anything else for MP4 (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *out == "" {
		return errors.New("encode-audio needs -o and exactly one input file of raw 16-bit samples")
	}
	c := hwmediacodec.AAC
	switch strings.ToLower(*codecName) {
	case "aac":
	case "opus":
		c = hwmediacodec.Opus
	default:
		return fmt.Errorf("unknown audio codec %q (aac or opus)", *codecName)
	}
	var opts []hwmediacodec.AudioEncoderOption
	if *bitrate != "" {
		b, err := parseBitrate(*bitrate)
		if err != nil {
			return err
		}
		opts = append(opts, hwmediacodec.WithBitrate(b))
	}
	if *cbr {
		opts = append(opts, hwmediacodec.WithRateControl(hwmediacodec.CBR))
	}
	pcm, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	ctx := context.Background()
	enc, err := hwmediacodec.NewAudioEncoder(ctx, c, *rate, *channels, opts...)
	if err != nil {
		return err
	}
	defer enc.Close()
	format := enc.Format()

	var sink hwmediacodec.PacketWriter
	var finish func() error
	if strings.EqualFold(filepath.Ext(*out), ".aac") {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		w, err := adts.NewWriter(f, format)
		if err != nil {
			f.Close()
			return err
		}
		sink, finish = w, f.Close
	} else {
		m, err := mp4.Create(*out)
		if err != nil {
			return err
		}
		aw, err := m.AddAudioTrack(format, uint32(*rate))
		if err != nil {
			m.Close()
			return err
		}
		sink, finish = aw, m.Close
	}
	packets, bytes := 0, 0
	drain := func() error {
		for {
			p, err := enc.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			packets++
			bytes += len(p.Data)
			if err := sink.WritePacket(p); err != nil {
				return err
			}
		}
	}
	unit := *channels * 2
	pcm = pcm[:len(pcm)/unit*unit]
	const piece = 4800
	var pts int64
	for at := 0; at < len(pcm); {
		end := min(at+piece*unit, len(pcm))
		f := &hwmediacodec.AudioFrame{SampleRate: *rate, Channels: *channels, Format: hwmediacodec.S16, Data: pcm[at:end], PTS: pts}
		err := enc.Send(ctx, f)
		if errors.Is(err, hwmediacodec.ErrAgain) {
			if err := drain(); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := drain(); err != nil {
			return err
		}
		pts += int64((end - at) / unit)
		at = end
	}
	if err := enc.Flush(ctx); err != nil {
		return err
	}
	if err := drain(); err != nil {
		return err
	}
	if err := finish(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "encoded %s: %d samples at %d Hz with %d channels into %d packets, %d bytes, delay %d, frames of %d\n",
		format.Codec, len(pcm)/unit, *rate, *channels, packets, bytes, format.Delay, format.FrameSamples)
	return nil
}
