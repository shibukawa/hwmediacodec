package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec"
)

func TestReportMarkdown(t *testing.T) {
	r := newReport()
	r.caps = []hwmediacodec.Capability{
		{Backend: "mediafoundation", Codec: hwmediacodec.H264, Direction: hwmediacodec.Decode, Hardware: true},
		{Backend: "mediafoundation", Codec: hwmediacodec.H264, Direction: hwmediacodec.Encode, Hardware: true},
		{Backend: "mediafoundation", Codec: hwmediacodec.AAC, Direction: hwmediacodec.Decode},
	}
	r.set(r.add("Encode", "H.264 encode / mediafoundation"), failed, "frame 3: a | b\nc")
	r.set(r.add("Decode", "H.264 decode / mediafoundation"), passed, "293 frames")
	md := r.markdown()
	for _, want := range []string{
		"## Check report: ", "(mediafoundation), 1 passed, 1 failed",
		"| mediafoundation | h264 | encode | true |",
		"| H.264 encode / mediafoundation | FAIL | frame 3: a \\| b c |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q:\n%s", want, md)
		}
	}
	if strings.Index(md, "### Decode") > strings.Index(md, "### Encode") {
		t.Error("sections out of order")
	}
	link, full := r.issueURL()
	if !full {
		t.Fatal("a short report does not fit the link")
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if q := u.Query(); len(q) != 2 || !strings.HasPrefix(q.Get("title"), "Check report: ") || q.Get("body") != md {
		t.Errorf("issue URL %s does not carry the title and the report", u)
	}
	for range 200 {
		r.set(r.add("Decode", "a long check name for a long report"), passed, "%s", strings.Repeat("detail ", 10))
	}
	if link, full := r.issueURL(); full || len(link) > maxIssueURL || !strings.Contains(link, "too+long") {
		t.Errorf("a long report gives a %d-byte link (full %v)", len(link), full)
	}
}

func TestWrap(t *testing.T) {
	got := wrap("Report saved to ./hwmediacodec-report.md (also printed)", 20)
	want := []string{"Report saved to", "./hwmediacodec-repor", "t.md (also printed)"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrap: %q", got)
	}
}

func TestHalve(t *testing.T) {
	const w, h = 720, 1280
	y := make([]byte, w*h)
	uv := make([]byte, w*h/2)
	for i := range y {
		y[i] = byte(i % w / 3)
	}
	for i := range uv {
		uv[i] = byte(i % 2 * 255)
	}
	f := &hwmediacodec.Frame{Width: w, Height: h, Format: hwmediacodec.NV12, Planes: [][]byte{y, uv}, Strides: []int{w, w}}
	s := halve(f, 2)
	if s.Width != srcW || s.Height != srcH || len(s.Planes[0]) != srcW*srcH || len(s.Planes[1]) != srcW*srcH/2 || s.PTS != 2*srcTick {
		t.Fatalf("halved to %dx%d with %d/%d bytes, PTS %d", s.Width, s.Height, len(s.Planes[0]), len(s.Planes[1]), s.PTS)
	}
	// The crop drops 4 columns on each side of the halved picture, so
	// column c averages source columns 2c+8 and 2c+9 of rows 0 and 1.
	if got, want := s.Planes[0][10], byte((2*int(y[28])+2*int(y[29])+2)/4); got != want {
		t.Errorf("luma %d, want %d", got, want)
	}
	if s.Planes[1][0] != 0 || s.Planes[1][1] != 255 {
		t.Errorf("chroma %d %d: U and V swapped", s.Planes[1][0], s.Planes[1][1])
	}
	if p := lumaPSNR(s, s); p != 99 {
		t.Errorf("PSNR of a picture with itself %.1f", p)
	}
	if synth := synthFrames(); len(synth) != srcFrames || flat(synth[0]) {
		t.Error("synthetic pictures are missing or flat")
	}
}

func TestWebResults(t *testing.T) {
	r := newReport()
	s := &stream{codec: hwmediacodec.HEVC, key: "hevc"}
	s.received = r.add("WebRTC", "HEVC stream received by the browser")
	s.picture = r.add("WebRTC", "HEVC picture in the browser")
	w := &web{r: r, streams: []*stream{s}, answered: map[string]bool{}}

	rec := httptest.NewRecorder()
	w.page(rec, httptest.NewRequest("GET", "/", nil))
	if body := rec.Body.String(); !strings.Contains(body, `data-codec="hevc"`) || !strings.Contains(body, "<h2>HEVC</h2>") {
		t.Fatalf("page:\n%s", body)
	}
	post := func(body string) int {
		rec := httptest.NewRecorder()
		w.result(rec, httptest.NewRequest("POST", "/result", bytes.NewBufferString(body)))
		return rec.Code
	}
	if c := post(`{"codec":"av1"}`); c != http.StatusBadRequest {
		t.Errorf("unknown codec answered %d", c)
	}
	if c := post(`{"codec":"hevc","frames":90,"width":640,"height":360,"mimeType":"video/H265","userAgent":"TestBrowser/1"}`); c != http.StatusNoContent {
		t.Fatalf("stats answered %d", c)
	}
	if s.received.Status != passed || !strings.Contains(s.received.Detail, "90 frames 640x360") || r.browser != "TestBrowser/1" {
		t.Errorf("received: %+v, browser %q", *s.received, r.browser)
	}
	if w.done() {
		t.Error("done before the answer")
	}
	post(`{"codec":"hevc","answer":"no"}`)
	if s.picture.Status != failed || !w.done() {
		t.Errorf("picture: %+v", *s.picture)
	}
}
