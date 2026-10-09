package mp4_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/bitstream/annexb"
	"github.com/shibukawa/hwmediacodec/internal/mp4test"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

func TestElementaryStream(t *testing.T) {
	mp4test.RequireFFmpeg(t)
	src := mp4test.GenerateMP4(t, t.TempDir(), mp4test.MP4Options{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 25, BFrames: 2, GOP: 10})
	d, err := mp4.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	v := d.Video()

	var want bytes.Buffer
	for i := range v.SampleCount() {
		au, err := v.AccessUnit(i)
		if err != nil {
			t.Fatal(err)
		}
		want.Write(au.Data)
	}
	es := v.ElementaryStream()
	got, err := io.ReadAll(io.LimitReader(es, 1<<30))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("stream has %d bytes, want %d", len(got), want.Len())
	}
	// annexb.Reader splits it back into the same access units.
	if _, err := es.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	r := annexb.NewReader(es, hwmediacodec.H264)
	n := 0
	for {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		want, err := v.AccessUnit(n)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(au, want.Data) {
			t.Fatalf("access unit %d differs (%d vs %d bytes)", n, len(au), len(want.Data))
		}
		n++
	}
	if n != v.SampleCount() {
		t.Fatalf("%d access units after rewind, want %d", n, v.SampleCount())
	}
	if _, err := es.Seek(10, io.SeekStart); err == nil {
		t.Error("seeking into the middle succeeded")
	}
}
