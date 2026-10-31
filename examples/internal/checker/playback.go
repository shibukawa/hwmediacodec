package checker

import (
	"errors"
	"fmt"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/ebitenvideo"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// Question is what the playback stage is waiting for.
type Question int

const (
	Watching Question = iota
	AskPicture
	AskSound
)

// Playback is the playback stage without its window: the clip of every
// codec some backend decodes plays with sound for a while, then the user
// is asked whether picture and sound were right. The caller draws
// Player's picture, reads the answers and passes them on, and calls
// Update once per tick; Done reports the end of the stage.
type Playback struct {
	r        *Report
	playLen  time.Duration
	software bool

	queue      []Clip
	player     *ebitenvideo.Player
	playing    Clip
	label      string // "H.264 picture", with "(software decoder)" when it is one
	start      time.Time
	ask        Question
	soundAsked bool
}

// NewPlayback queues the clips and starts the first. Each plays playLen
// before the question. With software, a codec no backend decodes in
// hardware plays through the OS software decoder and is labelled so in
// the report; it is for the iOS Simulator and Android emulators, which
// have no hardware decoder.
func NewPlayback(r *Report, playLen time.Duration, software bool) *Playback {
	p := &Playback{r: r, playLen: playLen, software: software}
	caps := r.Caps()
	for _, cl := range Clips() {
		queued := false
		for _, c := range caps {
			if c.Codec == cl.Codec && c.Direction == hwmediacodec.Decode {
				queued = true
				break
			}
		}
		if queued || software {
			p.queue = append(p.queue, cl)
		}
	}
	p.next()
	return p
}

// Done reports whether every clip was played or skipped.
func (p *Playback) Done() bool { return p.player == nil }

// Player is the clip playing now, nil when Done.
func (p *Playback) Player() *ebitenvideo.Player { return p.player }

// Codec is the codec of the clip playing now.
func (p *Playback) Codec() hwmediacodec.Codec { return p.playing.Codec }

// Question is what the stage waits for.
func (p *Playback) Question() Question { return p.ask }

// Left is how long the clip plays before the question.
func (p *Playback) Left() time.Duration { return max(0, p.playLen-time.Since(p.start)) }

func (p *Playback) next() {
	if p.player != nil {
		p.player.Close()
		p.player = nil
	}
	for len(p.queue) > 0 {
		cl := p.queue[0]
		p.queue = p.queue[1:]
		p.label = CodecName(cl.Codec) + " picture"
		if b := p.r.Backend(cl.Codec, hwmediacodec.Decode); b != "" {
			p.label += " / " + b
		}
		pl, err := openClip(cl, false)
		if errors.Is(err, hwmediacodec.ErrUnsupported) && p.software {
			p.label = CodecName(cl.Codec) + " picture (software decoder)"
			pl, err = openClip(cl, true)
		}
		if err != nil {
			res := p.r.Add("Playback", p.label)
			p.r.Set(res, StatusOf(err), "%v", err)
			continue
		}
		if err := pl.AudioErr(); err != nil && !p.soundAsked {
			p.soundAsked = true
			res := p.r.Add("Playback", "Sound (AAC)")
			p.r.Set(res, Failed, "%v", err)
		}
		pl.Play()
		p.player, p.playing, p.start, p.ask = pl, cl, time.Now(), Watching
		return
	}
}

func openClip(cl Clip, software bool) (*ebitenvideo.Player, error) {
	d, err := mp4.NewDemuxer(cl.Clip.Open())
	if err != nil {
		return nil, err
	}
	v := d.Video()
	if v == nil {
		return nil, fmt.Errorf("no video track in %s", cl.Clip.Name)
	}
	var opts []ebitenvideo.Option
	if a := d.Audio(); a != nil {
		opts = append(opts, ebitenvideo.WithAudio(a.PacketSource()))
	}
	if software {
		opts = append(opts, ebitenvideo.WithSoftwareFallback())
	}
	return ebitenvideo.NewPlayerFromSource(v.PacketSource(), opts...)
}

// Update advances the player and puts the question once the clip has
// played long enough. It reports whether a failure moved on to the next
// clip, in which case the caller ignores this tick's input.
func (p *Playback) Update() (moved bool) {
	if p.player == nil {
		return false
	}
	if err := p.player.Update(); err != nil {
		res := p.r.Add("Playback", p.label)
		p.r.Set(res, Failed, "%v", err)
		p.next()
		return true
	}
	if p.ask == Watching && time.Since(p.start) >= p.playLen {
		p.ask = AskPicture
	}
	return false
}

// Skip records the clip as skipped and moves on.
func (p *Playback) Skip() {
	if p.player == nil {
		return
	}
	res := p.r.Add("Playback", p.label)
	p.r.Set(res, Skipped, "skipped by the user")
	p.next()
}

// Answer records the user's answer to the question; it does nothing while
// the clip is still Watching.
func (p *Playback) Answer(yes bool) {
	switch {
	case p.player == nil:
	case p.ask == AskPicture:
		res := p.r.Add("Playback", p.label)
		p.r.Set(res, pick(yes), "user answered %s; %d frames skipped by the player", yesNo(yes), p.player.Skipped())
		if p.player.HasAudio() && !p.soundAsked {
			p.ask = AskSound
			return
		}
		p.next()
	case p.ask == AskSound:
		p.soundAsked = true
		res := p.r.Add("Playback", "Sound (AAC)")
		p.r.Set(res, pick(yes), "user answered %s", yesNo(yes))
		p.next()
	}
}

// Close stops the player.
func (p *Playback) Close() {
	if p.player != nil {
		p.player.Close()
		p.player = nil
	}
	p.queue = nil
}

func pick(yes bool) Status {
	if yes {
		return Passed
	}
	return Failed
}

func yesNo(yes bool) string {
	if yes {
		return "yes"
	}
	return "no"
}
