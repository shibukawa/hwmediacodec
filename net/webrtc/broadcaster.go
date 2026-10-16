// Package webrtc sends encoder output to browsers over WebRTC with pion.
// A Broadcaster fans one H.264 (or HEVC) stream out to every connected
// viewer; it is
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
// The signalling endpoint is unauthenticated: anyone who can reach it can
// watch the stream. Serve it on a loopback or trusted interface, or put
// it behind the application's own authentication. A Broadcaster accepts
// at most MaxViewers connections, and a request that is cancelled before
// the answer is ready releases its connection.
//
// The package is a module of its own
// (github.com/shibukawa/hwmediacodec/net/webrtc) so that the WebRTC stack
// stays out of the core module's dependencies.
package webrtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/pion/rtcp"
	pion "github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// h264Codec is what the track offers. Constrained Baseline with
// packetization-mode 1 is accepted by every browser; the actual profile of
// the bitstream is what the encoder was asked for.
var h264Codec = pion.RTPCodecCapability{
	MimeType:    pion.MimeTypeH264,
	ClockRate:   90000,
	SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
}

// hevcCodec is what an HEVC track offers (RFC 7798). Browsers decode it
// only where their platform has an HEVC decoder: Safari, and Chrome and
// Edge with hardware decoding; an offer without it is refused.
var hevcCodec = pion.RTPCodecCapability{
	MimeType:  pion.MimeTypeH265,
	ClockRate: 90000,
}

// DefaultMaxViewers is the number of viewers a Broadcaster accepts unless
// MaxViewers says otherwise.
const DefaultMaxViewers = 32

// gatherTimeout bounds how long Accept waits for ICE gathering.
const gatherTimeout = 5 * time.Second

// keyframeMinInterval is the shortest time between two keyframe requests
// the Broadcaster passes on to the encoder. Viewers that join or report a
// picture loss within it share the keyframe the first request produces,
// and a viewer that floods the stream with picture loss indications
// cannot turn it into keyframes only.
const keyframeMinInterval = 500 * time.Millisecond

// peerQueueSize is the number of access units a viewer may fall behind by
// before it is dropped: about one second at 60 fps.
const peerQueueSize = 64

var (
	// ErrTooManyViewers is returned by Accept, and answered with 503 by
	// ServeHTTP, when MaxViewers connections exist or are being set up.
	ErrTooManyViewers = errors.New("webrtc: too many viewers")
	// ErrBadOffer is returned by Accept, and answered with 400 by
	// ServeHTTP, when the session description is not an offer the
	// Broadcaster can answer. The wrapped error says why.
	ErrBadOffer = errors.New("webrtc: invalid SDP offer")
)

type peer struct {
	pc    *pion.PeerConnection
	track *pion.TrackLocalStaticSample
	// queue carries the access units the peer's own goroutine sends, so
	// that a slow network stalls neither the encoder nor the other
	// viewers; done is closed when the peer is dropped.
	queue  chan queued
	done   chan struct{}
	closed bool // guarded by Broadcaster.mu, including during Accept
}

type queued struct {
	data     []byte
	duration time.Duration
	keyframe bool
}

// Broadcaster fans one encoded H.264 or HEVC stream out to every connected
// browser. It is a hwmediacodec.PacketWriteCloser for the packets and an
// http.Handler for
// the signalling (a POST with the browser's SDP offer as JSON, answered
// with ours). It is safe for concurrent use.
type Broadcaster struct {
	codec           pion.RTPCodecCapability
	fps             float64
	iceServers      []string
	requestKeyframe func()

	// MaxViewers is the number of connections the Broadcaster accepts,
	// counting those still being set up; DefaultMaxViewers unless changed
	// before the first viewer arrives, and no limit when zero or
	// negative.
	MaxViewers int

	mu             sync.Mutex
	peers          map[*peer]struct{}
	pending        int // connections inside Accept
	lastPTS        int64
	closed         bool
	lastKeyRequest time.Time
	writeMu        sync.Mutex // preserves packet ordering across concurrent writers
}

// NewBroadcaster creates a broadcaster for a stream of fps frames per
// second. requestKeyframe is called when a new viewer joins or a viewer
// reports a picture loss; wire it to Recorder.RequestKeyframe (a viewer
// only starts at a keyframe). Requests closer together than half a
// second are merged into one. iceServers lists STUN/TURN URLs, empty for
// LAN use. Invalid or unrepresentable frame rates use 60 fps.
func NewBroadcaster(fps float64, iceServers []string, requestKeyframe func()) *Broadcaster {
	return newBroadcaster(h264Codec, fps, iceServers, requestKeyframe)
}

// NewCodecBroadcaster is NewBroadcaster for a stream in codec c, H.264 or
// HEVC; other codecs are ErrUnsupported. A viewer whose offer has no
// matching codec is refused with ErrBadOffer.
func NewCodecBroadcaster(c hwmediacodec.Codec, fps float64, iceServers []string, requestKeyframe func()) (*Broadcaster, error) {
	switch c {
	case hwmediacodec.H264:
		return newBroadcaster(h264Codec, fps, iceServers, requestKeyframe), nil
	case hwmediacodec.HEVC:
		return newBroadcaster(hevcCodec, fps, iceServers, requestKeyframe), nil
	}
	return nil, fmt.Errorf("webrtc: %s streams are not supported: %w", c, hwmediacodec.ErrUnsupported)
}

func newBroadcaster(rtpCodec pion.RTPCodecCapability, fps float64, iceServers []string, requestKeyframe func()) *Broadcaster {
	period := float64(time.Second) / fps
	if fps <= 0 || math.IsNaN(fps) || math.IsInf(fps, 0) || period < 1 || period >= float64(math.MaxInt64) {
		fps = 60
	}
	if requestKeyframe == nil {
		requestKeyframe = func() {}
	}
	return &Broadcaster{
		codec:           rtpCodec,
		fps:             fps,
		iceServers:      append([]string(nil), iceServers...),
		requestKeyframe: requestKeyframe,
		MaxViewers:      DefaultMaxViewers,
		peers:           map[*peer]struct{}{},
		lastPTS:         -1,
	}
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
// mount the handler where the page posts to. An offer that cannot be
// answered gets 400, a full or closed Broadcaster 503.
func (b *Broadcaster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST an SDP offer", http.StatusMethodNotAllowed)
		return
	}
	var offer pion.SessionDescription
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&offer); err != nil {
		http.Error(w, "the body is not a JSON session description", http.StatusBadRequest)
		return
	}
	answer, err := b.AcceptContext(r.Context(), offer)
	switch {
	case err == nil:
	case errors.Is(err, ErrBadOffer):
		http.Error(w, "invalid SDP offer", http.StatusBadRequest)
		return
	case errors.Is(err, ErrTooManyViewers), errors.Is(err, hwmediacodec.ErrClosed):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	case r.Context().Err() != nil:
		return // the client went away
	default:
		http.Error(w, "could not set up the connection", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(answer)
}

// Accept takes a viewer's offer, adds a video track and returns the
// answer once ICE candidates are gathered (so no trickle ICE is needed).
// It is what ServeHTTP does, for programs with their own signalling. The
// error is ErrBadOffer for an offer that cannot be answered and
// ErrTooManyViewers when MaxViewers is reached.
func (b *Broadcaster) Accept(offer pion.SessionDescription) (*pion.SessionDescription, error) {
	return b.AcceptContext(context.Background(), offer)
}

// AcceptContext is Accept that gives up, releasing the connection, when
// ctx is cancelled before the answer is ready.
func (b *Broadcaster) AcceptContext(ctx context.Context, offer pion.SessionDescription) (*pion.SessionDescription, error) {
	b.mu.Lock()
	switch {
	case b.closed:
		b.mu.Unlock()
		return nil, fmt.Errorf("webrtc: broadcaster is closed: %w", hwmediacodec.ErrClosed)
	case b.MaxViewers > 0 && len(b.peers)+b.pending >= b.MaxViewers:
		b.mu.Unlock()
		return nil, ErrTooManyViewers
	}
	b.pending++
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.pending--
		b.mu.Unlock()
	}()

	cfg := pion.Configuration{}
	if len(b.iceServers) > 0 {
		cfg.ICEServers = []pion.ICEServer{{URLs: b.iceServers}}
	}
	pc, err := pion.NewPeerConnection(cfg)
	if err != nil {
		return nil, err
	}
	track, err := pion.NewTrackLocalStaticSample(b.codec, "video", "hwmediacodec")
	if err != nil {
		pc.Close()
		return nil, err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		pc.Close()
		return nil, err
	}
	p := &peer{pc: pc, track: track, queue: make(chan queued, peerQueueSize), done: make(chan struct{})}

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
					b.askKeyframe()
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
		return nil, fmt.Errorf("%w: %v", ErrBadOffer, err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("%w: %v", ErrBadOffer, err)
	}
	gathered := pion.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		if errors.Is(err, pion.ErrUnsupportedCodec) {
			// The offer has no codec in common with the track.
			return nil, fmt.Errorf("%w: the viewer cannot receive %s", ErrBadOffer, b.codec.MimeType)
		}
		return nil, err
	}
	timeout := time.NewTimer(gatherTimeout)
	defer timeout.Stop()
	select {
	case <-gathered:
	case <-ctx.Done():
		pc.Close()
		return nil, ctx.Err()
	case <-timeout.C:
		pc.Close()
		return nil, errors.New("webrtc: ICE gathering timed out")
	}

	b.mu.Lock()
	if b.closed || p.closed {
		b.mu.Unlock()
		pc.Close()
		return nil, fmt.Errorf("webrtc: broadcaster is closed: %w", hwmediacodec.ErrClosed)
	}
	b.peers[p] = struct{}{}
	b.mu.Unlock()
	go b.serve(p)
	b.askKeyframe() // the new viewer can only start at a keyframe
	return pc.LocalDescription(), nil
}

// askKeyframe passes a keyframe request on to the encoder unless one went
// out within keyframeMinInterval, whose keyframe serves this request too.
func (b *Broadcaster) askKeyframe() {
	b.mu.Lock()
	now := time.Now()
	if !b.lastKeyRequest.IsZero() && now.Sub(b.lastKeyRequest) < keyframeMinInterval {
		b.mu.Unlock()
		return
	}
	b.lastKeyRequest = now
	b.mu.Unlock()
	b.requestKeyframe()
}

// serve sends the peer's queue on its track, starting at the first
// keyframe, until the peer is dropped or its track fails.
func (b *Broadcaster) serve(p *peer) {
	started := false
	for {
		select {
		case <-p.done:
			return
		case q := <-p.queue:
			if !started {
				if !q.keyframe {
					continue
				}
				started = true
			}
			if err := p.track.WriteSample(media.Sample{Data: q.data, Duration: q.duration}); err != nil {
				b.remove(p)
				return
			}
		}
	}
}

func (b *Broadcaster) remove(p *peer) {
	b.mu.Lock()
	wasClosed := p.closed
	p.closed = true
	delete(b.peers, p)
	b.mu.Unlock()
	if !wasClosed {
		close(p.done)
		if p.pc != nil {
			p.pc.Close()
		}
	}
}

// WritePacket implements hwmediacodec.PacketWriter: one Annex-B access unit goes to
// every viewer. pion's payloader splits the NAL units into RTP packets
// (for H.264 STAP-A for the parameter sets and FU-A for large slices, the
// aggregation and fragmentation units of RFC 7798 for HEVC). Packet
// times are in 90 kHz units (capture.TimeScale). The data is copied
// before WritePacket returns and sent by one goroutine per viewer; a
// viewer whose track fails, or that falls about a second behind, is
// dropped.
func (b *Broadcaster) WritePacket(pkt hwmediacodec.Packet) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	dur := time.Duration(float64(time.Second) / b.fps)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return hwmediacodec.ErrClosed
	}
	if b.lastPTS >= 0 && pkt.PTS > b.lastPTS {
		delta := pkt.PTS - b.lastPTS
		dur = time.Duration(codec.RescaleTrunc(delta, 90000, int64(time.Second)))
	}
	b.lastPTS = pkt.PTS
	peers := make([]*peer, 0, len(b.peers))
	for p := range b.peers {
		peers = append(peers, p)
	}
	b.mu.Unlock()
	if len(peers) == 0 {
		return nil
	}
	q := queued{data: append([]byte(nil), pkt.Data...), duration: dur, keyframe: pkt.Keyframe}
	for _, p := range peers {
		select {
		case p.queue <- q:
		default:
			b.remove(p) // too far behind to catch up
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
		b.remove(p)
	}
	return nil
}

func (b *Broadcaster) String() string {
	return fmt.Sprintf("%d viewer(s)", b.Viewers())
}
