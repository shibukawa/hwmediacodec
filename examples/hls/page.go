package main

import (
	"net/http"

	"github.com/shibukawa/hwmediacodec/net/hls"
)

// withPage serves the player page at / and leaves everything else (the
// playlist and the segments) to the hls.Playlist.
func withPage(p *hls.Playlist) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(indexHTML))
			return
		}
		p.ServeHTTP(w, r)
	})
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
