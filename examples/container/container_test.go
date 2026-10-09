package container_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/examples/container"
	"github.com/shibukawa/hwmediacodec/examples/internal/testutil"
)

// These tests need ffmpeg but no hardware: they check the container layer
// against ffprobe's view of the same files.

func rawFormat(c hwmediacodec.Codec) string {
	if c == hwmediacodec.HEVC {
		return "hevc"
	}
	return "h264"
}

func normalize(pts []int64) []int64 {
	out := slices.Clone(pts)
	slices.Sort(out)
	base := out[0]
	for i := range out {
		out[i] -= base
	}
	return out
}

func TestDemuxMatchesFFprobe(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cases := []testutil.MP4Options{
		{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 40, BFrames: 2, GOP: 12, Audio: true},
		{Codec: hwmediacodec.HEVC, Width: 160, Height: 120, Frames: 30, BFrames: 2, GOP: 10},
		{Codec: hwmediacodec.HEVC, Width: 160, Height: 120, Frames: 20, BFrames: 0, GOP: 10, HEV1: true},
	}
	for _, c := range cases {
		name := c.Codec.String()
		if c.HEV1 {
			name += "_hev1"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := testutil.GenerateMP4(t, dir, c)
			d, err := container.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			v := d.Video()
			if v == nil {
				t.Fatal("no video track")
			}
			if v.Codec != c.Codec || v.Width != c.Width || v.Height != c.Height {
				t.Fatalf("track is %s %dx%d, want %s %dx%d", v.Codec, v.Width, v.Height, c.Codec, c.Width, c.Height)
			}
			if got := v.SampleCount(); got != c.Frames {
				t.Fatalf("%d samples, want %d", got, c.Frames)
			}
			if fps := v.FrameRate(); fps < 29.9 || fps > 30.1 {
				t.Errorf("frame rate %.3f, want 30", fps)
			}
			if c.Audio && len(d.Others()) != 1 {
				t.Errorf("%d other tracks, want 1 audio track", len(d.Others()))
			}

			// Presentation times and keyframes agree with ffprobe.
			ref := testutil.Frames(t, path)
			if len(ref) != c.Frames {
				t.Fatalf("ffprobe sees %d frames", len(ref))
			}
			var refPTS, refKey []int64
			for _, f := range ref {
				refPTS = append(refPTS, f.PTS)
				if f.Keyframe {
					refKey = append(refKey, f.PTS)
				}
			}
			var pts, key []int64
			for i := range v.SampleCount() {
				_, p, k := v.Info(i)
				pts = append(pts, p)
				if k {
					key = append(key, p)
				}
			}
			if !slices.Equal(normalize(pts), normalize(refPTS)) {
				t.Errorf("PTS sequence differs from ffprobe:\n got %v\nwant %v", normalize(pts), normalize(refPTS))
			}
			if len(key) != len(refKey) {
				t.Errorf("%d keyframes, ffprobe sees %d", len(key), len(refKey))
			}
			if want := len(key); len(v.Keyframes()) != want {
				t.Errorf("Keyframes() has %d entries, want %d", len(v.Keyframes()), want)
			}
			if i := v.KeyframeAtOrBefore(0); i != 0 {
				t.Errorf("KeyframeAtOrBefore(0) = %d", i)
			}
			if i := v.KeyframeAtOrBefore(v.TimeOf(v.Duration()) + time.Hour); i != v.Keyframes()[len(v.Keyframes())-1] {
				t.Errorf("KeyframeAtOrBefore(end) = %d, want the last keyframe", i)
			}

			// The Annex-B access units decode to the same pictures as the
			// MP4 itself.
			raw := filepath.Join(dir, "dump."+rawFormat(c.Codec))
			f, err := os.Create(raw)
			if err != nil {
				t.Fatal(err)
			}
			for i := range v.SampleCount() {
				au, err := v.AccessUnit(i)
				if err != nil {
					t.Fatal(err)
				}
				if au.Keyframe != (slices.Index(v.Keyframes(), i) >= 0) {
					t.Errorf("sample %d keyframe flag mismatch", i)
				}
				f.Write(au.Data)
			}
			f.Close()
			want := testutil.FrameMD5(t, path, "")
			got := testutil.FrameMD5(t, raw, rawFormat(c.Codec))
			if !slices.Equal(got, want) {
				t.Errorf("elementary stream decodes differently from the MP4 (%d vs %d frames)", len(got), len(want))
			}
		})
	}
}

func TestMuxRoundTrip(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cases := []testutil.MP4Options{
		{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 40, BFrames: 2, GOP: 12, Audio: true},
		{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 20, BFrames: 0, GOP: 10},
		{Codec: hwmediacodec.HEVC, Width: 160, Height: 120, Frames: 30, BFrames: 2, GOP: 10, Audio: true},
	}
	for _, c := range cases {
		t.Run(c.Codec.String()+"_b"+string(rune('0'+c.BFrames)), func(t *testing.T) {
			dir := t.TempDir()
			src := testutil.GenerateMP4(t, dir, c)
			d, err := container.Open(src)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			v := d.Video()

			out := filepath.Join(dir, "remux.mp4")
			m, err := container.Create(out)
			if err != nil {
				t.Fatal(err)
			}
			vw, err := m.AddVideoTrack(v.Codec, v.TimeScale)
			if err != nil {
				t.Fatal(err)
			}
			type pass struct {
				src  *container.Track
				dst  *container.TrackWriter
				next int
			}
			var others []*pass
			for _, tr := range d.Others() {
				others = append(others, &pass{src: tr, dst: m.AddPassthroughTrack(tr)})
			}
			copyUpTo := func(limit time.Duration) {
				for _, p := range others {
					for p.next < p.src.SampleCount() {
						dts, _, _ := p.src.Info(p.next)
						if limit >= 0 && p.src.TimeOf(dts) > limit {
							break
						}
						s, err := p.src.Sample(p.next)
						if err != nil {
							t.Fatal(err)
						}
						if err := p.dst.WriteSample(s); err != nil {
							t.Fatal(err)
						}
						p.next++
					}
				}
			}
			for i := range v.SampleCount() {
				pkt, err := v.Packet(i)
				if err != nil {
					t.Fatal(err)
				}
				if err := vw.WritePacket(pkt); err != nil {
					t.Fatal(err)
				}
				copyUpTo(v.TimeOf(pkt.DTS))
			}
			copyUpTo(-1)
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}

			testutil.CheckDecodes(t, out)
			srcStreams, outStreams := testutil.Streams(t, src), testutil.Streams(t, out)
			if len(srcStreams) != len(outStreams) {
				t.Fatalf("%d streams, want %d", len(outStreams), len(srcStreams))
			}
			for i := range srcStreams {
				s, o := srcStreams[i], outStreams[i]
				if s.CodecName != o.CodecName || s.CodecType != o.CodecType {
					t.Errorf("stream %d is %s/%s, want %s/%s", i, o.CodecType, o.CodecName, s.CodecType, s.CodecName)
				}
				if s.PacketCount() != o.PacketCount() {
					t.Errorf("stream %d has %d packets, want %d", i, o.PacketCount(), s.PacketCount())
				}
				if s.CodecType == "video" && (o.Width != s.Width || o.Height != s.Height || o.HasB != s.HasB) {
					t.Errorf("video stream %dx%d has_b_frames %d, want %dx%d %d", o.Width, o.Height, o.HasB, s.Width, s.Height, s.HasB)
				}
				if c.Codec == hwmediacodec.HEVC && s.CodecType == "video" && o.Tag != "hvc1" {
					t.Errorf("HEVC sample entry is %s, want hvc1", o.Tag)
				}
			}
			// Same pictures, same presentation times (the edit list hides
			// the B-frame delay exactly as ffmpeg's does).
			if got, want := testutil.FrameMD5(t, out, ""), testutil.FrameMD5(t, src, ""); !slices.Equal(got, want) {
				t.Errorf("remuxed video decodes differently (%d vs %d frames)", len(got), len(want))
			}
			var gotPTS, wantPTS []int64
			for _, f := range testutil.Frames(t, out) {
				gotPTS = append(gotPTS, f.PTS)
			}
			for _, f := range testutil.Frames(t, src) {
				wantPTS = append(wantPTS, f.PTS)
			}
			if !slices.Equal(gotPTS, wantPTS) {
				t.Errorf("presentation times differ:\n got %v\nwant %v", gotPTS, wantPTS)
			}
			if sd, od := testutil.VideoStream(t, src).Seconds(), testutil.VideoStream(t, out).Seconds(); od < sd-0.05 || od > sd+0.05 {
				t.Errorf("duration %.3fs, want %.3fs", od, sd)
			}
		})
	}
}
