package mp4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"testing"
	"time"

	mp4ff "github.com/Eyevinn/mp4ff/mp4"
	"github.com/shibukawa/hwmediacodec"
)

func TestDemuxerRejectsTruncatedLazyMdat(t *testing.T) {
	data := binary.BigEndian.AppendUint32(nil, 8)
	data = append(data, "moov"...)
	data = binary.BigEndian.AppendUint32(data, 1<<30)
	data = append(data, "mdat"...)
	if _, err := NewDemuxer(bytes.NewReader(data)); err == nil {
		t.Fatal("lazy mdat beyond the file accepted")
	}
}

type shortWriteSeeker struct{}

func (shortWriteSeeker) Write(p []byte) (int, error)    { return len(p) / 2, nil }
func (shortWriteSeeker) Seek(int64, int) (int64, error) { return 0, nil }

func TestMuxerRejectsShortWrites(t *testing.T) {
	if _, err := NewMuxer(shortWriteSeeker{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("NewMuxer = %v", err)
	}
}

func TestAudioWriterRejectsUnsupportedTimeScale(t *testing.T) {
	m := &Muxer{}
	if _, err := m.AddAudioTrack(hwmediacodec.AudioFormat{Codec: hwmediacodec.AAC, SampleRate: 48000, Channels: 2}, 1<<31); err == nil {
		t.Fatal("time scale outside PacketReader's signed 32-bit range accepted")
	}
}

func TestMuxerRejectsUnrepresentableSampleTiming(t *testing.T) {
	for _, samples := range [][]outSample{
		{{dts: 0, pts: 0}, {dts: math.MaxUint32 + 1, pts: math.MaxUint32 + 1}},
		{{dts: 0, pts: math.MaxInt32 + 1}},
	} {
		tr := &trackWriter{samples: samples}
		if _, _, _, _, _, err := tr.sampleTable(); err == nil {
			t.Fatal("timing that truncates a 32-bit field accepted")
		}
	}
}

func TestSegmenterReleasesPacketReferences(t *testing.T) {
	s := &Segmenter{timeScale: 90000, pending: []pendingSample{{data: []byte{1}, sync: true}}}
	backing := s.pending
	if err := s.emit(-1); err != nil {
		t.Fatal(err)
	}
	if backing[0].data != nil {
		t.Fatal("emitted packet data retained in pending buffer")
	}
}

func safetyTrack() (*Demuxer, *mp4ff.TrakBox) {
	init := mp4ff.CreateEmptyInit()
	trak := init.AddEmptyTrack(90000, "video", "und")
	stbl := trak.Mdia.Minf.Stbl
	stbl.Stsz = &mp4ff.StszBox{SampleUniformSize: 1, SampleNumber: 1}
	stbl.Stts = &mp4ff.SttsBox{SampleCount: []uint32{1}, SampleTimeDelta: []uint32{3000}}
	stbl.Stsc = &mp4ff.StscBox{Entries: []mp4ff.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1}}}
	stbl.Stco = &mp4ff.StcoBox{ChunkOffset: []uint32{8}}
	return &Demuxer{file: &mp4ff.File{Mdat: &mp4ff.MdatBox{Data: []byte{1}}}}, trak
}

func TestTrackRejectsMalformedTables(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*mp4ff.TrakBox)
	}{
		{"missing tkhd", func(tr *mp4ff.TrakBox) { tr.Tkhd = nil }},
		{"zero time scale", func(tr *mp4ff.TrakBox) { tr.Mdia.Mdhd.Timescale = 0 }},
		{"sample count", func(tr *mp4ff.TrakBox) {
			tr.Mdia.Minf.Stbl.Stsz.SampleNumber = math.MaxUint32
			tr.Mdia.Minf.Stbl.Stts.SampleCount[0] = math.MaxUint32
		}},
		{"time count", func(tr *mp4ff.TrakBox) { tr.Mdia.Minf.Stbl.Stts.SampleCount[0] = 0 }},
		{"zero chunk", func(tr *mp4ff.TrakBox) { tr.Mdia.Minf.Stbl.Stsc.Entries[0].FirstChunk = 0 }},
		{"overflow chunk", func(tr *mp4ff.TrakBox) {
			tr.Mdia.Minf.Stbl.Stsc.Entries = append(tr.Mdia.Minf.Stbl.Stsc.Entries, mp4ff.StscEntry{FirstChunk: 0, SamplesPerChunk: 1})
		}},
		{"sample outside mdat", func(tr *mp4ff.TrakBox) { tr.Mdia.Minf.Stbl.Stco.ChunkOffset[0] = math.MaxUint32 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, tr := safetyTrack()
			test.change(tr)
			if _, err := d.newTrack(tr); err == nil {
				t.Fatal("malformed track accepted")
			}
		})
	}
}

func TestTrackLongRunningTimes(t *testing.T) {
	tr := Track{TimeScale: 90000}
	const ts = 48 * 60 * 60 * 90000
	if got := tr.TimeOf(ts); got != 48*time.Hour {
		t.Fatalf("48-hour timestamp = %v", got)
	}
	v := &VideoTrack{Track: tr}
	v.samples = []sampleInfo{{dts: 0, sync: true}, {dts: ts, sync: true}}
	if got := v.KeyframeAtOrBefore(49 * time.Hour); got != 1 {
		t.Fatalf("long-stream seek = %d", got)
	}
}
