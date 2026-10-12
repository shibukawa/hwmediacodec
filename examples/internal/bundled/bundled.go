// Package bundled opens the sample clip of package assets in a player, in
// the first of its encodings the machine decodes: HEVC where there is a
// decoder for it, H.264 elsewhere (Firefox, for one, has no HEVC decoder
// behind WebCodecs).
package bundled

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/ebitenvideo"
	"github.com/shibukawa/hwmediacodec/examples/assets"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// Video describes the clip that was opened.
type Video struct {
	Name          string
	Width, Height int
}

// Open returns a player for the bundled clip. Every encoding is tried
// with the options as given; in a browser, where nobody can pass a
// -software flag, a second round allows the browser's software decoders.
// The error of the first attempt is returned when nothing works.
func Open(opts ...ebitenvideo.Option) (*ebitenvideo.Player, Video, error) {
	rounds := [][]ebitenvideo.Option{opts}
	if runtime.GOOS == "js" {
		rounds = append(rounds, append(opts[:len(opts):len(opts)], ebitenvideo.WithSoftwareFallback()))
	}
	return open(assets.Clips(), rounds)
}

// open tries every clip with every round of options, clips first.
func open(clips []assets.Clip, rounds [][]ebitenvideo.Option) (*ebitenvideo.Player, Video, error) {
	var (
		player *ebitenvideo.Player
		video  Video
	)
	err := choose(len(rounds), len(clips), func(round, clip int) error {
		var err error
		player, video, err = openClip(clips[clip], rounds[round])
		return err
	})
	return player, video, err
}

// choose calls try for every round and every clip in it until one attempt
// succeeds. An attempt that fails for another reason than a missing
// decoder ends the search. When all fail, the error is the first one: it
// names the encoding the samples prefer.
func choose(rounds, clips int, try func(round, clip int) error) error {
	var first error
	for r := 0; r < rounds; r++ {
		for c := 0; c < clips; c++ {
			err := try(r, c)
			if err == nil {
				return nil
			}
			if !errors.Is(err, hwmediacodec.ErrUnsupported) {
				return err
			}
			if first == nil {
				first = err
			}
		}
	}
	if first == nil {
		return errors.New("bundled: no clip")
	}
	return first
}

func openClip(clip assets.Clip, opts []ebitenvideo.Option) (*ebitenvideo.Player, Video, error) {
	d, err := mp4.NewDemuxer(clip.Open())
	if err != nil {
		return nil, Video{}, fmt.Errorf("%s: %w", clip.Name, err)
	}
	v := d.Video()
	if v == nil {
		return nil, Video{}, fmt.Errorf("%s has no H.264, HEVC or AV1 video track", clip.Name)
	}
	p, err := ebitenvideo.NewPlayerFromSource(v.PacketSource(), opts...)
	if err != nil {
		return nil, Video{}, err
	}
	return p, Video{Name: clip.Name, Width: v.Width, Height: v.Height}, nil
}
