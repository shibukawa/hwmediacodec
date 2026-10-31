//go:build (windows || darwin || linux) && !android && !ios

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/cmd/hwmediacodec/checker"
)

func TestWrap(t *testing.T) {
	got := wrap("Report saved to ./hwmediacodec-report.md (also printed)", 20)
	want := []string{"Report saved to", "./hwmediacodec-repor", "t.md (also printed)"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrap: %q", got)
	}
}

func TestWebResults(t *testing.T) {
	r := checker.NewReport()
	s := &stream{codec: hwmediacodec.HEVC, key: "hevc", backend: "nvidia", rows: map[string]*rows{}}
	s.first = &rows{
		received: r.Add("WebRTC", s.name("stream received by", "the browser")),
		picture:  r.Add("WebRTC", s.name("picture in", "the browser")),
	}
	w := &web{r: r, streams: []*stream{s}, answered: map[string]bool{}}
	const chrome = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"
	const edge = chrome + " Edg/156.0.0.0"

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
	if c := post(`{"codec":"hevc","frames":90,"width":640,"height":360,"mimeType":"video/H265","userAgent":"` + chrome + `"}`); c != http.StatusNoContent {
		t.Fatalf("stats answered %d", c)
	}
	first := s.first.received
	if first.Status != checker.Passed || !strings.Contains(first.Detail, "90 frames 640x360") ||
		first.Name != "HEVC stream received by Chrome 156 / nvidia" || len(r.Browsers()) != 1 {
		t.Errorf("received: %+v, browsers %q", *first, r.Browsers())
	}
	if w.done() {
		t.Error("done before the answer")
	}
	post(`{"codec":"hevc","answer":"no","userAgent":"` + chrome + `"}`)
	if s.first.picture.Status != checker.Failed || s.first.picture.Name != "HEVC picture in Chrome 156 / nvidia" || !w.done() {
		t.Errorf("picture: %+v", *s.first.picture)
	}

	// A second browser gets rows of its own and leaves the first one's.
	post(`{"codec":"hevc","error":"no frame decoded","userAgent":"` + edge + `"}`)
	post(`{"codec":"hevc","answer":"no","userAgent":"` + edge + `"}`)
	md := r.Markdown()
	for _, want := range []string{
		"| HEVC stream received by Chrome 156 / nvidia | pass |",
		"| HEVC picture in Chrome 156 / nvidia | FAIL | answered no in Chrome 156 |",
		"| HEVC stream received by Edge 156 / nvidia | FAIL | no frame decoded |",
		"| HEVC picture in Edge 156 / nvidia | FAIL | answered no in Edge 156 |",
		"- Browser: " + chrome + "\n- Browser: " + edge + "\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q:\n%s", want, md)
		}
	}
}
