package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/examples/container"
	"github.com/shibukawa/hwmediacodec/examples/internal/testutil"
)

// viewer is a pion peer standing in for the browser: it posts an offer to
// the server, receives the track and rebuilds access units from RTP.
type viewer struct {
	pc      *webrtc.PeerConnection
	samples chan []byte
	ssrc    atomic.Uint32
}

func connect(t *testing.T, url string) *viewer {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	v := &viewer{pc: pc, samples: make(chan []byte, 1024)}
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
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
	gathered := webrtc.GatheringCompletePromise(pc)
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
	var answer webrtc.SessionDescription
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
	testutil.RequireFFmpeg(t)
	dir := t.TempDir()
	src := testutil.GenerateMP4(t, dir, testutil.MP4Options{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 60, BFrames: 0, GOP: 15})
	d, err := container.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	track := d.Video()

	var keyRequests atomic.Int32
	bc := NewBroadcaster(30, nil, func() { keyRequests.Add(1) })
	srv := httptest.NewServer(bc)
	defer srv.Close()
	defer bc.Close()

	if r, err := http.Get(srv.URL + "/"); err != nil || r.StatusCode != 200 {
		t.Fatalf("player page: %v %v", err, r)
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
	for v.pc.ConnectionState() != webrtc.PeerConnectionStateConnected && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if v.pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
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
	want := testutil.FrameMD5(t, src, "")
	have := testutil.FrameMD5(t, raw, "h264")
	if !slices.Equal(have, want[:len(have)]) || len(have) < len(want)-1 {
		t.Errorf("received stream decodes differently (%d vs %d frames)", len(have), len(want))
	}

	// A picture loss indication from the viewer asks the encoder for a
	// keyframe.
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
	testutil.RequireFFmpeg(t)
	src := testutil.GenerateMP4(t, t.TempDir(), testutil.MP4Options{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 40, BFrames: 0, GOP: 10})
	d, err := container.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	track := d.Video()

	bc := NewBroadcaster(30, nil, func() {})
	srv := httptest.NewServer(bc)
	defer srv.Close()
	defer bc.Close()
	v := connect(t, srv.URL)
	defer v.pc.Close()
	deadline := time.Now().Add(10 * time.Second)
	for v.pc.ConnectionState() != webrtc.PeerConnectionStateConnected && time.Now().Before(deadline) {
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
