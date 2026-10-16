package mp4

import (
	"bytes"
	"math"
	"runtime"
	"strings"
	"testing"

	mp4ff "github.com/Eyevinn/mp4ff/mp4"
)

// manySamplesTrack is a track of n one-byte samples in one chunk over a
// lazy mdat of n bytes: the layout a hostile file uses to make the sample
// count as large as the media data.
func manySamplesTrack(n uint32) (*Demuxer, *mp4ff.TrakBox) {
	init := mp4ff.CreateEmptyInit()
	trak := init.AddEmptyTrack(90000, "video", "und")
	stbl := trak.Mdia.Minf.Stbl
	stbl.Stsz = &mp4ff.StszBox{SampleUniformSize: 1, SampleNumber: n}
	stbl.Stts = &mp4ff.SttsBox{SampleCount: []uint32{n}, SampleTimeDelta: []uint32{1}}
	stbl.Stsc = &mp4ff.StscBox{Entries: []mp4ff.StscEntry{{FirstChunk: 1, SamplesPerChunk: n}}}
	stbl.Stco = &mp4ff.StcoBox{ChunkOffset: []uint32{8}}
	mdat := &mp4ff.MdatBox{}
	mdat.SetLazyDataSize(uint64(n))
	return &Demuxer{file: &mp4ff.File{Mdat: mdat}}, trak
}

func TestTrackRejectsMoreThanMaxSamples(t *testing.T) {
	defer func(n int) { MaxSamples = n }(MaxSamples)
	MaxSamples = 1000
	d, trak := manySamplesTrack(1001)
	_, err := d.newTrack(trak)
	if err == nil || !strings.Contains(err.Error(), "MaxSamples (1000)") {
		t.Fatalf("1001 samples over a limit of 1000: %v", err)
	}
	d, trak = manySamplesTrack(1000)
	if _, err := d.newTrack(trak); err != nil {
		t.Fatalf("1000 samples at a limit of 1000: %v", err)
	}
}

// TestSampleIndexMemory measures what the index costs per sample: a
// million one-byte samples must stay within 24 bytes each (the size of
// sampleInfo), with the allocator's rounding allowed for.
func TestSampleIndexMemory(t *testing.T) {
	const n = 1 << 20
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	d, trak := manySamplesTrack(n)
	tr, err := d.newTrack(trak)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	used := after.HeapAlloc - before.HeapAlloc
	perSample := float64(used) / n
	t.Logf("%d samples: %d bytes of index (%.1f bytes per sample)", n, used, perSample)
	if perSample > 25 {
		t.Errorf("index costs %.1f bytes per sample, want at most 24", perSample)
	}
	if tr.SampleCount() != n || tr.Duration() != n {
		t.Errorf("index has %d samples lasting %d", tr.SampleCount(), tr.Duration())
	}
	runtime.KeepAlive(tr)
}

// TestTrackDerivedFields checks what the slimmer index derives instead of
// storing: durations, sync flags and the presentation end.
func TestTrackDerivedFields(t *testing.T) {
	d, trak := manySamplesTrack(6)
	stbl := trak.Mdia.Minf.Stbl
	stbl.Stts = &mp4ff.SttsBox{SampleCount: []uint32{3, 3}, SampleTimeDelta: []uint32{10, 20}}
	// The sync table is deliberately out of order and includes an index
	// beyond the track, which must be ignored.
	stbl.Stss = &mp4ff.StssBox{SampleNumber: []uint32{4, 1, 9}}
	ctts := &mp4ff.CttsBox{}
	if err := ctts.AddSampleCountsAndOffset([]uint32{1, 5}, []int32{0, 5}); err != nil {
		t.Fatal(err)
	}
	stbl.Ctts = ctts
	tr, err := d.newTrack(trak)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []uint32{10, 10, 10, 20, 20, 20} {
		if got := tr.dur(i); got != want {
			t.Errorf("sample %d lasts %d, want %d", i, got, want)
		}
	}
	if tr.Duration() != 90 {
		t.Errorf("track lasts %d, want 90", tr.Duration())
	}
	// Last sample: dts 70, cto 5, dur 20.
	if tr.presEnd != 95 {
		t.Errorf("presentation ends at %d, want 95", tr.presEnd)
	}
	for i, want := range []bool{true, false, false, true, false, false} {
		if _, _, got := tr.Info(i); got != want {
			t.Errorf("sample %d sync = %v, want %v", i, got, want)
		}
	}
	v := &VideoTrack{Track: *tr}
	if kf := v.Keyframes(); len(kf) != 2 || kf[0] != 0 || kf[1] != 3 {
		t.Errorf("Keyframes() = %v, want [0 3]", kf)
	}
	// Sync samples present at 5 and 35 (of 90000 per second); the search
	// returns the first one before it and the last one after it.
	for _, c := range []struct {
		at   int64
		want int
	}{{0, 0}, {5, 0}, {34, 0}, {35, 3}, {1000, 3}} {
		if got := v.KeyframeAtOrBefore(v.TimeOf(c.at)); got != c.want {
			t.Errorf("KeyframeAtOrBefore(%d) = %d, want %d", c.at, got, c.want)
		}
	}
	if got := (&VideoTrack{Track: Track{TimeScale: 90000}}).KeyframeAtOrBefore(0); got != -1 {
		t.Errorf("KeyframeAtOrBefore on an empty track = %d, want -1", got)
	}
}

func TestInfoOutOfRangeGivesZeroValues(t *testing.T) {
	d, trak := manySamplesTrack(2)
	tr, err := d.newTrack(trak)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{-1, 2, math.MaxInt} {
		if dts, pts, sync := tr.Info(i); dts != 0 || pts != 0 || sync {
			t.Errorf("Info(%d) = %d, %d, %v; want zero values", i, dts, pts, sync)
		}
		if _, err := tr.Sample(i); err == nil {
			t.Errorf("Sample(%d) succeeded", i)
		}
	}
}

func TestStartCodesInPlace(t *testing.T) {
	sample := []byte{0, 0, 0, 2, 0x65, 0xAA, 0, 0, 0, 1, 0x41, 0xFF, 0xFF}
	n, err := startCodesInPlace(sample)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 0, 0, 1, 0x65, 0xAA, 0, 0, 0, 1, 0x41}
	if n != len(want) || !bytes.Equal(sample[:n], want) {
		t.Errorf("rewrote %d bytes to % x, want % x", n, sample[:n], want)
	}
	for _, bad := range [][]byte{
		{0, 0, 0, 0, 0x65},       // zero length
		{0, 0, 0, 2, 0x65},       // runs past the sample
		{0xFF, 0xFF, 0xFF, 0xFF}, // would wrap on a 32-bit int
	} {
		if _, err := startCodesInPlace(bad); err == nil {
			t.Errorf("% x accepted", bad)
		}
	}
}

// TestSampleTableRunLengthCtts checks that composition offsets are stored
// as runs: a 3-frame GOP pattern over 300 samples needs a handful of
// entries, not one per sample, and decodes back to the same offsets.
func TestSampleTableRunLengthCtts(t *testing.T) {
	tr := &trackWriter{m: &Muxer{}, id: 1, timeScale: 90000}
	var want []int32
	for i := range 300 {
		cto := int64(0)
		if i >= 2 {
			cto = int64([]int32{6000, 0, 3000}[i%3])
		}
		tr.samples = append(tr.samples, outSample{dts: int64(i) * 3000, pts: int64(i)*3000 + cto, dur: 3000, sync: i%30 == 0})
		want = append(want, int32(cto))
	}
	stbl, _, _, _, _, err := tr.sampleTable()
	if err != nil {
		t.Fatal(err)
	}
	if stbl.Ctts == nil {
		t.Fatal("no ctts box")
	}
	// The two leading zeros are one run; after them every offset differs
	// from its neighbour, so this pattern checks the round trip (and the
	// leading run), while the flat track below checks that a constant
	// offset collapses into one entry.
	if n := len(stbl.Ctts.SampleOffset); n != 1+298 {
		t.Errorf("ctts has %d entries, want 299", n)
	}
	got, err := stbl.Ctts.CompositionTimeOffsets(300)
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d has composition offset %d, want %d", i, got[i], want[i])
		}
	}
	flat := &trackWriter{m: &Muxer{}, id: 1, timeScale: 90000}
	for i := range 300 {
		flat.samples = append(flat.samples, outSample{dts: int64(i) * 3000, pts: int64(i)*3000 + 3000, dur: 3000, sync: true})
	}
	stbl, _, _, _, _, err = flat.sampleTable()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(stbl.Ctts.SampleOffset); n != 1 {
		t.Errorf("a constant composition offset takes %d ctts entries, want 1", n)
	}
}

// editList builds an edts box the way mp4ff's decoder does: the elst in
// both Children (what is encoded) and Elst (what is read).
func editList(entries ...mp4ff.ElstEntry) *mp4ff.EdtsBox {
	edts := &mp4ff.EdtsBox{}
	elst := &mp4ff.ElstBox{Entries: entries}
	edts.Elst = append(edts.Elst, elst)
	edts.AddChild(elst)
	return edts
}

func TestRescaleEdits(t *testing.T) {
	src := editList(
		mp4ff.ElstEntry{SegmentDuration: 300, MediaTime: -1, MediaRateInteger: 1},
		mp4ff.ElstEntry{SegmentDuration: 601, MediaTime: 1024, MediaRateInteger: 1},
	)
	out, total := rescaleEdits(src, 600)
	if len(out.Elst) != 1 || len(out.Children) != 1 || len(out.Elst[0].Entries) != 2 {
		t.Fatalf("copy has %d lists and %d children", len(out.Elst), len(out.Children))
	}
	e := out.Elst[0].Entries
	// 300/600 s = 500/1000 s; 601/600 s rounds up to 1002/1000 s.
	if e[0].SegmentDuration != 500 || e[1].SegmentDuration != 1002 || total != 1502 {
		t.Errorf("segments %d and %d (total %d), want 500 and 1002 (1502)", e[0].SegmentDuration, e[1].SegmentDuration, total)
	}
	if e[0].MediaTime != -1 || e[1].MediaTime != 1024 {
		t.Errorf("media times %d and %d changed", e[0].MediaTime, e[1].MediaTime)
	}
	if src.Elst[0].Entries[1].SegmentDuration != 601 {
		t.Error("the source edit list was modified")
	}
	// Same time scale and an unknown one: copied as they are.
	for _, ts := range []uint32{movieTimeScale, 0} {
		if out, _ := rescaleEdits(src, ts); out.Elst[0].Entries[1].SegmentDuration != 601 {
			t.Errorf("time scale %d: segment duration %d, want 601", ts, out.Elst[0].Entries[1].SegmentDuration)
		}
	}
	// A 32-bit box whose duration outgrows the field is promoted.
	wide := editList(mp4ff.ElstEntry{SegmentDuration: math.MaxUint32, MediaRateInteger: 1})
	if out, _ := rescaleEdits(wide, 600); out.Elst[0].Version != 1 {
		t.Error("a segment duration beyond 32 bits left in a version 0 box")
	}
	if got := rescaleCeil(math.MaxUint64, 600, 1000); got != math.MaxUint64 {
		t.Errorf("rescaleCeil(MaxUint64) = %d, want saturation", got)
	}
}
