package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec/net/webrtc"
)

// TestPlayerPage checks the sample's own part: the page at / and that the
// page's POST to /offer reaches the broadcaster (which rejects an empty
// offer).
func TestPlayerPage(t *testing.T) {
	bc := webrtc.NewBroadcaster(30, nil, func() {})
	defer bc.Close()
	h := withPage(bc)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "RTCPeerConnection") {
		t.Errorf("player page: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/offer", strings.NewReader("not json")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a malformed offer answered %d, want 400", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nothing", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("an unknown path answered %d, want 404", rec.Code)
	}
}
