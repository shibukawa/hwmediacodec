package main

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// Playlist keeps a sliding window of fMP4 segments in memory and serves
// them as a live HLS stream: /index.m3u8, /init.mp4, /seg_N.m4s and a
// player page at /.
type Playlist struct {
	window int
	target time.Duration

	mu    sync.RWMutex
	init  []byte
	segs  []mp4.Segment
	ended bool
}

// NewPlaylist keeps the last window segments.
func NewPlaylist(window int, target time.Duration) *Playlist {
	if window < 3 {
		window = 3
	}
	return &Playlist{window: window, target: target}
}

// SetInit stores the init segment; it is the Segmenter's onInit.
func (p *Playlist) SetInit(data []byte) error {
	p.mu.Lock()
	p.init = data
	p.mu.Unlock()
	return nil
}

// Add appends a media segment; it is the Segmenter's onSegment.
func (p *Playlist) Add(s mp4.Segment) error {
	p.mu.Lock()
	p.segs = append(p.segs, s)
	if len(p.segs) > p.window {
		p.segs = p.segs[len(p.segs)-p.window:]
	}
	p.mu.Unlock()
	return nil
}

// End marks the stream finished (EXT-X-ENDLIST), turning it into a VOD
// playlist of the remaining window.
func (p *Playlist) End() {
	p.mu.Lock()
	p.ended = true
	p.mu.Unlock()
}

// M3U8 renders the media playlist.
func (p *Playlist) M3U8() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var b strings.Builder
	target := p.target
	for _, s := range p.segs {
		if s.Duration > target {
			target = s.Duration
		}
	}
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:%d\n", int(math.Ceil(target.Seconds())))
	seq := 0
	if len(p.segs) > 0 {
		seq = p.segs[0].Seq
	}
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-INDEPENDENT-SEGMENTS\n#EXT-X-MAP:URI=\"init.mp4\"\n", seq)
	for _, s := range p.segs {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg_%d.m4s\n", s.Duration.Seconds(), s.Seq)
	}
	if p.ended {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return b.String()
}

// ServeHTTP implements http.Handler.
func (p *Playlist) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	switch {
	case r.URL.Path == "/" || r.URL.Path == "/index.html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(indexHTML))
	case r.URL.Path == "/index.m3u8":
		p.mu.RLock()
		ready := p.init != nil && len(p.segs) > 0
		p.mu.RUnlock()
		if !ready {
			http.Error(w, "no segments yet", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte(p.M3U8()))
	case r.URL.Path == "/init.mp4":
		p.mu.RLock()
		data := p.init
		p.mu.RUnlock()
		if data == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Write(data)
	case strings.HasPrefix(r.URL.Path, "/seg_") && strings.HasSuffix(r.URL.Path, ".m4s"):
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/seg_"), ".m4s"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		p.mu.RLock()
		var data []byte
		for _, s := range p.segs {
			if s.Seq == n {
				data = s.Data
				break
			}
		}
		p.mu.RUnlock()
		if data == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/iso.segment")
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Write(data)
	default:
		http.NotFound(w, r)
	}
}

const indexHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>hwmediacodec HLS</title>
<style>body{margin:0;background:#111;color:#ddd;font:14px system-ui}video{width:100vw;max-height:90vh;background:#000}p{margin:8px}</style>
</head><body>
<video id="v" controls autoplay muted playsinline></video>
<p id="s">loading…</p>
<script src="https://cdn.jsdelivr.net/npm/hls.js@1.5.17/dist/hls.min.js"></script>
<script>
const v = document.getElementById('v'), s = document.getElementById('s'), src = 'index.m3u8';
const forceHlsjs = location.search.includes('hlsjs');      // ?hlsjs forces the MSE path
let player = '';
if (!forceHlsjs && v.canPlayType('application/vnd.apple.mpegurl')) { // Safari: native HLS
  v.src = src; player = 'native HLS';
} else if (window.Hls && Hls.isSupported()) {                 // everyone else: hls.js over MSE
  const h = new Hls({liveSyncDurationCount: 2, maxBufferLength: 4});
  h.loadSource(src); h.attachMedia(v); player = 'hls.js';
  h.on(Hls.Events.ERROR, (e, d) => { player = 'hls.js error: ' + d.details; });
} else {
  player = 'this browser can play neither native HLS nor MSE';
}
setInterval(() => { s.textContent = player + '  position ' + v.currentTime.toFixed(1) + ' s' + (v.paused ? '  (paused)' : ''); }, 500);
</script></body></html>
`
