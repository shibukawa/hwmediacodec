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

// WaterfallAV1MP4 is the same clip in AV1 (Main profile, 8-bit 4:2:0,
// about 0.9 Mbit/s) with the same audio track, for checking AV1 decoders;
// it is not one of Clips because few machines decode AV1 in hardware yet.
// It was made from the HEVC clip with
//
//	ffmpeg -i waterfall-720p-hevc.mp4 -map 0:v:0 -map 0:a:0 \
//	  -c:v libsvtav1 -preset 4 -b:v 900k -svtav1-params tune=0 \
//	  -pix_fmt yuv420p -g 150 -fps_mode passthrough \
//	  -colorspace bt709 -color_primaries bt709 -color_trc bt709 -color_range tv \
//	  -c:a copy -map_metadata -1 -movflags +faststart waterfall-720p-av1.mp4
//
//go:embed waterfall-720p-av1.mp4
var WaterfallAV1MP4 []byte

// WaterfallAV1Name is the AV1 clip's file name, for messages.
const WaterfallAV1Name = "waterfall-720p-av1.mp4"

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

// AV1 returns the AV1 encoding of the clip (WaterfallAV1MP4).
func AV1() Clip { return Clip{Name: WaterfallAV1Name, data: WaterfallAV1MP4} }
