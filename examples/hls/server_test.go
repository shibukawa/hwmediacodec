package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/examples/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// feed runs the access units of an ffmpeg-made file through a Segmenter
// into the playlist, so the server is tested without a hardware encoder.
func feed(t *testing.T, p *Playlist, target time.Duration) string {
	t.Helper()
	src := testutil.GenerateMP4(t, t.TempDir(), testutil.MP4Options{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 150, BFrames: 0, GOP: 15})
	d, err := mp4.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	v := d.Video()
	seg, err := mp4.NewSegmenter(hwmediacodec.H264, v.TimeScale, target, p.SetInit, p.Add)
	if err != nil {
		t.Fatal(err)
	}
	for i := range v.SampleCount() {
		pkt, err := v.Packet(i)
		if err != nil {
			t.Fatal(err)
		}
		if err := seg.WritePacket(pkt); err != nil {
			t.Fatal(err)
		}
	}
	if err := seg.Close(); err != nil {
		t.Fatal(err)
	}
	return src
}

func get(t *testing.T, url string) (int, string, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), body
}

func TestPlaylistServesLiveHLS(t *testing.T) {
	testutil.RequireFFmpeg(t)
	p := NewPlaylist(10, time.Second)
	srv := httptest.NewServer(p)
	defer srv.Close()

	if code, _, _ := get(t, srv.URL+"/index.m3u8"); code != http.StatusServiceUnavailable {
		t.Errorf("empty playlist answered %d, want 503", code)
	}
	feed(t, p, time.Second) // 5 s of video, keyframes every 0.5 s: five 1 s segments
	code, ctype, body := get(t, srv.URL+"/index.m3u8")
	if code != 200 || ctype != "application/vnd.apple.mpegurl" {
		t.Fatalf("playlist: %d %s", code, ctype)
	}
	m3u8 := string(body)
	for _, want := range []string{"#EXTM3U", "#EXT-X-VERSION:7", "#EXT-X-TARGETDURATION:1", "#EXT-X-MEDIA-SEQUENCE:1", `#EXT-X-MAP:URI="init.mp4"`, "#EXTINF:1.000,\nseg_1.m4s", "seg_5.m4s"} {
		if !strings.Contains(m3u8, want) {
			t.Errorf("playlist lacks %q:\n%s", want, m3u8)
		}
	}
	if strings.Contains(m3u8, "#EXT-X-ENDLIST") {
		t.Error("live playlist carries ENDLIST")
	}
	if code, ctype, _ := get(t, srv.URL+"/init.mp4"); code != 200 || ctype != "video/mp4" {
		t.Errorf("init: %d %s", code, ctype)
	}
	if code, ctype, _ := get(t, srv.URL+"/seg_3.m4s"); code != 200 || ctype != "video/iso.segment" {
		t.Errorf("segment: %d %s", code, ctype)
	}
	if code, _, _ := get(t, srv.URL+"/seg_42.m4s"); code != 404 {
		t.Errorf("missing segment answered %d", code)
	}
	if code, ctype, body := get(t, srv.URL+"/"); code != 200 || !strings.HasPrefix(ctype, "text/html") || !strings.Contains(string(body), "hls.js") {
		t.Errorf("player page: %d %s", code, ctype)
	}

	// ffmpeg plays the finished stream end to end over HTTP.
	p.End()
	if !strings.Contains(p.M3U8(), "#EXT-X-ENDLIST") {
		t.Error("ended playlist lacks ENDLIST")
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-count_frames", "-show_entries", "stream=codec_name,nb_read_frames,width", "-of", "csv=p=0", srv.URL+"/index.m3u8").CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe over HLS: %v\n%s", err, out)
	}
	// ffprobe lists the stream once per program and once on its own.
	if got := strings.TrimSpace(string(out)); !strings.HasPrefix(got, "h264,160,150") {
		t.Errorf("ffprobe saw %q, want h264 160 wide 150 frames", got)
	}
}

func TestPlaylistSlidingWindow(t *testing.T) {
	testutil.RequireFFmpeg(t)
	p := NewPlaylist(3, time.Second)
	feed(t, p, time.Second)
	m3u8 := p.M3U8()
	if !strings.Contains(m3u8, "#EXT-X-MEDIA-SEQUENCE:3") || strings.Contains(m3u8, "seg_2.m4s") || !strings.Contains(m3u8, "seg_5.m4s") {
		t.Errorf("window of 3 after 5 segments:\n%s", m3u8)
	}
}
