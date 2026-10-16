// Command audiocmp is the host half of the audio checks that run a codec
// somewhere else (a phone over adb, a Windows machine): it makes the test
// signal and compares raw 16-bit samples that came back with a reference.
//
//	go run ./internal/audiocmp -make -rate 48000 -channels 2 -seconds 2 > signal.pcm
//	go run ./internal/audiocmp -channels 2 reference.pcm decoded.pcm
//
// The comparison fails (exit status 1) unless the two files hold the same
// number of samples (-prefix: at least as many as the reference), line up
// without a shift and agree to -min-snr dB.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

func main() {
	produce := flag.Bool("make", false, "write the test signal to standard output instead of comparing")
	rate := flag.Int("rate", 48000, "sample rate of the signal to make")
	channels := flag.Int("channels", 2, "channel count")
	seconds := flag.Float64("seconds", 2, "length of the signal to make")
	skip := flag.Int("skip", 0, "samples per channel to leave out at the start of both files")
	prefix := flag.Bool("prefix", false, "the second file may be longer than the reference")
	minSNR := flag.Float64("min-snr", 20, "the signal-to-noise ratio to ask for, in dB")
	flag.Parse()
	if *produce {
		pcm := testutil.AudioSignal(*rate, *channels, int(float64(*rate)**seconds))
		if _, err := os.Stdout.Write(testutil.PCMBytes(pcm)); err != nil {
			fmt.Fprintln(os.Stderr, "audiocmp:", err)
			os.Exit(1)
		}
		return
	}
	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}
	read := func(path string) []int16 {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "audiocmp:", err)
			os.Exit(1)
		}
		return testutil.PCMSamples(b)
	}
	ref, got := read(flag.Arg(0)), read(flag.Arg(1))
	nRef, nGot := len(ref) / *channels, len(got) / *channels
	ok := nGot == nRef || (*prefix && nGot >= nRef)
	if *prefix && nGot > nRef {
		got = got[:len(ref)]
	}
	from := min(*skip**channels, len(ref), len(got))
	shift, snr := testutil.AudioAlignment(ref[from:], got[from:], *channels, 8)
	fmt.Printf("%d samples for %d, %.1f dB at shift %d\n", nGot, nRef, snr, shift)
	if !ok || shift != 0 || snr < *minSNR {
		os.Exit(1)
	}
}
