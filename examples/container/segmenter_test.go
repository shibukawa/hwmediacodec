package container_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/examples/container"
	"github.com/shibukawa/hwmediacodec/examples/internal/testutil"
)

// segmentSource feeds the access units of an ffmpeg-made file (no
// B-frames, keyframe every 10 frames at 30 fps) through a Segmenter, so
// the segmenter is tested without a hardware encoder.
func segmentSource(t *testing.T, dir string, c hwmediacodec.Codec, target time.Duration) (src string, init []byte, segs []container.Segment) {
	t.Helper()
	src = testutil.GenerateMP4(t, dir, testutil.MP4Options{Codec: c, Width: 160, Height: 120, Frames: 90, BFrames: 0, GOP: 10})
	d, err := container.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	v := d.Video()
	seg, err := container.NewSegmenter(c, v.TimeScale, target,
		func(b []byte) error { init = b; return nil },
		func(s container.Segment) error { segs = append(segs, s); return nil })
	if err != nil {
		t.Fatal(err)
	}
	for i := range v.SampleCount() {
		p, err := v.Packet(i)
		if err != nil {
			t.Fatal(err)
		}
		if err := seg.WritePacket(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := seg.Close(); err != nil {
		t.Fatal(err)
	}
	return src, init, segs
}

func TestSegmenter(t *testing.T) {
	testutil.RequireFFmpeg(t)
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC} {
		t.Run(c.String(), func(t *testing.T) {
			dir := t.TempDir()
			src, init, segs := segmentSource(t, dir, c, time.Second)
			if init == nil {
				t.Fatal("no init segment")
			}
			// Keyframes every 1/3 s and a 1 s target give three 1 s segments.
			if len(segs) != 3 {
				t.Fatalf("%d segments, want 3", len(segs))
			}
			for i, s := range segs {
				if s.Seq != i+1 {
					t.Errorf("segment %d has seq %d", i, s.Seq)
				}
				if d := s.Duration; d < 990*time.Millisecond || d > 1010*time.Millisecond {
					t.Errorf("segment %d lasts %v, want 1 s", i, d)
				}
				if s.Start != time.Duration(i)*time.Second {
					t.Errorf("segment %d starts at %v", i, s.Start)
				}
				// Each segment is one fragment of 30 samples starting with a
				// sync sample and decode times continuing from the previous.
				f, err := mp4.DecodeFile(bytesReader(s.Data))
				if err != nil {
					t.Fatalf("segment %d: %v", i, err)
				}
				if len(f.Segments) != 1 || len(f.Segments[0].Fragments) != 1 {
					t.Fatalf("segment %d has %d segments", i, len(f.Segments))
				}
				frag := f.Segments[0].Fragments[0]
				trun := frag.Moof.Traf.Trun
				if trun.SampleCount() != 30 {
					t.Errorf("segment %d has %d samples", i, trun.SampleCount())
				}
				if !mp4.IsSyncSampleFlags(trun.Samples[0].Flags) || mp4.IsSyncSampleFlags(trun.Samples[1].Flags) {
					t.Errorf("segment %d: sample flags %x %x", i, trun.Samples[0].Flags, trun.Samples[1].Flags)
				}
				if got, want := frag.Moof.Traf.Tfdt.BaseMediaDecodeTime(), uint64(i)*30*512; got != want {
					t.Errorf("segment %d decodes from %d, want %d", i, got, want)
				}
			}
			// init + all segments is a complete fragmented MP4 that decodes to
			// the same pictures as the source.
			all := filepath.Join(dir, "all.mp4")
			var data []byte
			data = append(data, init...)
			for _, s := range segs {
				data = append(data, s.Data...)
			}
			if err := os.WriteFile(all, data, 0o644); err != nil {
				t.Fatal(err)
			}
			testutil.CheckDecodes(t, all)
			if got, want := testutil.FrameMD5(t, all, ""), testutil.FrameMD5(t, src, ""); !slices.Equal(got, want) {
				t.Errorf("segments decode differently from the source (%d vs %d frames)", len(got), len(want))
			}
			if vs := testutil.VideoStream(t, all); vs.CodecName != c.String() || vs.Width != 160 {
				t.Errorf("stream is %s %dx%d", vs.CodecName, vs.Width, vs.Height)
			}
		})
	}
}

func TestSegmenterShortTarget(t *testing.T) {
	testutil.RequireFFmpeg(t)
	// A target shorter than the keyframe interval cuts at every keyframe.
	_, _, segs := segmentSource(t, t.TempDir(), hwmediacodec.H264, 100*time.Millisecond)
	if len(segs) != 9 {
		t.Fatalf("%d segments, want 9 (one per keyframe)", len(segs))
	}
	for _, s := range segs {
		if d := s.Duration; d < 330*time.Millisecond || d > 336*time.Millisecond {
			t.Errorf("segment %d lasts %v", s.Seq, d)
		}
	}
}
