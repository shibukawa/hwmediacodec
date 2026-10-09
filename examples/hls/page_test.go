package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec/mediacontainer/hls"
)

// TestPlayerPage checks the sample's own part: the page at / and that
// every other path reaches the playlist (which has no segment yet).
func TestPlayerPage(t *testing.T) {
	h := withPage(hls.NewPlaylist(6, time.Second))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "hls.js") {
		t.Errorf("player page: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.m3u8", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("empty playlist answered %d, want 503", rec.Code)
	}
}
