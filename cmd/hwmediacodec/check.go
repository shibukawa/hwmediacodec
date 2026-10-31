package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shibukawa/hwmediacodec/cmd/hwmediacodec/checker"
)

// checkOptions are the flags of the check command.
type checkOptions struct {
	out      string
	addr     string
	open     bool
	play     time.Duration
	playback bool
	webrtc   bool
}

// check tries everything hwmediacodec does on this machine and writes a
// report for a GitHub issue. It runs in three stages in one window:
//
//  1. Automatic checks, once per backend that reports the codec: decode the
//     bundled waterfall clip in H.264, HEVC and AV1 (every frame, in display
//     order); encode pictures of it in each codec and decode them back,
//     comparing the PSNR; decode the clip's AAC track; encode a tone in AAC
//     and Opus and decode it back.
//  2. Playback: the clip plays in every codec this machine decodes, with
//     sound, and asks whether picture and sound were right (Y or N).
//  3. WebRTC: the fireworks show is encoded in H.264 and HEVC and streamed
//     to a page that opens in the browser; the page reports what it
//     received and asks whether the picture is right.
//
// At the end the report is printed, saved as hwmediacodec-report.md and a
// link opens a new GitHub issue with the report filled in; nothing is sent
// until it is submitted there. S skips the current clip or stage, Esc ends
// the run and saves what was checked; -playback=false and -webrtc=false
// leave a stage out, and -auto runs the automatic checks only, without a
// window (the only mode where Ebitengine has no window). It returns the
// exit status: 1 when a check failed.
func check(args []string) int {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	auto := fs.Bool("auto", false, "run the automatic checks only, without a window, and print the report")
	var o checkOptions
	fs.StringVar(&o.out, "o", "hwmediacodec-report.md", "file the report is saved to")
	fs.StringVar(&o.addr, "addr", "127.0.0.1:0", "listen address of the WebRTC page (port 0 picks a free one)")
	fs.BoolVar(&o.open, "open", true, "open the WebRTC page in the default browser")
	fs.DurationVar(&o.play, "play", 6*time.Second, "how long each clip plays before the question")
	fs.BoolVar(&o.playback, "playback", true, "run the playback stage")
	fs.BoolVar(&o.webrtc, "webrtc", true, "run the WebRTC stage")
	fs.Parse(args)

	r := checker.NewReport()
	if *auto || !hasWindow {
		if !*auto {
			fmt.Fprintln(os.Stderr, "check: no window on this platform; running the automatic checks only")
		}
		(&checker.Checker{R: r}).Run(context.Background())
		return finishCheck(r, o.out)
	}
	return checkWindow(r, o)
}

// finishCheck prints and saves the report and returns the exit status: 1
// when a check failed.
func finishCheck(r *checker.Report, path string) int {
	md := r.Markdown()
	fmt.Println(md)
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "check: saving the report:", err)
	} else if abs, err := filepath.Abs(path); err == nil {
		fmt.Println("Report saved to", abs)
	}
	link, full := r.IssueURL()
	if full {
		fmt.Println("Please open an issue with it; this link fills the report in:")
	} else {
		fmt.Println("Please open an issue with it (paste the report from the file as the body):")
	}
	fmt.Println(link)
	if _, fail := r.Counts(); fail > 0 {
		return 1
	}
	return 0
}
