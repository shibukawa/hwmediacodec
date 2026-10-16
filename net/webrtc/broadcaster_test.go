package webrtc_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp/codecs"
	pion "github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/mediatest"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
	"github.com/shibukawa/hwmediacodec/net/webrtc"
)

// viewer is a pion peer standing in for the browser: it posts an offer to
// the server, receives the track and rebuilds access units from RTP.
type viewer struct {
	pc      *pion.PeerConnection
	samples chan []byte
	ssrc    atomic.Uint32
}

func connect(t *testing.T, url string) *viewer {
	t.Helper()
	pc, err := pion.NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	v := &viewer{pc: pc, samples: make(chan []byte, 1024)}
	if _, err := pc.AddTransceiverFromKind(pion.RTPCodecTypeVideo, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	pc.OnTrack(func(track *pion.TrackRemote, _ *pion.RTPReceiver) {
		v.ssrc.Store(uint32(track.SSRC()))
		sb := samplebuilder.New(50, &codecs.H264Packet{}, 90000)
		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			sb.Push(pkt)
			for s := sb.Pop(); s != nil; s = sb.Pop() {
				v.samples <- s.Data
			}
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := pion.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	body, _ := json.Marshal(pc.LocalDescription())
	resp, err := http.Post(url+"/offer", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("offer answered %d", resp.StatusCode)
	}
	var answer pion.SessionDescription
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		t.Fatal(err)
	}
	return v
}

// nalUnits returns the NAL units of an Annex-B access unit without AUD
// and filler, which pion's payloader drops.
func nalUnits(au []byte) [][]byte {
	var out [][]byte
	for _, nal := range annexb.Split(au) {
		if t := annexb.NALUnitType(hwmediacodec.H264, nal); t == annexb.H264NALAUD || t == annexb.H264NALFiller {
			continue
		}
		out = append(out, nal)
	}
	return out
}

func TestBroadcasterDeliversAccessUnits(t *testing.T) {
	mediatest.RequireFFmpeg(t)
	dir := t.TempDir()
	src := mediatest.GenerateMP4(t, dir, mediatest.MP4Options{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 60, BFrames: 0, GOP: 15})
	d, err := mp4.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	track := d.Video()

	var keyRequests atomic.Int32
	bc := webrtc.NewBroadcaster(30, nil, func() { keyRequests.Add(1) })
	srv := httptest.NewServer(bc)
	defer srv.Close()
	defer bc.Close()

	// The handler is the signalling endpoint only: anything but a POST is
	// refused.
	if r, err := http.Get(srv.URL + "/"); err != nil || r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET answered %v %v, want 405", r, err)
	} else {
		r.Body.Close()
	}
	v := connect(t, srv.URL)
	defer v.pc.Close()
	deadline := time.Now().Add(10 * time.Second)
	for bc.Viewers() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if bc.Viewers() != 1 {
		t.Fatal("viewer did not register")
	}
	if keyRequests.Load() == 0 {
		t.Error("a joining viewer did not trigger a keyframe request")
	}
	// Wait for the connection, otherwise the first samples go nowhere.
	for v.pc.ConnectionState() != pion.PeerConnectionStateConnected && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if v.pc.ConnectionState() != pion.PeerConnectionStateConnected {
		t.Fatalf("connection state %s", v.pc.ConnectionState())
	}

	// Stream the file in real time-ish and collect what arrives.
	var sent [][]byte
	for i := range track.SampleCount() {
		p, err := track.Packet(i)
		if err != nil {
			t.Fatal(err)
		}
		if err := bc.WritePacket(p); err != nil {
			t.Fatal(err)
		}
		sent = append(sent, p.Data)
		time.Sleep(2 * time.Millisecond)
	}
	// The sample builder releases a sample when the next one starts, so
	// the very last access unit may stay inside it.
	var got [][]byte
	timeout := time.After(5 * time.Second)
	for len(got) < len(sent)-1 {
		select {
		case s := <-v.samples:
			got = append(got, s)
		case <-timeout:
			t.Fatalf("received %d of %d access units", len(got), len(sent))
		}
	}
	for i := range got {
		want := nalUnits(sent[i])
		have := nalUnits(got[i])
		if len(have) != len(want) {
			t.Fatalf("access unit %d: %d NAL units, want %d", i, len(have), len(want))
		}
		for k := range want {
			if !bytes.Equal(have[k], want[k]) {
				t.Fatalf("access unit %d NAL %d differs", i, k)
			}
		}
	}
	// What arrived decodes to the same pictures as the file.
	raw := filepath.Join(dir, "received.h264")
	var all []byte
	for _, au := range got {
		all = append(all, au...)
	}
	if err := os.WriteFile(raw, all, 0o644); err != nil {
		t.Fatal(err)
	}
	want := mediatest.FrameMD5(t, src, "")
	have := mediatest.FrameMD5(t, raw, "h264")
	if !slices.Equal(have, want[:len(have)]) || len(have) < len(want)-1 {
		t.Errorf("received stream decodes differently (%d vs %d frames)", len(have), len(want))
	}

	// A picture loss indication from the viewer asks the encoder for a
	// keyframe. Requests within half a second of the one the join made
	// are merged into it, so the indication goes out after that.
	time.Sleep(600 * time.Millisecond)
	before := keyRequests.Load()
	if err := v.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: v.ssrc.Load()}}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for keyRequests.Load() == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if keyRequests.Load() == before {
		t.Error("PLI did not trigger a keyframe request")
	}
}

func TestBroadcasterStartsViewersAtKeyframes(t *testing.T) {
	mediatest.RequireFFmpeg(t)
	src := mediatest.GenerateMP4(t, t.TempDir(), mediatest.MP4Options{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 40, BFrames: 0, GOP: 10})
	d, err := mp4.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	track := d.Video()

	bc := webrtc.NewBroadcaster(30, nil, func() {})
	srv := httptest.NewServer(bc)
	defer srv.Close()
	defer bc.Close()
	v := connect(t, srv.URL)
	defer v.pc.Close()
	deadline := time.Now().Add(10 * time.Second)
	for v.pc.ConnectionState() != pion.PeerConnectionStateConnected && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// Start in the middle of a GOP: samples 5..39. The viewer must first
	// see sample 10, the next keyframe.
	for i := 5; i < track.SampleCount(); i++ {
		p, err := track.Packet(i)
		if err != nil {
			t.Fatal(err)
		}
		if err := bc.WritePacket(p); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	select {
	case first := <-v.samples:
		want, _ := track.Packet(10)
		if !bytes.Equal(nalUnits(first)[len(nalUnits(first))-1], nalUnits(want.Data)[len(nalUnits(want.Data))-1]) {
			t.Error("the first delivered access unit is not the keyframe at sample 10")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing received")
	}
}

// hevcAccessUnit is a made-up keyframe access unit: VPS, SPS, PPS and an
// IDR slice header with filler. The payloader looks at the NAL unit
// headers only.
func hevcAccessUnit(size int) []byte {
	var au []byte
	for _, typ := range []byte{32, 33, 34} {
		au = append(au, 0, 0, 0, 1, typ<<1, 1, 0xAA, 0xBB)
	}
	au = append(au, 0, 0, 0, 1, 19<<1, 1)
	for i := range size {
		au = append(au, byte(i%200)+1)
	}
	return au
}

func TestCodecBroadcasterHEVC(t *testing.T) {
	if _, err := webrtc.NewCodecBroadcaster(hwmediacodec.AV1, 30, nil, nil); !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Fatalf("AV1: %v", err)
	}
	bc, err := webrtc.NewCodecBroadcaster(hwmediacodec.HEVC, 30, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { bc.ServeHTTP(w, r) }))
	defer srv.Close()

	pc, err := pion.NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.AddTransceiverFromKind(pion.RTPCodecTypeVideo, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	mime := make(chan string, 1)
	packets := make(chan int, 1024)
	pc.OnTrack(func(track *pion.TrackRemote, _ *pion.RTPReceiver) {
		mime <- track.Codec().MimeType
		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			packets <- len(pkt.Payload)
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := pion.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	body, _ := json.Marshal(pc.LocalDescription())
	resp, err := http.Post(srv.URL+"/offer", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var answer pion.SessionDescription
	err = json.NewDecoder(resp.Body).Decode(&answer)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer.SDP, "H265/90000") {
		t.Fatalf("answer offers no H265:\n%s", answer.SDP)
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for pts := int64(0); ; pts += 3000 {
		if err := bc.WritePacket(hwmediacodec.Packet{Data: hevcAccessUnit(3000), PTS: pts, Keyframe: true}); err != nil {
			t.Fatal(err)
		}
		select {
		case m := <-mime:
			if !strings.EqualFold(m, pion.MimeTypeH265) {
				t.Fatalf("track codec %s", m)
			}
			select {
			case <-packets:
				return
			case <-deadline:
				t.Fatal("no RTP packet arrived")
			}
		case <-deadline:
			t.Fatal("no track arrived")
		case <-time.After(30 * time.Millisecond):
		}
	}
}

func TestCodecBroadcasterRefusesOfferWithoutCodec(t *testing.T) {
	bc, err := webrtc.NewCodecBroadcaster(hwmediacodec.HEVC, 30, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close()
	m := &pion.MediaEngine{}
	if err := m.RegisterCodec(pion.RTPCodecParameters{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, PayloadType: 102}, pion.RTPCodecTypeVideo); err != nil {
		t.Fatal(err)
	}
	pc, err := pion.NewAPI(pion.WithMediaEngine(m)).NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.AddTransceiverFromKind(pion.RTPCodecTypeVideo, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := pion.GatheringCompletePromise(pc)
	pc.SetLocalDescription(offer)
	<-gathered
	answer, err := bc.Accept(*pc.LocalDescription())
	if !errors.Is(err, webrtc.ErrBadOffer) {
		t.Fatalf("an H.264-only offer got %v, answer:\n%v", err, answer)
	}
}
