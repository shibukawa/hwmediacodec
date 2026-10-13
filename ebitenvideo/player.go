package ebitenvideo

import (
	"image"
	"io"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/playback"
)

// Option configures a Player. The options are those of package playback,
// which does the decoding and the timing.
type Option = playback.Option

// WithLoop restarts the stream from the beginning when it ends. The source
// must be able to seek: an io.ReadSeeker for NewPlayer, a
// hwmediacodec.PacketSeeker for NewPlayerFromSource.
func WithLoop() Option { return playback.WithLoop() }

// WithSoftwareFallback allows the operating system's software decoder on
// machines without a hardware engine (see hwmediacodec.WithSoftwareFallback).
func WithSoftwareFallback() Option { return playback.WithSoftwareFallback() }

// WithPrefetch sets how many decoded frames may wait ahead of playback
// (default 4). Each one holds a full RGBA picture in memory.
func WithPrefetch(frames int) Option { return playback.WithPrefetch(frames) }

// ErrNotSeekable is returned by Player.Seek when the source is not a
// hwmediacodec.PacketSeeker (a plain io.Reader, for example).
var ErrNotSeekable = playback.ErrNotSeekable

// Player plays hardware-decoded video and keeps the current frame in an
// *ebiten.Image. It is a playback.Player whose frames are uploaded to a
// texture.
//
// Decoding runs on a background goroutine; the methods of Player are meant
// to be called from the game's goroutine (Update and Draw).
type Player struct {
	core    *playback.Player
	img     *ebiten.Image
	scratch []byte
	width   int
	height  int
	last    time.Time
}

// NewPlayer starts decoding the Annex-B elementary stream r (for example a
// .h264 or .hevc file) of codec c, to be shown at fps frames per second.
// Playback starts paused; call Play. When r is an io.ReadSeeker the player
// can loop and Seek (the stream is scanned once for keyframes on the first
// seek).
func NewPlayer(r io.Reader, c hwmediacodec.Codec, fps float64, opts ...Option) (*Player, error) {
	core, err := playback.NewStream(r, c, fps, rgba(opts)...)
	if err != nil {
		return nil, err
	}
	return &Player{core: core}, nil
}

// NewPlayerFromSource starts decoding packets from src, which carries its
// own presentation times (an MP4 demuxer, for example). Looping and Seek
// need src to be a hwmediacodec.PacketSeeker.
func NewPlayerFromSource(src hwmediacodec.PacketReader, opts ...Option) (*Player, error) {
	core, err := playback.New(src, rgba(opts)...)
	if err != nil {
		return nil, err
	}
	return &Player{core: core}, nil
}

// rgba pins the frame format to what WritePixels takes, whatever the
// options say.
func rgba(opts []Option) []Option {
	return append(opts[:len(opts):len(opts)], playback.WithOutputFormat(hwmediacodec.RGBA))
}

// Play starts or resumes playback.
func (p *Player) Play() { p.core.Play() }

// Pause stops the clock; the current picture stays.
func (p *Player) Pause() { p.core.Pause() }

// IsPlaying reports whether the clock is running: Play was called and the
// stream has neither ended nor failed.
func (p *Player) IsPlaying() bool { return p.core.IsPlaying() }

// Ended reports whether the last frame has been shown (never when looping).
func (p *Player) Ended() bool { return p.core.Ended() }

// Err returns the first decoding error.
func (p *Player) Err() error { return p.core.Err() }

// Position is the current playback time within the stream.
func (p *Player) Position() time.Duration { return p.core.Position() }

// Length is the stream duration, or 0 when it is not known (a source that
// cannot seek, or a raw stream that has not been scanned yet).
func (p *Player) Length() time.Duration { return p.core.Length() }

// Seekable reports whether Seek works.
func (p *Player) Seekable() bool { return p.core.Seekable() }

// Seek jumps to t: decoding restarts at the keyframe before t and the next
// picture shown is the one at t. The clock waits for that picture. Seeking
// also resumes a player that has ended.
func (p *Player) Seek(t time.Duration) error { return p.core.Seek(t) }

// Skipped is the number of frames dropped so far because decoding or the
// game loop was behind.
func (p *Player) Skipped() int { return p.core.Skipped() }

// Image returns the current picture, or nil before the first frame. The
// image is updated in place by Update and replaced when the picture size
// changes.
func (p *Player) Image() *ebiten.Image { return p.img }

// Size is the size of the current picture (0, 0 before the first frame).
func (p *Player) Size() (width, height int) { return p.width, p.height }

// Update advances playback by one tick and uploads the frame that became
// due. Call it once from the game's Update.
func (p *Player) Update() error {
	if !p.core.IsPlaying() {
		p.last = time.Time{}
		return p.core.Err()
	}
	f, err := p.core.Advance(p.tick())
	if f != nil {
		p.upload(f)
		f.Release()
	}
	return err
}

// tick is the time one Update stands for: 1/TPS, or the wall-clock time
// since the previous Update when the TPS is SyncWithFPS.
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
		// WritePixels wants tightly packed rows.
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

// Close stops decoding and releases the decoder. The image stays valid.
func (p *Player) Close() error { return p.core.Close() }
