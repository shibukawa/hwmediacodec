// Package assets embeds the sample media so that the players run from any
// directory without arguments.
package assets

import (
	"bytes"
	_ "embed"
	"io"
)

// WaterfallMP4 is a ten-second waterfall clip shot by the repository's
// author (so it can be used freely here): 720x1280 (portrait) HEVC in an
// MP4 with an AAC track, 29.67 frames per second, 293 frames.
//
//go:embed waterfall-720p-hevc.mp4
var WaterfallMP4 []byte

// WaterfallName is the clip's file name, for messages.
const WaterfallName = "waterfall-720p-hevc.mp4"

// Waterfall returns a reader over the clip, which the MP4 demuxer
// accepts in place of a file.
func Waterfall() io.ReadSeeker { return bytes.NewReader(WaterfallMP4) }

// WaterfallH264MP4 is the same clip in H.264 (High profile, level 3.1,
// about 2 Mbit/s), for decoders without HEVC: Firefox does not decode HEVC
// through WebCodecs. Same size, frames, timing and audio track. It was
// made from the HEVC clip with
//
//	ffmpeg -i waterfall-720p-hevc.mp4 -map 0:v:0 -map 0:a:0 \
//	  -c:v libx264 -preset slower -refs 4 -level:v 3.1 -profile:v high -b:v 2000k \
//	  -pix_fmt yuv420p -g 150 -fps_mode passthrough \
//	  -colorspace bt709 -color_primaries bt709 -color_trc bt709 -color_range tv \
//	  -c:a copy -map_metadata -1 -movflags +faststart waterfall-720p-h264.mp4
//
//go:embed waterfall-720p-h264.mp4
var WaterfallH264MP4 []byte

// WaterfallH264Name is the H.264 clip's file name, for messages.
const WaterfallH264Name = "waterfall-720p-h264.mp4"

// Clip is one encoding of the bundled sample video.
type Clip struct {
	// Name is the file name, for messages.
	Name string
	data []byte
}

// Open returns a reader over the clip, which the MP4 demuxer accepts in
// place of a file.
func (c Clip) Open() io.ReadSeeker { return bytes.NewReader(c.data) }

// Clips returns the encodings of the bundled clip in the order a player
// should try them: HEVC, the smaller file, then H.264 for a machine or
// browser that does not decode HEVC. The codec is not part of the entry;
// like for any MP4 it is read from the file.
func Clips() []Clip {
	return []Clip{
		{Name: WaterfallName, data: WaterfallMP4},
		{Name: WaterfallH264Name, data: WaterfallH264MP4},
	}
}
