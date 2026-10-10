// Package playback plays a video stream against a clock: it decodes on a
// background goroutine through hwmediacodec, a few frames ahead, and hands
// out the frame that is due as the caller advances the time. It knows
// nothing about where the pictures go; upload each returned frame to a
// texture, a window or an image.
//
//	p, err := playback.New(track.PacketSource()) // *mp4.VideoTrack
//	...
//	p.Play()
//	for each tick of the display or game loop {
//		f, err := p.Advance(dt) // dt: the time since the previous call
//		if f != nil {
//			show(f)             // RGBA pixels in f.Planes[0]
//			f.Release()
//		}
//	}
//
// New takes any hwmediacodec.PacketReader, which carries the presentation
// times; a hwmediacodec.PacketSeeker also lets the player loop, report its
// Length and Seek. NewStream reads a raw Annex-B elementary stream, which
// carries no timestamps, so the frame rate is a parameter; an io.ReadSeeker
// gets seeking too, by scanning the stream once for keyframes.
//
// When decoding or the caller falls behind, frames are skipped so that the
// picture stays in time. Seek restarts decoding at the keyframe before the
// target and drops the frames up to it, so the next picture shown is the
// one at the target.
//
// The module github.com/shibukawa/hwmediacodec/ebitenvideo wraps a Player
// for Ebitengine.
package playback

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/shibukawa/hwmediacodec"
)

// Option configures a Player.
type Option func(*config)

type config struct {
	loop     bool
	software bool
	prefetch int
	format   hwmediacodec.PixelFormat
}

// WithLoop restarts the stream from the beginning when it ends. The source
// must be able to seek: an io.ReadSeeker for NewStream, a
// hwmediacodec.PacketSeeker for New.
func WithLoop() Option { return func(c *config) { c.loop = true } }

// WithSoftwareFallback allows the operating system's software decoder on
// machines without a hardware engine (see hwmediacodec.WithSoftwareFallback).
func WithSoftwareFallback() Option { return func(c *config) { c.software = true } }

// WithPrefetch sets how many decoded frames may wait ahead of playback
// (default 4). Each one holds a full picture in memory.
func WithPrefetch(frames int) Option { return func(c *config) { c.prefetch = frames } }

// WithOutputFormat sets the pixel format of the frames (default RGBA).
func WithOutputFormat(f hwmediacodec.PixelFormat) Option {
	return func(c *config) { c.format = f }
}

// Player plays hardware-decoded video against a clock the caller drives
// with Advance.
//
// Decoding runs on a background goroutine; the methods of Player are meant
// to be called from one goroutine (the display or game loop).
type Player struct {
	src *source
	tl  timeline

	width   int
	height  int
	playing bool
	ended   bool
	endSeen bool
	gen     int
	err     error
	skipped int
}

// NewStream starts decoding the Annex-B elementary stream r (for example a
// .h264 or .hevc file) of codec c, to be shown at fps frames per second.
// Playback starts paused; call Play. When r is an io.ReadSeeker the player
// can loop and Seek (the stream is scanned once for keyframes on the first
// seek).
func NewStream(r io.Reader, c hwmediacodec.Codec, fps float64, opts ...Option) (*Player, error) {
	if fps <= 0 {
		return nil, fmt.Errorf("playback: frame rate must be positive, got %g", fps)
	}
	ss, err := newStreamSource(r, c, fps)
	if err != nil {
		return nil, err
	}
	var src hwmediacodec.PacketReader = ss
	if ss.rs != nil {
		src = &seekableStream{ss}
	}
	return New(src, opts...)
}

// New starts decoding packets from src, which carries its own presentation
// times (an MP4 demuxer, for example). Playback starts paused; call Play.
// Looping and Seek need src to be a hwmediacodec.PacketSeeker.
func New(src hwmediacodec.PacketReader, opts ...Option) (*Player, error) {
	cfg := config{prefetch: 4, format: hwmediacodec.RGBA}
	for _, o := range opts {
		o(&cfg)
	}
	s, err := newSource(src, cfg.prefetch, cfg.loop, cfg.software, cfg.format)
	if err != nil {
		return nil, err
	}
	return &Player{src: s}, nil
}

// Play starts or resumes playback.
func (p *Player) Play() { p.playing = true }

// Pause stops the clock; the current picture stays.
func (p *Player) Pause() { p.playing = false }

// IsPlaying reports whether the clock is running: Play was called and the
// stream has neither ended nor failed.
func (p *Player) IsPlaying() bool { return p.playing && !p.ended && p.err == nil }

// Ended reports whether the last frame has been shown (never when looping).
func (p *Player) Ended() bool { return p.ended }

// Err returns the first decoding error.
func (p *Player) Err() error { return p.err }

// Position is the current playback time within the stream.
func (p *Player) Position() time.Duration { return p.tl.position() }

// Length is the stream duration, or 0 when it is not known (a source that
// cannot seek, or a raw stream that has not been scanned yet).
func (p *Player) Length() time.Duration {
	if p.src.seeker == nil {
		return 0
	}
	return p.src.seeker.Length()
}

// Seekable reports whether Seek works.
func (p *Player) Seekable() bool { return p.src.seeker != nil }

// Seek jumps to t: decoding restarts at the keyframe before t and the next
// frame Advance returns is the one at t. The clock waits for that frame.
// Seeking also resumes a player that has ended.
func (p *Player) Seek(t time.Duration) error {
	if p.err != nil {
		return p.err
	}
	if p.src.seeker == nil {
		return ErrNotSeekable
	}
	if t < 0 {
		t = 0
	}
	p.gen++
	p.src.requestSeek(seekRequest{target: t, gen: p.gen})
	p.tl.reset(t)
	p.ended, p.endSeen = false, false
	return nil
}

// Skipped is the number of frames dropped so far because decoding or the
// caller was behind.
func (p *Player) Skipped() int { return p.skipped }

// Size is the size of the last frame Advance returned (0, 0 before the
// first).
func (p *Player) Size() (width, height int) { return p.width, p.height }

// Advance moves the clock forward by dt and returns the frame that became
// due, or nil when the picture does not change (paused, ended, or the next
// frame is not due yet). The caller owns the frame and must Release it.
// A nil frame with a nil error is the normal case between frames.
func (p *Player) Advance(dt time.Duration) (*hwmediacodec.Frame, error) {
	if p.err != nil {
		return nil, p.err
	}
	if p.ended || !p.playing {
		return nil, nil
	}
	show, skipped := p.tl.advance(dt, p.next)
	p.skipped += skipped
	if err := p.src.Err(); err != nil {
		show.doRelease()
		p.err = err
		return nil, err
	}
	if p.endSeen && !p.tl.hasPending {
		p.ended = true
	}
	if show.frame != nil {
		p.width, p.height = show.frame.Width, show.frame.Height
	}
	return show.frame, nil
}

func (p *Player) next() (item, bool) {
	for {
		it, ok := p.src.next()
		if !ok {
			return item{}, false
		}
		if it.gen != p.gen {
			it.doRelease() // decoded before the last seek was picked up
			continue
		}
		if it.end {
			p.endSeen = true
			continue
		}
		return it, true
	}
}

// Close stops decoding and releases the decoder. Frames already returned
// by Advance stay valid until their own Release.
func (p *Player) Close() error {
	p.playing = false
	p.tl.release()
	err := p.src.close()
	if p.err == nil && err != nil && !errors.Is(err, hwmediacodec.ErrClosed) {
		p.err = err
	}
	return err
}
