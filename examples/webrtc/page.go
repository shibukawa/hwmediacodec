package main

import (
	"net/http"

	"github.com/shibukawa/hwmediacodec/net/webrtc"
)

// withPage serves the player page at / and hands the signalling (the
// page's POST to /offer) to the Broadcaster.
func withPage(bc *webrtc.Broadcaster) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /offer", bc)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(indexHTML))
	})
	return mux
}

const indexHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>hwmediacodec WebRTC</title>
<style>body{margin:0;background:#111;color:#ddd;font:14px system-ui}video{width:100vw;max-height:90vh;background:#000}p{margin:8px}</style>
</head><body>
<video id="v" autoplay muted playsinline controls></video>
<p id="s">connecting…</p>
<script>
const v = document.getElementById('v'), s = document.getElementById('s');
async function start() {
  const pc = new RTCPeerConnection();
  window.pc = pc; // for pc.getStats() in the console
  pc.addTransceiver('video', {direction: 'recvonly'});
  pc.ontrack = e => { v.srcObject = e.streams[0]; };
  pc.onconnectionstatechange = () => { s.textContent = pc.connectionState; };
  await pc.setLocalDescription(await pc.createOffer());
  // Wait for ICE gathering so that the offer carries our candidates (no trickle).
  await new Promise(res => {
    if (pc.iceGatheringState === 'complete') return res();
    const t = setTimeout(res, 1000);
    pc.onicegatheringstatechange = () => { if (pc.iceGatheringState === 'complete') { clearTimeout(t); res(); } };
  });
  const r = await fetch('offer', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(pc.localDescription)});
  if (!r.ok) { s.textContent = 'offer rejected: ' + await r.text(); return; }
  await pc.setRemoteDescription(await r.json());
  setInterval(() => {
    const q = v.getVideoPlaybackQuality ? v.getVideoPlaybackQuality() : null;
    s.textContent = pc.connectionState + (q ? '  frames ' + q.totalVideoFrames + '  dropped ' + q.droppedVideoFrames : '') + '  ' + v.videoWidth + 'x' + v.videoHeight;
  }, 500);
}
start().catch(e => { s.textContent = 'error: ' + e; });
</script></body></html>
`
