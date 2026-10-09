package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/shibukawa/hwmediacodec"
)

// h264Codec is what the track offers. Constrained Baseline with
// packetization-mode 1 is accepted by every browser; the actual profile of
// the bitstream is what the encoder was asked for.
var h264Codec = webrtc.RTPCodecCapability{
	MimeType:    webrtc.MimeTypeH264,
	ClockRate:   90000,
	SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
}

type peer struct {
	pc      *webrtc.PeerConnection
	track   *webrtc.TrackLocalStaticSample
	started bool // true once a keyframe has been sent
}

// Broadcaster fans one encoded stream out to every connected browser. It
// is an http.Handler for the player page (/) and the signalling endpoint
// (POST /offer with the browser's SDP, answered with ours) and a
// screencast.Sink for the packets.
type Broadcaster struct {
	fps             float64
	iceServers      []string
	requestKeyframe func()

	mu      sync.Mutex
	peers   map[*peer]struct{}
	lastPTS int64
	closed  bool
}

// NewBroadcaster creates a broadcaster for a stream of fps frames per
// second. requestKeyframe is called when a new viewer joins or a viewer
// reports a picture loss; wire it to Recorder.RequestKeyframe.
// iceServers lists STUN/TURN URLs, empty for LAN use.
func NewBroadcaster(fps float64, iceServers []string, requestKeyframe func()) *Broadcaster {
	return &Broadcaster{fps: fps, iceServers: iceServers, requestKeyframe: requestKeyframe, peers: map[*peer]struct{}{}, lastPTS: -1}
}

// Viewers is the number of connected peers.
func (b *Broadcaster) Viewers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.peers)
}

// ServeHTTP implements http.Handler.
func (b *Broadcaster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/" || r.URL.Path == "/index.html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(indexHTML))
	case r.URL.Path == "/offer" && r.Method == http.MethodPost:
		var offer webrtc.SessionDescription
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&offer); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		answer, err := b.Accept(offer)
		if err != nil {
			log.Println("webrtc: offer rejected:", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(answer)
	default:
		http.NotFound(w, r)
	}
}

// Accept takes a viewer's offer, adds a video track and returns the
// answer once ICE candidates are gathered (so no trickle ICE is needed).
func (b *Broadcaster) Accept(offer webrtc.SessionDescription) (*webrtc.SessionDescription, error) {
	cfg := webrtc.Configuration{}
	if len(b.iceServers) > 0 {
		cfg.ICEServers = []webrtc.ICEServer{{URLs: b.iceServers}}
	}
	pc, err := webrtc.NewPeerConnection(cfg)
	if err != nil {
		return nil, err
	}
	track, err := webrtc.NewTrackLocalStaticSample(h264Codec, "video", "hwmediacodec")
	if err != nil {
		pc.Close()
		return nil, err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		pc.Close()
		return nil, err
	}
	p := &peer{pc: pc, track: track}

	// Picture loss or full intra requests from the viewer become
	// keyframe requests to the encoder.
	go func() {
		for {
			pkts, _, err := sender.ReadRTCP()
			if err != nil {
				return
			}
			for _, pkt := range pkts {
				switch pkt.(type) {
				case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
					b.requestKeyframe()
				}
			}
		}
	}()
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		switch s {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateDisconnected:
			b.remove(p)
		}
	})

	if err := pc.SetRemoteDescription(offer); err != nil {
		pc.Close()
		return nil, err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return nil, err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return nil, err
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		pc.Close()
		return nil, errors.New("ICE gathering timed out")
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		pc.Close()
		return nil, errors.New("broadcaster is closed")
	}
	b.peers[p] = struct{}{}
	b.mu.Unlock()
	b.requestKeyframe() // the new viewer can only start at a keyframe
	return pc.LocalDescription(), nil
}

func (b *Broadcaster) remove(p *peer) {
	b.mu.Lock()
	_, ok := b.peers[p]
	delete(b.peers, p)
	b.mu.Unlock()
	if ok {
		p.pc.Close()
	}
}

// WritePacket implements screencast.Sink: one Annex-B access unit goes to
// every viewer. pion's H.264 payloader splits the NAL units into RTP
// packets (STAP-A for the parameter sets, FU-A for large slices).
func (b *Broadcaster) WritePacket(pkt hwmediacodec.Packet) error {
	dur := time.Duration(float64(time.Second) / b.fps)
	b.mu.Lock()
	if b.lastPTS >= 0 && pkt.PTS > b.lastPTS {
		dur = time.Duration(pkt.PTS-b.lastPTS) * time.Second / 90000
	}
	b.lastPTS = pkt.PTS
	peers := make([]*peer, 0, len(b.peers))
	for p := range b.peers {
		peers = append(peers, p)
	}
	b.mu.Unlock()
	for _, p := range peers {
		if !p.started {
			if !pkt.Keyframe {
				continue
			}
			p.started = true
		}
		if err := p.track.WriteSample(media.Sample{Data: pkt.Data, Duration: dur}); err != nil {
			log.Println("webrtc: write sample:", err)
			b.remove(p)
		}
	}
	return nil
}

// Close implements screencast.Sink: it disconnects every viewer.
func (b *Broadcaster) Close() error {
	b.mu.Lock()
	b.closed = true
	peers := b.peers
	b.peers = map[*peer]struct{}{}
	b.mu.Unlock()
	for p := range peers {
		p.pc.Close()
	}
	return nil
}

func (b *Broadcaster) String() string {
	return fmt.Sprintf("%d viewer(s)", b.Viewers())
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
