package mp4_test

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	mp4ff "github.com/Eyevinn/mp4ff/mp4"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/mediatest"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// quickTimeScale rewrites an ffmpeg file into the
// shape of a QuickTime or iPhone recording: movie time scale 600, with the
// movie, track and edit durations in that unit. Box sizes do not change,
// so the chunk offsets stay valid.
func quickTimeScale(t *testing.T, src, dst string) {
	t.Helper()
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	file, err := mp4ff.DecodeFile(f)
	if err != nil {
		t.Fatal(err)
	}
	// (ffmpeg writes 1000, or the audio sample rate when there is sound.)
	mvhd := file.Moov.Mvhd
	from, to := uint64(mvhd.Timescale), uint64(600)
	if from == to {
		t.Fatalf("ffmpeg already wrote a movie time scale of %d", from)
	}
	scale := func(v uint64) uint64 { return v * to / from }
	mvhd.Timescale = uint32(to)
	mvhd.Duration = scale(mvhd.Duration)
	edits := 0
	for _, trak := range file.Moov.Traks {
		trak.Tkhd.Duration = scale(trak.Tkhd.Duration)
		if trak.Edts == nil {
			continue
		}
		for _, elst := range trak.Edts.Elst {
			for i := range elst.Entries {
				elst.Entries[i].SegmentDuration = scale(elst.Entries[i].SegmentDuration)
				edits++
			}
		}
	}
	if edits == 0 {
		t.Fatal("the source has no edit list to rescale")
	}
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := file.Encode(out); err != nil {
		t.Fatal(err)
	}
}

// TestPassthroughRescalesEditList copies the tracks of a file with a
// movie time scale of 600 into the writer's 1000. The edit lists' segment
// durations are in the movie time scale, so copying them unchanged would
// make a player cut every track to 60 percent of its length.
func TestPassthroughRescalesEditList(t *testing.T) {
	mediatest.RequireFFmpeg(t)
	dir := t.TempDir()
	orig := mediatest.GenerateMP4(t, dir, mediatest.MP4Options{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 60, BFrames: 2, GOP: 15, Audio: true})
	src := filepath.Join(dir, "mov600.mp4")
	quickTimeScale(t, orig, src)
	d, err := mp4.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.MovieTimeScale() != 600 {
		t.Fatalf("movie time scale %d, want 600", d.MovieTimeScale())
	}
	if d.Duration() < 1900e6 || d.Duration() > 2100e6 {
		t.Fatalf("movie lasts %v, want about 2 s", d.Duration())
	}

	out := filepath.Join(dir, "copy.mp4")
	m, err := mp4.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	tracks := append([]*mp4.Track{&d.Video().Track}, d.Others()...)
	for _, tr := range tracks {
		w := m.AddPassthroughTrack(tr)
		for i := range tr.SampleCount() {
			s, err := tr.Sample(i)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.WriteSample(s); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	mediatest.CheckDecodes(t, out)

	// The boxes: every segment duration is the source's, rounded up into
	// the new scale, and the track header carries their sum.
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	file, err := mp4ff.DecodeFile(f, mp4ff.WithDecodeMode(mp4ff.DecModeLazyMdat))
	if err != nil {
		t.Fatal(err)
	}
	if file.Moov.Mvhd.Timescale != 1000 {
		t.Fatalf("output movie time scale %d", file.Moov.Mvhd.Timescale)
	}
	if len(file.Moov.Traks) != len(tracks) {
		t.Fatalf("%d output tracks, want %d", len(file.Moov.Traks), len(tracks))
	}
	for i, trak := range file.Moov.Traks {
		srcEdts := tracks[i].Trak.Edts
		if (trak.Edts == nil) != (srcEdts == nil) {
			t.Fatalf("track %d: edit list present %v, source %v", i, trak.Edts != nil, srcEdts != nil)
		}
		if trak.Edts == nil {
			continue
		}
		var total uint64
		for j, elst := range trak.Edts.Elst {
			for k, e := range elst.Entries {
				want := srcEdts.Elst[j].Entries[k]
				wantDur := (want.SegmentDuration*1000 + 599) / 600
				if e.SegmentDuration != wantDur || e.MediaTime != want.MediaTime {
					t.Errorf("track %d edit %d: %d/%d, want %d/%d", i, k, e.SegmentDuration, e.MediaTime, wantDur, want.MediaTime)
				}
				total += e.SegmentDuration
			}
		}
		if trak.Tkhd.Duration != total {
			t.Errorf("track %d header lasts %d, edits sum to %d", i, trak.Tkhd.Duration, total)
		}
	}
	// And ffprobe, which honours edit lists, sees streams as long as the
	// source's; without the rescaling they would be 0.6 times as long.
	srcStreams, outStreams := mediatest.Streams(t, src), mediatest.Streams(t, out)
	if len(srcStreams) != len(outStreams) {
		t.Fatalf("%d streams, want %d", len(outStreams), len(srcStreams))
	}
	for i := range srcStreams {
		s, o := srcStreams[i].Seconds(), outStreams[i].Seconds()
		if math.Abs(s-o) > 0.01 {
			t.Errorf("%s stream lasts %.3f s, source %.3f s", outStreams[i].CodecType, o, s)
		}
	}
}
