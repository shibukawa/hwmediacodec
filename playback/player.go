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
// # Sound
//
// WithAudio adds the audio track that goes with the pictures. The package
// opens no audio device: Player.Audio is the sound as a stream of samples
// on the pictures' time line, for a device of the caller's to play, and
// Player.AttachAudioDevice makes that device the clock, so that Advance
// shows the picture for the sample at the loudspeaker:
//
//	p, err := playback.New(video.PacketSource(), playback.WithAudio(audio.PacketSource()))
//	if stream := p.Audio(); stream != nil { // nil: no decoder for the track
//		p.AttachAudioDevice(newDevice(stream)) // plays stream, reports its position
//	}
//
// See AudioStream and AudioDevice.
//
// The module github.com/shibukawa/hwmediacodec/ebitenvideo wraps a Player
// for Ebitengine, sound included.
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

	audio         AudioSource
	audioFormat   hwmediacodec.SampleFormat
	audioChannels int
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

// WithAudio gives the player the sound that goes with the pictures: an
// audio track of the same file, for example mp4.AudioTrack.PacketSource.
// Player.Audio then returns it as a stream of samples on the pictures'
// time line, which follows Seek and loops with them.
//
// The player goes on without sound when the platform has no decoder for
// the track or the decoder gives up: Audio returns nil in the first case
// and silence from then on in the second, and AudioErr says why.
func WithAudio(src AudioSource) Option { return func(c *config) { c.audio = src } }

// WithAudioFormat sets the sample format of the audio stream: F32 (the
// default) or S16.
func WithAudioFormat(f hwmediacodec.SampleFormat) Option {
	return func(c *config) { c.audioFormat = f }
}

// WithAudioChannels makes the audio stream mono (1) or stereo (2)
// whatever the track has: a mono track goes to both sides, and more
// channels are mixed down (centre and surrounds into both sides at -3 dB,
// the sum scaled so that it cannot clip). By default the stream has the
// channels of the track.
func WithAudioChannels(n int) Option { return func(c *config) { c.audioChannels = n } }

// audioGrace is how long a player with an audio device that is not ready
// holds the first picture before it goes ahead on the clock of Advance,
// and holdLimit how long the sound waits for the first picture after a
// start or a jump.
const (
	audioGrace = 500 * time.Millisecond
	holdLimit  = 500 * time.Millisecond
)

// Player plays hardware-decoded video against a clock the caller drives
// with Advance.
//
// Decoding runs on a background goroutine; the methods of Player are meant
// to be called from one goroutine (the display or game loop).
type Player struct {
	src *source
	tl  timeline

	audio    *AudioStream
	audioErr error
	loop     bool
	// length is the length of one pass through the stream with sound
	// (the longer of the two tracks), or 0 without sound.
	length time.Duration
	dev    AudioDevice
	// synced reports that the device plays from where the clock is, and
	// holding that it is paused there until the first picture is in.
	// notReady and held count the time spent waiting for the device and
	// for the picture.
	synced   bool
	holding  bool
	notReady time.Duration
	held     time.Duration
	// heard is the furthest position the device has reported since it was
	// last placed: a device's estimate may step back a little, the clock
	// does not.
	heard time.Duration

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
	if cfg.audioFormat == 0 {
		cfg.audioFormat = hwmediacodec.F32
	}
	if cfg.audioFormat.BytesPerSample() == 0 {
		return nil, fmt.Errorf("playback: unknown audio sample format %s", cfg.audioFormat)
	}
	if cfg.audioChannels < 0 {
		return nil, fmt.Errorf("playback: audio channel count must not be negative, got %d", cfg.audioChannels)
	}
	s, err := newSource(src, cfg.prefetch, cfg.loop, cfg.software, cfg.format)
	if err != nil {
		return nil, err
	}
	p := &Player{src: s, loop: cfg.loop}
	if cfg.audio != nil {
		// A film without its sound is still a film.
		if p.audio, p.audioErr = newAudioStream(cfg.audio, cfg.audioFormat, cfg.audioChannels, cfg.loop); p.audio != nil {
			if s.seeker != nil {
				p.length = s.seeker.Length()
			}
			if p.audio.seeker != nil {
				p.length = max(p.length, p.audio.seeker.Length())
			}
			p.audio.start(p.length)
			s.setLength(p.length)
		}
	}
	return p, nil
}

// Audio returns the sound as a stream of samples for an audio device to
// play, or nil when the player has none (no WithAudio, or no decoder for
// the track: see AudioErr). Attach the device that plays it with
// AttachAudioDevice.
func (p *Player) Audio() *AudioStream { return p.audio }

// AudioErr returns why the player is without sound, or lost it: the
// error of opening the audio decoder, or the one that stopped it.
func (p *Player) AudioErr() error {
	if p.audio != nil {
		return p.audio.Err()
	}
	return p.audioErr
}

// AttachAudioDevice hands the player the device that plays Audio. From
// then on the sound is the clock: Advance shows the picture for the
// sample at the loudspeaker (AudioDevice.Position) and uses its argument
// only while the device is not ready. Play, Pause and Seek steer the
// device along. After a start or a jump the sound waits a moment for the
// first picture.
func (p *Player) AttachAudioDevice(dev AudioDevice) {
	p.dev = dev
	p.synced, p.holding, p.notReady = false, false, 0
}

// Play starts or resumes playback.
func (p *Player) Play() {
	p.playing = true
	if p.dev != nil && p.synced && !p.holding && !p.ended && p.err == nil {
		p.dev.Play()
	}
}

// Pause stops the clock; the current picture stays.
func (p *Player) Pause() {
	p.playing = false
	if p.dev != nil {
		p.dev.Pause()
	}
}

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
	// With sound: the longer of the two tracks.
	return max(p.src.seeker.Length(), p.length)
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
	p.tl.loop = 0
	p.ended, p.endSeen = false, false
	if p.dev != nil && p.synced {
		// The sound jumps along and waits there for the picture.
		p.dev.Pause()
		if err := p.dev.SetPosition(t); err != nil {
			p.synced = false
		}
		p.holding, p.held, p.heard = true, 0, 0
	}
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
	show, skipped := p.step(dt)
	p.skipped += skipped
	if err := p.src.Err(); err != nil {
		show.doRelease()
		p.err = err
		if p.dev != nil {
			p.dev.Pause()
		}
		return nil, err
	}
	// With sound the stream is over when its longer track is.
	if p.endSeen && !p.tl.hasPending && (p.dev == nil || !p.synced || p.holding || p.clock() >= p.length) {
		p.ended = true
		if p.dev != nil {
			p.dev.Pause()
		}
	}
	if show.frame != nil {
		p.width, p.height = show.frame.Width, show.frame.Height
	}
	return show.frame, nil
}

// clock is the time on the time line that goes on through the loops: the
// passes played so far and the position in the current one.
func (p *Player) clock() time.Duration {
	return max(time.Duration(p.tl.loop)*p.length+p.tl.pos, 0)
}

// step moves the clock and returns the frame that became due. Without an
// audio device, or while it is not ready, the clock moves by dt. With one
// it is the device's position; a device that has just come up, and one
// that was just told to jump, first waits where the clock is until the
// pictures have arrived there.
func (p *Player) step(dt time.Duration) (show item, skipped int) {
	if p.dev == nil || p.audio == nil {
		return p.tl.advance(dt, p.next)
	}
	if !p.dev.Ready() {
		p.synced = false
		// A device that is coming up gets a moment before the pictures
		// go ahead without it.
		if p.notReady < audioGrace {
			p.notReady += dt
			dt = 0
		}
		return p.tl.advance(dt, p.next)
	}
	p.notReady = audioGrace
	if !p.synced {
		p.dev.Pause()
		if err := p.dev.SetPosition(p.clock()); err != nil {
			// A device that cannot be placed is left alone.
			return p.tl.advance(dt, p.next)
		}
		p.synced, p.holding, p.held = true, true, 0
		p.heard = 0
	}
	at := max(p.dev.Position(), p.heard)
	if p.holding {
		at = p.clock()
	}
	p.heard = at
	loop := 0
	if p.loop && p.length > 0 {
		loop = int(at / p.length)
		at -= time.Duration(loop) * p.length
	}
	show, skipped = p.tl.sync(loop, at, p.next)
	if p.holding {
		p.held += dt
		if p.tl.started || p.endSeen || p.held >= holdLimit {
			p.holding = false
			p.dev.Play()
		}
	}
	return show, skipped
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
	if p.dev != nil {
		p.dev.Pause()
	}
	if p.audio != nil {
		p.audio.close()
	}
	err := p.src.close()
	if p.err == nil && err != nil && !errors.Is(err, hwmediacodec.ErrClosed) {
		p.err = err
	}
	return err
}
