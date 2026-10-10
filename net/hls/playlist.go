// Package hls serves encoder output as a live HLS stream. A Playlist keeps
// a sliding window of the fMP4 segments an mp4.Segmenter cuts and is an
// http.Handler for the media playlist, the init segment and the media
// segments:
//
//	playlist := hls.NewPlaylist(6, 2*time.Second)
//	seg, _ := mp4.NewSegmenter(codec, timeScale, 2*time.Second, playlist.SetInit, playlist.Add)
//	// feed seg with encoder packets (it is a hwmediacodec.PacketWriteCloser), then:
//	http.Handle("/live/", playlist) // players open /live/index.m3u8
//
// Everything stays in memory; nothing is written to disk. Safari plays
// the stream natively, other browsers through hls.js or another MSE
// player.
package hls

import (
	"fmt"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// Playlist keeps a sliding window of fMP4 segments in memory and serves
// them as a live HLS stream: index.m3u8, init.mp4 and seg_N.m4s. It is
// safe for concurrent use.
type Playlist struct {
	window int
	target time.Duration

	mu    sync.RWMutex
	init  []byte
	segs  []mp4.Segment
	ended bool
}

// NewPlaylist keeps the last window segments (at least three, the minimum
// a live playlist should list). target is the nominal segment length, the
// same as the Segmenter's; EXT-X-TARGETDURATION grows with the longest
// segment in the window.
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

// ServeHTTP implements http.Handler. It answers by the last element of
// the request path (index.m3u8, init.mp4, seg_N.m4s), so the playlist can
// be mounted under any prefix; the URIs inside the playlist are relative.
// The playlist answers 503 until the first segment exists. Responses
// carry Access-Control-Allow-Origin: * so that a player page on another
// origin can fetch them.
func (p *Playlist) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	name := path.Base(r.URL.Path)
	switch {
	case name == "index.m3u8":
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
	case name == "init.mp4":
		p.mu.RLock()
		data := p.init
		p.mu.RUnlock()
		if data == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Write(data)
	case strings.HasPrefix(name, "seg_") && strings.HasSuffix(name, ".m4s"):
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "seg_"), ".m4s"))
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
