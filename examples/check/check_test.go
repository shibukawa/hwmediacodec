package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/examples/internal/checker"
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
	s := &stream{codec: hwmediacodec.HEVC, key: "hevc"}
	s.received = r.Add("WebRTC", "HEVC stream received by the browser")
	s.picture = r.Add("WebRTC", "HEVC picture in the browser")
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
	if s.received.Status != checker.Passed || !strings.Contains(s.received.Detail, "90 frames 640x360") || r.Browser() != "TestBrowser/1" {
		t.Errorf("received: %+v, browser %q", *s.received, r.Browser())
	}
	if w.done() {
		t.Error("done before the answer")
	}
	post(`{"codec":"hevc","answer":"no"}`)
	if s.picture.Status != checker.Failed || !w.done() {
		t.Errorf("picture: %+v", *s.picture)
	}
}
