// Command check tries everything hwmediacodec does on this machine and
// writes a report to paste into a GitHub issue:
//
//	go run github.com/shibukawa/hwmediacodec/examples/check@latest
//	go run ./check -auto        # the automatic checks only, no window
//
// It runs in three stages in one window:
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
// link to a new GitHub issue is shown; the issue body is pasted by hand.
// S skips the current clip or stage, Esc ends the run and saves what was
// checked; -playback=false and -webrtc=false leave a stage out.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

func main() {
	auto := flag.Bool("auto", false, "run the automatic checks only, without a window, and print the report")
	out := flag.String("o", "hwmediacodec-report.md", "file the report is saved to")
	addr := flag.String("addr", "127.0.0.1:0", "listen address of the WebRTC page (port 0 picks a free one)")
	open := flag.Bool("open", true, "open the WebRTC page in the default browser")
	play := flag.Duration("play", 6*time.Second, "how long each clip plays before the question")
	playback := flag.Bool("playback", true, "run the playback stage")
	webrtc := flag.Bool("webrtc", true, "run the WebRTC stage")
	flag.Parse()

	r := newReport()
	k := &checker{r: r}
	if *auto {
		k.run(context.Background())
		os.Exit(finish(r, *out))
	}

	g := newGame(r, *out, *addr, *open, *play)
	g.skipPlayback, g.skipWebRTC = !*playback, !*webrtc
	go func() {
		k.run(context.Background())
		g.autoDone.Store(true)
	}()
	ebiten.SetWindowSize(winW, winH)
	ebiten.SetWindowTitle("hwmediacodec check")
	ebiten.SetTPS(webFPS)
	err := ebiten.RunGame(g)
	g.close()
	code := 0
	if !g.saved {
		code = finish(r, *out)
	} else if _, fail := r.counts(); fail > 0 {
		code = 1
	}
	if err != nil {
		log.Fatal(err)
	}
	os.Exit(code)
}

// finish prints and saves the report and returns the exit code: 1 when a
// check failed.
func finish(r *report, path string) int {
	md := r.markdown()
	fmt.Println(md)
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "check: saving the report:", err)
	} else if abs, err := filepath.Abs(path); err == nil {
		fmt.Println("Report saved to", abs)
	}
	fmt.Println("Please open an issue with it (paste the report as the body):")
	fmt.Println(r.issueURL())
	if _, fail := r.counts(); fail > 0 {
		return 1
	}
	return 0
}
