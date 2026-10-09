package ebitenvideo

import (
	"errors"
	"fmt"
	"image"
	"io"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/shibukawa/hwmediacodec"
)

// Option configures a Player.
type Option func(*config)

type config struct {
	loop     bool
	software bool
	prefetch int
}

// WithLoop restarts the stream from the beginning when it ends. The source
// must be an io.ReadSeeker.
func WithLoop() Option { return func(c *config) { c.loop = true } }

// WithSoftwareFallback allows the operating system's software decoder on
// machines without a hardware engine (see hwmediacodec.WithSoftwareFallback).
func WithSoftwareFallback() Option { return func(c *config) { c.software = true } }

// WithPrefetch sets how many decoded frames may wait ahead of playback
// (default 4). Each one holds a full RGBA picture in memory.
func WithPrefetch(frames int) Option { return func(c *config) { c.prefetch = frames } }

// Player plays an H.264 or HEVC elementary stream through the hardware
// decoder and keeps the current frame in an *ebiten.Image.
//
// Decoding runs on a background goroutine; the methods of Player are meant
// to be called from the game's goroutine (Update and Draw).
type Player struct {
	src     *source
	tl      timeline
	img     *ebiten.Image
	scratch []byte
	width   int
	height  int

	playing bool
	ended   bool
	err     error
	last    time.Time
	skipped int
}

// NewPlayer starts decoding the Annex-B elementary stream r (for example a
// .h264 or .hevc file) of codec c, to be shown at fps frames per second.
// Playback starts paused; call Play.
func NewPlayer(r io.Reader, c hwmediacodec.Codec, fps float64, opts ...Option) (*Player, error) {
	if fps <= 0 {
		return nil, fmt.Errorf("ebitenvideo: frame rate must be positive, got %g", fps)
	}
	cfg := config{prefetch: 4}
	for _, o := range opts {
		o(&cfg)
	}
	src, err := newSource(r, c, cfg.prefetch, cfg.loop, cfg.software)
	if err != nil {
		return nil, err
	}
	return &Player{src: src, tl: timeline{fps: fps}}, nil
}

// Play starts or resumes playback.
func (p *Player) Play() { p.playing = true }

// Pause stops advancing; the current frame stays in Image.
func (p *Player) Pause() { p.playing = false }

// IsPlaying reports whether playback advances on Update.
func (p *Player) IsPlaying() bool { return p.playing && !p.ended && p.err == nil }

// Ended reports whether the last frame has been shown. A looping player
// never ends.
func (p *Player) Ended() bool { return p.ended }

// Err returns the decoding error that stopped playback, or nil.
func (p *Player) Err() error { return p.err }

// Position returns the playback position within the current loop
// iteration.
func (p *Player) Position() time.Duration { return p.tl.position() }

// Skipped returns how many frames were dropped so far because decoding or
// the game loop fell behind.
func (p *Player) Skipped() int { return p.skipped }

// Image returns the image holding the current frame, or nil before the
// first frame is available. The same image is updated in place; do not
// deallocate it.
func (p *Player) Image() *ebiten.Image { return p.img }

// Size returns the picture size, or (0, 0) before the first frame.
func (p *Player) Size() (width, height int) { return p.width, p.height }

// Update advances playback by one tick. Call it once from the game's
// Update. It returns the error that stopped playback, if any.
func (p *Player) Update() error {
	if p.err != nil {
		return p.err
	}
	if p.ended || !p.playing {
		p.last = time.Time{}
		return nil
	}
	show, skipped := p.tl.advance(p.tick(), p.src.next)
	p.skipped += skipped
	if show.frame != nil {
		p.upload(show.frame)
		show.release()
	}
	if err := p.src.Err(); err != nil {
		p.err = err
		return err
	}
	if p.src.ended() && !p.tl.hasPending {
		p.ended = true
	}
	return nil
}

// tick returns the time one Update represents.
func (p *Player) tick() time.Duration {
	if tps := ebiten.TPS(); tps > 0 {
		return time.Second / time.Duration(tps)
	}
	now := time.Now()
	if p.last.IsZero() {
		p.last = now
		return 0
	}
	dt := now.Sub(p.last)
	p.last = now
	return dt
}

// upload copies a decoded RGBA frame into the image.
func (p *Player) upload(f *hwmediacodec.Frame) {
	w, h := f.Width, f.Height
	if p.img == nil || w != p.width || h != p.height {
		if p.img != nil {
			p.img.Deallocate()
		}
		p.img = ebiten.NewImageWithOptions(image.Rect(0, 0, w, h), &ebiten.NewImageOptions{Unmanaged: true})
		p.width, p.height = w, h
	}
	pix := f.Planes[0]
	if stride := f.Strides[0]; stride != w*4 {
		if cap(p.scratch) < w*h*4 {
			p.scratch = make([]byte, w*h*4)
		}
		p.scratch = p.scratch[:w*h*4]
		for r := 0; r < h; r++ {
			copy(p.scratch[r*w*4:(r+1)*w*4], pix[r*stride:r*stride+w*4])
		}
		pix = p.scratch
	}
	p.img.WritePixels(pix)
}

// Close stops decoding and releases the decoder. The image stays usable.
func (p *Player) Close() error {
	p.playing = false
	p.tl.release()
	err := p.src.close()
	if p.err == nil && err != nil && !errors.Is(err, hwmediacodec.ErrClosed) {
		p.err = err
	}
	return err
}
