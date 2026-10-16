package mp4_test

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/mediatest"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// TestDemuxerReaderNotAtStart opens the same file twice: from a reader at
// 0 and from one positioned after 100 bytes of junk. Chunk offsets count
// from the start of the file, not of the reader, so every sample must
// come out the same.
func TestDemuxerReaderNotAtStart(t *testing.T) {
	mediatest.RequireFFmpeg(t)
	path := mediatest.GenerateMP4(t, t.TempDir(), mediatest.MP4Options{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 20, BFrames: 2, GOP: 10, Audio: true})
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := mp4.NewDemuxer(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	junk := bytes.Repeat([]byte{0xAB}, 100)
	r := bytes.NewReader(append(junk, file...))
	if _, err := r.Seek(int64(len(junk)), io.SeekStart); err != nil {
		t.Fatal(err)
	}
	shifted, err := mp4.NewDemuxer(r)
	if err != nil {
		t.Fatalf("file behind a junk prefix: %v", err)
	}
	if plain.MovieTimeScale() != shifted.MovieTimeScale() || plain.Duration() != shifted.Duration() {
		t.Errorf("movie differs: %d/%v vs %d/%v", plain.MovieTimeScale(), plain.Duration(), shifted.MovieTimeScale(), shifted.Duration())
	}
	compare := func(name string, a, b *mp4.Track) {
		if a.SampleCount() != b.SampleCount() {
			t.Fatalf("%s: %d vs %d samples", name, a.SampleCount(), b.SampleCount())
		}
		for i := range a.SampleCount() {
			want, err := a.Sample(i)
			if err != nil {
				t.Fatal(err)
			}
			got, err := b.Sample(i)
			if err != nil {
				t.Fatalf("%s sample %d behind the junk prefix: %v", name, i, err)
			}
			if !bytes.Equal(got.Data, want.Data) || got.DTS != want.DTS || got.PTS != want.PTS || got.Duration != want.Duration || got.Keyframe != want.Keyframe {
				t.Fatalf("%s sample %d differs behind the junk prefix", name, i)
			}
		}
	}
	compare("video", &plain.Video().Track, &shifted.Video().Track)
	if len(plain.Others()) != 1 || len(shifted.Others()) != 1 {
		t.Fatalf("%d and %d other tracks", len(plain.Others()), len(shifted.Others()))
	}
	compare("audio", plain.Others()[0], shifted.Others()[0])
	// AccessUnit reads through its own path.
	for i := range plain.Video().SampleCount() {
		want, err := plain.Video().AccessUnit(i)
		if err != nil {
			t.Fatal(err)
		}
		got, err := shifted.Video().AccessUnit(i)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Data, want.Data) {
			t.Fatalf("access unit %d differs behind the junk prefix", i)
		}
	}
	// A file whose mdat runs past the reader's end is still refused when
	// the reader starts late: the check is in the reader's frame too.
	short := bytes.NewReader(append(junk, file[:len(file)-10]...))
	if _, err := short.Seek(int64(len(junk)), io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := mp4.NewDemuxer(short); err == nil {
		t.Error("truncated file behind a junk prefix accepted")
	}
}
