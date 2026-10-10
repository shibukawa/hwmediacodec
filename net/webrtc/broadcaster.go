// Package webrtc sends encoder output to browsers over WebRTC with pion.
// A Broadcaster fans one H.264 stream out to every connected viewer; it is
// the sink for the packets (a hwmediacodec.PacketWriteCloser, which is
// what capture.Recorder writes into) and an http.Handler for the
// signalling:
//
//	var rec *capture.Recorder
//	bc := pion.NewBroadcaster(60, nil, func() { rec.RequestKeyframe() })
//	rec, _ = capture.New(w, h, bc, capture.Options{FPS: 60, LowLatency: true,
//		Profile: hwmediacodec.ProfileBaseline})
//	http.Handle("/offer", bc) // the page POSTs its SDP offer, gets the answer
//
// Latency is that of the encoder and the network, around 100 ms on a LAN.
//
// The package is a module of its own
// (github.com/shibukawa/hwmediacodec/net/webrtc) so that the WebRTC stack
// stays out of the core module's dependencies.
package webrtc

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/pion/rtcp"
	pion "github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/shibukawa/hwmediacodec"
)

// h264Codec is what the track offers. Constrained Baseline with
// packetization-mode 1 is accepted by every browser; the actual profile of
// the bitstream is what the encoder was asked for.
var h264Codec = pion.RTPCodecCapability{
	MimeType:    pion.MimeTypeH264,
	ClockRate:   90000,
	SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
}

type peer struct {
	pc      *pion.PeerConnection
	track   *pion.TrackLocalStaticSample
	started bool // true once a keyframe has been sent
}

// Broadcaster fans one encoded H.264 stream out to every connected
// browser. It is a hwmediacodec.PacketWriteCloser for the packets and an
// http.Handler for
// the signalling (a POST with the browser's SDP offer as JSON, answered
// with ours). It is safe for concurrent use.
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
// reports a picture loss; wire it to Recorder.RequestKeyframe (a viewer
// only starts at a keyframe). iceServers lists STUN/TURN URLs, empty for
// LAN use.
func NewBroadcaster(fps float64, iceServers []string, requestKeyframe func()) *Broadcaster {
	return &Broadcaster{fps: fps, iceServers: iceServers, requestKeyframe: requestKeyframe, peers: map[*peer]struct{}{}, lastPTS: -1}
}

// Viewers is the number of connected peers.
func (b *Broadcaster) Viewers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.peers)
}

// ServeHTTP implements http.Handler: the signalling endpoint. A POST with
// a JSON session description (what RTCPeerConnection.localDescription
// serialises to) is answered with ours; the path is not looked at, so
// mount the handler where the page posts to.
func (b *Broadcaster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST an SDP offer", http.StatusMethodNotAllowed)
		return
	}
	var offer pion.SessionDescription
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&offer); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	answer, err := b.Accept(offer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(answer)
}

// Accept takes a viewer's offer, adds a video track and returns the
// answer once ICE candidates are gathered (so no trickle ICE is needed).
// It is what ServeHTTP does, for programs with their own signalling.
func (b *Broadcaster) Accept(offer pion.SessionDescription) (*pion.SessionDescription, error) {
	cfg := pion.Configuration{}
	if len(b.iceServers) > 0 {
		cfg.ICEServers = []pion.ICEServer{{URLs: b.iceServers}}
	}
	pc, err := pion.NewPeerConnection(cfg)
	if err != nil {
		return nil, err
	}
	track, err := pion.NewTrackLocalStaticSample(h264Codec, "video", "hwmediacodec")
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
	pc.OnConnectionStateChange(func(s pion.PeerConnectionState) {
		switch s {
		case pion.PeerConnectionStateFailed, pion.PeerConnectionStateClosed, pion.PeerConnectionStateDisconnected:
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
	gathered := pion.GatheringCompletePromise(pc)
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

// WritePacket implements hwmediacodec.PacketWriter: one Annex-B access unit goes to
// every viewer. pion's H.264 payloader splits the NAL units into RTP
// packets (STAP-A for the parameter sets, FU-A for large slices). Packet
// times are in 90 kHz units (capture.TimeScale). A viewer whose track
// fails is dropped.
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
			b.remove(p)
		}
	}
	return nil
}

// Close implements io.Closer: it disconnects every viewer and refuses
// new ones.
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
