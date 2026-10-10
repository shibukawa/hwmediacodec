package ivf_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/ivf"
)

var frames = [][]byte{
	{0x12, 0x00, 0x0a, 0x01, 0x00},
	{0x12, 0x00},
	bytes.Repeat([]byte{0xab}, 1000),
}

func TestRoundTripSeekable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.ivf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := ivf.NewWriter(f, "AV01", 320, 240, 1, 30)
	for i, fr := range frames {
		if err := w.WriteFrame(fr, uint64(i)*2); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := ivf.HeaderSize + 3*12 + 5 + 2 + 1000; len(data) != want {
		t.Fatalf("file is %d bytes, want %d", len(data), want)
	}
	r, err := ivf.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	h := r.Header()
	if h != (ivf.Header{FourCC: "AV01", Width: 320, Height: 240, TimebaseNum: 1, TimebaseDen: 30, FrameCount: 3}) {
		t.Fatalf("header %+v", h)
	}
	for i, want := range frames {
		fr, pts, err := r.Next()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if !bytes.Equal(fr, want) || pts != uint64(i)*2 {
			t.Fatalf("frame %d: %d bytes pts %d", i, len(fr), pts)
		}
	}
	if _, _, err := r.Next(); err != io.EOF {
		t.Fatalf("after the last frame Next = %v, want io.EOF", err)
	}
	if _, _, err := r.Next(); err != io.EOF {
		t.Fatalf("second Next after EOF = %v, want io.EOF", err)
	}
}

func TestRoundTripStream(t *testing.T) {
	var buf bytes.Buffer
	w := ivf.NewWriter(&buf, "VP90", 64, 48, 1001, 30000)
	for _, fr := range frames {
		if err := w.WriteFrame(fr, 7); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := ivf.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if h := r.Header(); h.FourCC != "VP90" || h.Width != 64 || h.Height != 48 || h.TimebaseNum != 1001 || h.TimebaseDen != 30000 || h.FrameCount != 0 {
		t.Fatalf("header %+v (a non-seekable writer cannot patch the frame count)", h)
	}
	n := 0
	for {
		_, pts, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil || pts != 7 {
			t.Fatal(pts, err)
		}
		n++
	}
	if n != 3 {
		t.Fatalf("read %d frames, want 3", n)
	}
}

func TestEmptyFile(t *testing.T) {
	var buf bytes.Buffer
	if err := ivf.NewWriter(&buf, "AV01", 1, 1, 1, 1).Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != ivf.HeaderSize {
		t.Fatalf("empty file is %d bytes", buf.Len())
	}
	r, err := ivf.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Next(); err != io.EOF {
		t.Fatalf("Next = %v, want io.EOF", err)
	}
}

func TestErrors(t *testing.T) {
	if _, err := ivf.NewReader(bytes.NewReader([]byte("DKIF"))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short header: %v", err)
	}
	if _, err := ivf.NewReader(bytes.NewReader(make([]byte, ivf.HeaderSize))); err == nil || !strings.Contains(err.Error(), "DKIF") {
		t.Errorf("bad signature: %v", err)
	}
	var buf bytes.Buffer
	w := ivf.NewWriter(&buf, "AV01", 1, 1, 1, 1)
	if err := w.WriteFrame(frames[2], 0); err != nil {
		t.Fatal(err)
	}
	truncated := buf.Bytes()[:buf.Len()-1]
	r, err := ivf.NewReader(bytes.NewReader(truncated))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated frame: %v", err)
	}
	r, err = ivf.NewReader(bytes.NewReader(buf.Bytes()[:ivf.HeaderSize+5]))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated frame header: %v", err)
	}
	if err := ivf.NewWriter(&buf, "AV1", 1, 1, 1, 1).Close(); err == nil {
		t.Error("a three-byte fourcc was accepted")
	}
	if err := ivf.NewWriter(&buf, "AV01", 70000, 1, 1, 1).Close(); err == nil {
		t.Error("a width above 65535 was accepted")
	}
}

// TestFFmpegInterop reads an IVF file ffmpeg wrote and checks that a copy
// written by this package is read back by ffprobe with the same frame
// count and timebase.
func TestFFmpegInterop(t *testing.T) {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not found in PATH")
	}
	path := testutil.GenerateAV1(t, 96, 64, 12)
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	r, err := ivf.NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	h := r.Header()
	if h.FourCC != "AV01" || h.Width != 96 || h.Height != 64 || h.FrameCount != 12 || h.TimebaseNum != 1 || h.TimebaseDen != 30 {
		t.Fatalf("ffmpeg's header read as %+v", h)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.ivf")
	dst, err := os.Create(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	w := ivf.NewWriter(dst, h.FourCC, h.Width, h.Height, h.TimebaseNum, h.TimebaseDen)
	n := 0
	for {
		fr, pts, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if pts != uint64(n) {
			t.Fatalf("frame %d has pts %d", n, pts)
		}
		if err := w.WriteFrame(fr, pts); err != nil {
			t.Fatal(err)
		}
		n++
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	if n != 12 {
		t.Fatalf("read %d frames, want 12", n)
	}
	orig, _ := os.ReadFile(path)
	dup, _ := os.ReadFile(copyPath)
	if !bytes.Equal(orig, dup) {
		t.Fatal("the copy differs from ffmpeg's file")
	}
	out, err := exec.Command(ffprobe, "-hide_banner", "-loglevel", "error", "-f", "ivf", "-i", copyPath, "-select_streams", "v:0",
		"-count_packets", "-show_entries", "stream=nb_read_packets,time_base,width,height", "-of", "csv=p=0").Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "96,64,1/30,12" {
		t.Fatalf("ffprobe read the copy as %q", got)
	}
}
