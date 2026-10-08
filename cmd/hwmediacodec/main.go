// Command hwmediacodec probes the hardware codecs on this machine and decodes
// raw Annex-B elementary streams with them.
//
//	hwmediacodec probe
//	hwmediacodec decode [-codec h264|hevc] [-o out.nv12] [-hash] file.h264
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/annexb"
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
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hwmediacodec:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:\n  hwmediacodec probe\n  hwmediacodec decode [-codec h264|hevc] [-o out.nv12] [-hash] file")
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

func decode(args []string) error {
	fs := flag.NewFlagSet("decode", flag.ExitOnError)
	codecName := fs.String("codec", "h264", "input codec: h264 or hevc")
	out := fs.String("o", "", "write decoded NV12 frames to this file")
	hash := fs.Bool("hash", false, "print the SHA-256 of every frame")
	software := fs.Bool("software", false, "allow the OS software decoder when no hardware engine exists")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("decode needs exactly one input file")
	}
	var c hwmediacodec.Codec
	switch *codecName {
	case "h264":
		c = hwmediacodec.H264
	case "hevc", "h265":
		c = hwmediacodec.HEVC
	default:
		return fmt.Errorf("unknown codec %q", *codecName)
	}

	in, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer in.Close()

	var w io.Writer = io.Discard
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}

	ctx := context.Background()
	var opts []hwmediacodec.DecoderOption
	if *software {
		opts = append(opts, hwmediacodec.WithSoftwareFallback())
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
		rows := []int{f.Height, (f.Height + 1) / 2}
		rowBytes := []int{f.Width, (f.Width + 1) / 2 * 2}
		for i, p := range f.Planes {
			for r := 0; r < rows[i]; r++ {
				row := p[r*f.Strides[i] : r*f.Strides[i]+rowBytes[i]]
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

	r := annexb.NewReader(in, c)
	var pts int64
	for {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := dec.Send(ctx, hwmediacodec.Packet{Data: au, PTS: pts}); err != nil {
			return fmt.Errorf("send: %w", err)
		}
		pts += 3000
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
	elapsed := time.Since(start)
	fmt.Fprintf(os.Stderr, "decoded %d frames (%dx%d) in %s (%.1f fps)\n", frames, width, height, elapsed.Round(time.Millisecond), float64(frames)/elapsed.Seconds())
	return nil
}
