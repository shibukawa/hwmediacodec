package main

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/mediatest"
)

func TestConvert(t *testing.T) {
	mediatest.RequireFFmpeg(t)
	cases := []struct {
		name string
		in   mediatest.MP4Options
		opts options
		minB int
	}{
		{
			name: "h264_to_hevc_bitrate",
			in:   mediatest.MP4Options{Codec: hwmediacodec.H264, Width: 320, Height: 240, Frames: 60, BFrames: 2, GOP: 20, Audio: true},
			opts: options{codec: hwmediacodec.HEVC, bitrate: 2_000_000, gop: 30},
		},
		{
			name: "hevc_to_h264_quality_bframes",
			in:   mediatest.MP4Options{Codec: hwmediacodec.HEVC, Width: 320, Height: 240, Frames: 45, BFrames: 0, GOP: 15},
			opts: options{codec: hwmediacodec.H264, quality: 0.7, bframes: true},
			minB: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mediatest.RequireHardware(t, c.in.Codec, hwmediacodec.Decode)
			mediatest.RequireHardware(t, c.opts.codec, hwmediacodec.Encode)
			dir := t.TempDir()
			src := mediatest.GenerateMP4(t, dir, c.in)
			out := filepath.Join(dir, "out.mp4")
			if err := run(context.Background(), c.opts, src, out, io.Discard); err != nil {
				t.Fatal(err)
			}
			mediatest.CheckDecodes(t, out)

			vs, vo := mediatest.VideoStream(t, src), mediatest.VideoStream(t, out)
			if vo.CodecName != c.opts.codec.String() {
				t.Errorf("output codec %s, want %s", vo.CodecName, c.opts.codec)
			}
			if vo.FrameCount() != vs.FrameCount() {
				t.Errorf("%d frames, want %d", vo.FrameCount(), vs.FrameCount())
			}
			if vo.Width != vs.Width || vo.Height != vs.Height {
				t.Errorf("size %dx%d, want %dx%d", vo.Width, vo.Height, vs.Width, vs.Height)
			}
			if vo.HasB < c.minB {
				t.Errorf("has_b_frames %d, want at least %d", vo.HasB, c.minB)
			}
			if psnr := mediatest.PSNR(t, src, out); psnr < 30 {
				t.Errorf("PSNR %.2f dB against the source, want at least 30", psnr)
			}
			// Timestamps are carried through unchanged.
			var want, got []int64
			for _, f := range mediatest.Frames(t, src) {
				want = append(want, f.PTS)
			}
			for _, f := range mediatest.Frames(t, out) {
				got = append(got, f.PTS)
			}
			if !slices.Equal(got, want) {
				t.Errorf("presentation times differ:\n got %v\nwant %v", got, want)
			}
			// Other tracks are copied.
			if c.in.Audio {
				var as, ao *mediatest.Stream
				for _, s := range mediatest.Streams(t, src) {
					if s.CodecType == "audio" {
						s := s
						as = &s
					}
				}
				for _, s := range mediatest.Streams(t, out) {
					if s.CodecType == "audio" {
						s := s
						ao = &s
					}
				}
				if as == nil || ao == nil {
					t.Fatalf("audio track missing: src %v out %v", as != nil, ao != nil)
				}
				if ao.CodecName != as.CodecName || ao.PacketCount() != as.PacketCount() {
					t.Errorf("audio is %s with %d packets, want %s with %d", ao.CodecName, ao.PacketCount(), as.CodecName, as.PacketCount())
				}
			}
		})
	}
}

func TestParseBitrate(t *testing.T) {
	for in, want := range map[string]int{"": 0, "6M": 6_000_000, "2500k": 2_500_000, "1.5M": 1_500_000, "800000": 800000} {
		got, err := parseBitrate(in)
		if err != nil || got != want {
			t.Errorf("parseBitrate(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseBitrate("fast"); err == nil {
		t.Error("parseBitrate(fast) succeeded")
	}
}
