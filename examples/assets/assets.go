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
