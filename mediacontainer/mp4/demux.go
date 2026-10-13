// Package mp4 reads and writes MP4 files around hwmediacodec, which
// itself only handles elementary streams. It is deliberately small:
// progressive (non-fragmented) MP4 and MOV files with one H.264, HEVC or AV1
// video track, one AAC or Opus audio track and any number of other tracks
// that are copied through untouched, and a progressive writer whose video
// and audio samples come straight from hwmediacodec packets. The ISOBMFF
// plumbing is done by
// github.com/Eyevinn/mp4ff; this package only maps between its sample tables
// and the codec library's packets and frames.
package mp4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/Eyevinn/mp4ff/hevc"
	mp4ff "github.com/Eyevinn/mp4ff/mp4"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// MaxSamples bounds the number of samples NewDemuxer indexes per track.
// The index costs 24 bytes per sample (plus the composition offsets and
// the sync sample table the file carries), and the sample count is a
// 32-bit field a hostile file fills freely: with a uniform sample size of
// 1 the count may be the whole mdat length, so without a limit a 100 MB
// file would cost 4 GB of memory. The default of 8 million samples is
// about 37 hours of 60 fps video or 47 hours of AAC at 48 kHz. A track
// with more samples fails NewDemuxer with an error naming this limit;
// raise it before opening a longer file.
var MaxSamples = 8_000_000

// Sample is one sample of a track: a video access unit (already converted
// to Annex-B for H.264 and HEVC, one temporal unit for AV1) or an untouched
// audio/subtitle sample. Times are in the track's time scale.
type Sample struct {
	Data     []byte
	DTS      int64
	PTS      int64
	Duration uint32
	Keyframe bool
}

// sampleInfo is the per-sample part of the index: 24 bytes. The duration
// is the gap to the next sample's decode time (the last sample's is kept
// on the Track) and the sync flag lives in the Track's sync sample index,
// so that a long file costs as little as possible per sample.
type sampleInfo struct {
	// offset counts from the first byte of the file, like the chunk
	// offsets in the file do; Demuxer.start maps it to the reader.
	offset uint64
	dts    int64
	size   uint32
	cto    int32
}

// Track is a track of a demuxed file. Audio and other non-video tracks are
// exposed as plain Tracks so that a transcoder can copy them to its output.
type Track struct {
	ID        uint32
	Handler   string // "vide", "soun", "subt", ...
	TimeScale uint32
	// MediaTimeOffset is the media time that lies at the start of the
	// presentation according to the track's edit list (0 without one):
	// sample PTS minus this offset is the time a player shows the sample
	// at. Encoders with B-frames give the first picture a PTS above its
	// DTS, and muxers hide that delay with an edit list; an audio track
	// starts behind its encoder's delay. A track that starts later than
	// the presentation (an empty edit in front) has the wait taken off, so
	// the offset can be negative.
	MediaTimeOffset int64
	// Trak is the underlying box tree, exposed for passthrough copying.
	Trak *mp4ff.TrakBox

	d       *Demuxer
	samples []sampleInfo
	// lastDur is the duration of the last sample; every other sample's
	// duration is the decode-time gap to its successor.
	lastDur uint32
	// syncs lists the sync samples (0-based, ascending) when the track has
	// an stss box; allSync says that it has none, so every sample is one.
	syncs   []uint32
	allSync bool
	// presEnd is the end of the last presented sample in media time, from
	// which PresentationLength is answered without walking the samples.
	presEnd int64
	// movieTimeScale is the mvhd time scale of the source file (0 when
	// unknown), the unit of the edit list's segment durations.
	movieTimeScale uint32
	// editStart is the media time of the first edit that plays media,
	// without the wait of an empty edit in front of it.
	editStart int64
}

// VideoTrack is the decodable video track of a file.
type VideoTrack struct {
	Track
	Codec  hwmediacodec.Codec
	Width  int
	Height int

	nalLength int
	// paramSets are the parameter-set NAL units (without start codes) from
	// avcC/hvcC. They are prepended to every keyframe so that a decoder can
	// start at any sync sample.
	paramSets [][]byte
}

// Demuxer is an open MP4 file. Its tracks may be read from different
// goroutines (a player decodes pictures and sound side by side); the
// reads of the file itself take turns.
type Demuxer struct {
	rs     io.ReadSeeker
	closer io.Closer
	file   *mp4ff.File
	// start is where the file begins in rs. mp4ff numbers box positions
	// from there and the chunk offsets in the file count from there too,
	// so every offset this package keeps is added to start before a seek.
	start  int64
	video  *VideoTrack
	audio  *AudioTrack
	others []*Track
	mu     sync.Mutex // sample reads share the seek position of rs
}

// Open opens the file at path.
func Open(path string) (*Demuxer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	d, err := NewDemuxer(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	d.closer = f
	return d, nil
}

// NewDemuxer parses the movie header of rs. Sample data is read on demand,
// so rs must stay open while the Demuxer is used. The file is taken to
// start at the current position of rs, which need not be 0.
func NewDemuxer(rs io.ReadSeeker) (*Demuxer, error) {
	start, err := rs.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, fmt.Errorf("mp4: reader position: %w", err)
	}
	end, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("mp4: reader size: %w", err)
	}
	if _, err := rs.Seek(start, io.SeekStart); err != nil {
		return nil, fmt.Errorf("mp4: restore reader position: %w", err)
	}
	if start < 0 || end < start {
		return nil, errors.New("mp4: reader position lies beyond its end")
	}
	// Everything below is in the file's own frame, where the first byte
	// of the file is 0; only the reader's end has to be moved into it.
	fileSize := uint64(end - start)
	file, err := mp4ff.DecodeFile(rs, mp4ff.WithDecodeMode(mp4ff.DecModeLazyMdat))
	if err != nil {
		return nil, fmt.Errorf("mp4: parse: %w", err)
	}
	if file.IsFragmented() {
		return nil, errors.New("mp4: fragmented MP4 files are not supported by this demuxer")
	}
	if file.Moov == nil || file.Mdat == nil {
		return nil, errors.New("mp4: file has no moov or mdat box")
	}
	mdatStart := file.Mdat.PayloadAbsoluteOffset()
	mdatSize := file.Mdat.GetLazyDataSize()
	if !file.Mdat.IsLazy() {
		mdatSize = file.Mdat.DataLength()
	}
	if mdatStart > fileSize || mdatSize > fileSize-mdatStart {
		return nil, errors.New("mp4: mdat extends beyond the file")
	}
	d := &Demuxer{rs: rs, file: file, start: start}
	for _, trak := range file.Moov.Traks {
		t, err := d.newTrack(trak)
		if err != nil {
			return nil, err
		}
		if t.Handler == "vide" && d.video == nil {
			v, err := d.newVideoTrack(t)
			if err != nil {
				return nil, err
			}
			if v != nil {
				d.video = v
				continue
			}
		}
		if t.Handler == "soun" && d.audio == nil {
			d.audio = d.newAudioTrack(t)
		}
		d.others = append(d.others, t)
	}
	return d, nil
}

// Close closes the file when the Demuxer was created with Open.
func (d *Demuxer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closer != nil {
		err := d.closer.Close()
		d.closer = nil
		return err
	}
	return nil
}

// Video returns the decodable video track, or nil when the file has none
// that hwmediacodec understands.
func (d *Demuxer) Video() *VideoTrack { return d.video }

// Audio returns the first audio track hwmediacodec can decode (AAC or
// Opus), or nil when the file has none. The track is also one of Others,
// for copying it through instead of decoding it.
func (d *Demuxer) Audio() *AudioTrack { return d.audio }

// Others returns every track that is not the video track, in file order.
func (d *Demuxer) Others() []*Track { return d.others }

// MovieTimeScale is the time scale of the movie header: the unit of the
// movie duration and of the edit lists' segment durations. ffmpeg writes
// 1000, QuickTime and iPhone recordings 600. It is 0 when the file has no
// movie header.
func (d *Demuxer) MovieTimeScale() uint32 {
	if d.file == nil || d.file.Moov == nil || d.file.Moov.Mvhd == nil {
		return 0
	}
	return d.file.Moov.Mvhd.Timescale
}

// Duration is the movie duration from the movie header.
func (d *Demuxer) Duration() time.Duration {
	mvhd := d.file.Moov.Mvhd
	if mvhd == nil || mvhd.Timescale == 0 {
		return 0
	}
	seconds, remainder := mvhd.Duration/uint64(mvhd.Timescale), mvhd.Duration%uint64(mvhd.Timescale)
	if seconds > math.MaxInt64/uint64(time.Second) {
		return time.Duration(math.MaxInt64)
	}
	ns := seconds*uint64(time.Second) + remainder*uint64(time.Second)/uint64(mvhd.Timescale)
	return time.Duration(min(ns, uint64(math.MaxInt64)))
}

// readAt fills buf with the sample data at off, an offset in the file's
// own frame. The mdat is normally lazy (its bytes stay in the reader) and
// is read through rs; the in-memory form is what a hand-built file and
// the tests use.
func (d *Demuxer) readAt(off uint64, buf []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.file.Mdat.IsLazy() {
		data, err := d.file.Mdat.ReadData(int64(off), int64(len(buf)), nil)
		if err != nil {
			return err
		}
		copy(buf, data)
		return nil
	}
	if _, err := d.rs.Seek(d.start+int64(off), io.SeekStart); err != nil {
		return err
	}
	_, err := io.ReadFull(d.rs, buf)
	return err
}

func (d *Demuxer) newTrack(trak *mp4ff.TrakBox) (*Track, error) {
	if trak == nil || trak.Tkhd == nil || trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil || trak.Mdia.Mdhd == nil || trak.Mdia.Hdlr == nil {
		return nil, errors.New("mp4: track is missing mandatory boxes")
	}
	t := &Track{
		ID:             trak.Tkhd.TrackID,
		Handler:        trak.Mdia.Hdlr.HandlerType,
		TimeScale:      trak.Mdia.Mdhd.Timescale,
		Trak:           trak,
		d:              d,
		movieTimeScale: d.MovieTimeScale(),
	}
	if t.TimeScale == 0 || t.TimeScale > math.MaxInt32 {
		return nil, fmt.Errorf("mp4: track %d has an unsupported time scale %d", t.ID, t.TimeScale)
	}
	if trak.Edts != nil {
		for _, elst := range trak.Edts.Elst {
			// Empty edits in front make the track start later; the first
			// edit with media says where in the media it starts.
			var wait uint64
			for _, e := range elst.Entries {
				if e.MediaTime >= 0 {
					t.editStart = e.MediaTime
					break
				}
				wait += e.SegmentDuration
			}
			t.MediaTimeOffset = t.editStart
			if wait > 0 && wait <= math.MaxInt64 && t.movieTimeScale > 0 {
				w := codec.RescaleTrunc(int64(wait), int64(t.movieTimeScale), int64(t.TimeScale))
				if t.MediaTimeOffset >= math.MinInt64+w {
					t.MediaTimeOffset -= w
				}
			}
			break
		}
	}
	stbl := trak.Mdia.Minf.Stbl
	if stbl.Stsz == nil || stbl.Stts == nil || stbl.Stsc == nil || (stbl.Stco == nil && stbl.Co64 == nil) {
		return nil, fmt.Errorf("mp4: track %d has an incomplete sample table", t.ID)
	}
	n := stbl.Stsz.GetNrSamples()
	if len(stbl.Stts.SampleCount) != len(stbl.Stts.SampleTimeDelta) {
		return nil, fmt.Errorf("mp4: track %d has an invalid decoding time table", t.ID)
	}
	var timedSamples, timedDuration uint64
	for i, count := range stbl.Stts.SampleCount {
		timedSamples += uint64(count)
		duration := uint64(count) * uint64(stbl.Stts.SampleTimeDelta[i])
		if duration > math.MaxInt64-timedDuration {
			return nil, fmt.Errorf("mp4: track %d decoding duration overflows", t.ID)
		}
		timedDuration += duration
	}
	if timedSamples != uint64(n) {
		return nil, fmt.Errorf("mp4: track %d: time table covers %d of %d samples", t.ID, timedSamples, n)
	}
	// Even a uniform-size stsz stores n in four bytes. Verify that its
	// samples fit in the actual media data, and that the index they need
	// is one this process is willing to hold, before allocating it.
	mdatSize := d.file.Mdat.GetLazyDataSize()
	if !d.file.Mdat.IsLazy() {
		mdatSize = d.file.Mdat.DataLength()
	}
	if MaxSamples < 0 || uint64(n) > uint64(MaxSamples) {
		return nil, fmt.Errorf("mp4: track %d has %d samples, more than MaxSamples (%d)", t.ID, n, MaxSamples)
	}
	if stbl.Stsz.SampleUniformSize == 0 && uint64(len(stbl.Stsz.SampleSize)) != uint64(n) {
		return nil, fmt.Errorf("mp4: track %d has an invalid sample size table", t.ID)
	}
	if stbl.Stsz.SampleUniformSize > 0 && uint64(n)*uint64(stbl.Stsz.SampleUniformSize) > mdatSize {
		return nil, fmt.Errorf("mp4: track %d sample sizes exceed the media data", t.ID)
	}
	t.samples = make([]sampleInfo, n)
	for i := uint32(0); i < n; i++ {
		t.samples[i].size = stbl.Stsz.GetSampleSize(int(i + 1))
	}

	// Decode times from stts. Durations are the gaps between them; only
	// the last one has to be remembered.
	var dts int64
	idx := 0
	for i := range stbl.Stts.SampleCount {
		for c := uint32(0); c < stbl.Stts.SampleCount[i] && idx < int(n); c++ {
			t.samples[idx].dts = dts
			dts += int64(stbl.Stts.SampleTimeDelta[i])
			idx++
		}
		if stbl.Stts.SampleCount[i] > 0 {
			t.lastDur = stbl.Stts.SampleTimeDelta[i]
		}
	}
	if stbl.Ctts != nil {
		ctos, err := stbl.Ctts.CompositionTimeOffsets(n)
		if err != nil {
			return nil, fmt.Errorf("mp4: track %d ctts: %w", t.ID, err)
		}
		for i := range ctos {
			t.samples[i].cto = ctos[i]
		}
	}
	for i := range t.samples {
		if e := t.samples[i].dts + int64(t.samples[i].cto) + int64(t.dur(i)); e > t.presEnd {
			t.presEnd = e
		}
	}
	// Sync samples: an ascending index for binary search. The file's list
	// should already be ascending, but a wrong one must not break seeking.
	if stbl.Stss == nil {
		t.allSync = true
	} else {
		t.syncs = make([]uint32, 0, len(stbl.Stss.SampleNumber))
		for _, nr := range stbl.Stss.SampleNumber {
			if nr >= 1 && nr <= n {
				t.syncs = append(t.syncs, nr-1)
			}
		}
		slices.Sort(t.syncs)
		t.syncs = slices.Compact(t.syncs)
	}
	// File offsets from stsc + stco/co64.
	var chunkOffsets []uint64
	if stbl.Stco != nil {
		for _, o := range stbl.Stco.ChunkOffset {
			chunkOffsets = append(chunkOffsets, uint64(o))
		}
	} else {
		chunkOffsets = stbl.Co64.ChunkOffset
	}
	sample := 0
	entries := stbl.Stsc.Entries
	for ei, e := range entries {
		if e.FirstChunk == 0 || e.SamplesPerChunk == 0 || (ei == 0 && e.FirstChunk != 1) ||
			(ei > 0 && e.FirstChunk <= entries[ei-1].FirstChunk) || uint64(e.FirstChunk) > uint64(len(chunkOffsets)) {
			return nil, fmt.Errorf("mp4: track %d has an invalid chunk table entry %d", t.ID, ei+1)
		}
	}
	// The chunk offsets and mp4ff's mdat position both count from the
	// first byte of the file, so they compare directly.
	mdatStart := d.file.Mdat.PayloadAbsoluteOffset()
	for ei, e := range entries {
		last := uint32(len(chunkOffsets))
		if ei+1 < len(entries) {
			last = entries[ei+1].FirstChunk - 1
		}
		for chunk := e.FirstChunk; chunk <= last; chunk++ {
			if int(chunk) > len(chunkOffsets) {
				break
			}
			off := chunkOffsets[chunk-1]
			for s := uint32(0); s < e.SamplesPerChunk && sample < int(n); s++ {
				if off < mdatStart || off-mdatStart > mdatSize || uint64(t.samples[sample].size) > mdatSize-(off-mdatStart) {
					return nil, fmt.Errorf("mp4: track %d sample %d is outside the media data", t.ID, sample)
				}
				t.samples[sample].offset = off
				off += uint64(t.samples[sample].size)
				sample++
			}
		}
	}
	if sample != int(n) {
		return nil, fmt.Errorf("mp4: track %d: chunk table covers %d of %d samples", t.ID, sample, n)
	}
	return t, nil
}

func (d *Demuxer) newVideoTrack(t *Track) (*VideoTrack, error) {
	stsd := t.Trak.Mdia.Minf.Stbl.Stsd
	if stsd == nil || len(stsd.Children) == 0 {
		return nil, nil
	}
	entry, ok := stsd.Children[0].(*mp4ff.VisualSampleEntryBox)
	if !ok {
		return nil, nil
	}
	v := &VideoTrack{Track: *t, Width: int(entry.Width), Height: int(entry.Height)}
	switch entry.Type() {
	case "avc1", "avc3":
		if entry.AvcC == nil {
			return nil, errors.New("mp4: avc1 sample entry without avcC")
		}
		v.Codec = hwmediacodec.H264
		v.nalLength = 4 // avcC lengthSizeMinusOne is almost always 3; mp4ff does not expose it
		v.paramSets = append(v.paramSets, entry.AvcC.SPSnalus...)
		v.paramSets = append(v.paramSets, entry.AvcC.PPSnalus...)
	case "hvc1", "hev1":
		if entry.HvcC == nil {
			return nil, errors.New("mp4: hvc1 sample entry without hvcC")
		}
		v.Codec = hwmediacodec.HEVC
		v.nalLength = int(entry.HvcC.LengthSizeMinusOne) + 1
		for _, nt := range []hevc.NaluType{hevc.NALU_VPS, hevc.NALU_SPS, hevc.NALU_PPS} {
			v.paramSets = append(v.paramSets, entry.HvcC.GetNalusForType(nt)...)
		}
	case "av01":
		v.Codec = hwmediacodec.AV1
	default:
		return nil, nil
	}
	return v, nil
}

// dur is the duration of sample i: the decode-time gap to the next sample,
// or the last stts delta for the last sample.
func (t *Track) dur(i int) uint32 {
	if i+1 < len(t.samples) {
		return uint32(t.samples[i+1].dts - t.samples[i].dts)
	}
	return t.lastDur
}

// isSync reports whether sample i is a sync sample.
func (t *Track) isSync(i int) bool {
	if t.allSync {
		return true
	}
	_, found := slices.BinarySearch(t.syncs, uint32(i))
	return found
}

// syncCount is the number of sync samples and syncAt the index of the k-th
// of them, both over the sync sample index or, without an stss box, over
// every sample.
func (t *Track) syncCount() int {
	if t.allSync {
		return len(t.samples)
	}
	return len(t.syncs)
}

func (t *Track) syncAt(k int) int {
	if t.allSync {
		return k
	}
	return int(t.syncs[k])
}

// SampleCount is the number of samples in the track.
func (t *Track) SampleCount() int { return len(t.samples) }

// Duration is the track duration in its time scale.
func (t *Track) Duration() int64 {
	if len(t.samples) == 0 {
		return 0
	}
	return t.samples[len(t.samples)-1].dts + int64(t.lastDur)
}

// PresentationLength is the length of the track as a player sees it: the
// end of the last presented sample minus the edit-list offset.
func (t *Track) PresentationLength() time.Duration {
	if d := t.PresentationTime(t.presEnd); d > 0 {
		return d
	}
	return 0
}

// TimeOf converts a track time to a duration.
func (t *Track) TimeOf(ts int64) time.Duration {
	return time.Duration(codec.RescaleTrunc(ts, int64(t.TimeScale), int64(time.Second)))
}

// PresentationTime is the moment a player shows a sample with the given
// PTS: TimeOf(pts) minus the edit-list offset.
func (t *Track) PresentationTime(pts int64) time.Duration {
	return t.TimeOf(pts - t.MediaTimeOffset)
}

// Sample reads sample i (0-based) as stored in the file.
func (t *Track) Sample(i int) (Sample, error) {
	if i < 0 || i >= len(t.samples) {
		return Sample{}, fmt.Errorf("mp4: sample %d out of range [0, %d)", i, len(t.samples))
	}
	s := t.samples[i]
	data := make([]byte, s.size)
	if err := t.d.readAt(s.offset, data); err != nil {
		return Sample{}, fmt.Errorf("mp4: read sample %d: %w", i, err)
	}
	return Sample{Data: data, DTS: s.dts, PTS: s.dts + int64(s.cto), Duration: t.dur(i), Keyframe: t.isSync(i)}, nil
}

// Info returns the timing of sample i without reading its data. An index
// outside [0, SampleCount()) gives zero values, where Sample returns an
// error: Info is the cheap call in the inner loop of a copy, and a wrong
// index there is the caller's own bug, which the Sample call that follows
// reports.
func (t *Track) Info(i int) (dts, pts int64, keyframe bool) {
	if i < 0 || i >= len(t.samples) {
		return 0, 0, false
	}
	s := t.samples[i]
	return s.dts, s.dts + int64(s.cto), t.isSync(i)
}

// FrameRate is the average frame rate of the track.
func (v *VideoTrack) FrameRate() float64 {
	d := v.Duration()
	if d == 0 {
		return 0
	}
	return float64(len(v.samples)) * float64(v.TimeScale) / float64(d)
}

// Keyframes lists the indexes of the sync samples.
func (v *VideoTrack) Keyframes() []int {
	out := make([]int, v.syncCount())
	for k := range out {
		out[k] = v.syncAt(k)
	}
	return out
}

// KeyframeAtOrBefore returns the index of the last sync sample whose
// presentation time is not after t, or the first sync sample when t lies
// before it (-1 without any). Sync samples are presented in decode order,
// so the search is a binary one over the sync sample index.
func (v *VideoTrack) KeyframeAtOrBefore(t time.Duration) int {
	n := v.syncCount()
	if n == 0 {
		return -1
	}
	k := sort.Search(n, func(k int) bool {
		s := v.samples[v.syncAt(k)]
		return v.PresentationTime(s.dts+int64(s.cto)) > t
	})
	return v.syncAt(max(k-1, 0))
}

// AccessUnit reads sample i and returns it in the form hwmediacodec expects:
// an Annex-B access unit with the parameter sets in front of keyframes for
// H.264 and HEVC, or the temporal unit as stored for AV1.
//
// The sample is read straight into the buffer that is returned and, with
// the usual 4-byte NAL unit lengths, the length prefixes are overwritten
// with start codes of the same size, so a call costs one allocation.
func (v *VideoTrack) AccessUnit(i int) (Sample, error) {
	if v.Codec == hwmediacodec.AV1 {
		return v.Sample(i)
	}
	if i < 0 || i >= len(v.samples) {
		return Sample{}, fmt.Errorf("mp4: sample %d out of range [0, %d)", i, len(v.samples))
	}
	s := v.samples[i]
	sync := v.isSync(i)
	head := 0
	if sync {
		for _, ps := range v.paramSets {
			head += 4 + len(ps)
		}
	}
	buf := make([]byte, head+int(s.size))
	if sync {
		pos := 0
		for _, ps := range v.paramSets {
			pos += copy(buf[pos:], []byte{0, 0, 0, 1})
			pos += copy(buf[pos:], ps)
		}
	}
	if err := v.d.readAt(s.offset, buf[head:]); err != nil {
		return Sample{}, fmt.Errorf("mp4: read sample %d: %w", i, err)
	}
	out := buf
	if v.nalLength == 4 {
		n, err := startCodesInPlace(buf[head:])
		if err != nil {
			return Sample{}, fmt.Errorf("mp4: sample %d: %w", i, err)
		}
		out = buf[:head+n]
	} else {
		// Shorter length fields grow when they become start codes, so this
		// (rare: hvcC with lengthSizeMinusOne below 3) form needs a second
		// buffer. It must not share buf: AppendUnit would write over the
		// sample bytes it is still reading.
		var err error
		if out, err = annexb.FromLengthPrefixed(slices.Clone(buf[:head]), buf[head:], v.nalLength); err != nil {
			return Sample{}, fmt.Errorf("mp4: sample %d: %w", i, err)
		}
	}
	return Sample{Data: out, DTS: s.dts, PTS: s.dts + int64(s.cto), Duration: v.dur(i), Keyframe: sync}, nil
}

// startCodesInPlace rewrites the 4-byte NAL unit length prefixes of an MP4
// sample into Annex-B start codes and returns how many bytes of the sample
// the NAL units cover (trailing bytes too short for a length field are
// left out, as annexb.FromLengthPrefixed leaves them out). A length of 0
// or one that runs past the sample is an error, so a corrupt sample
// cannot make a decoder read start codes out of picture data.
func startCodesInPlace(sample []byte) (int, error) {
	pos := 0
	for len(sample)-pos >= 4 {
		n := uint64(binary.BigEndian.Uint32(sample[pos:]))
		if n == 0 || n > uint64(len(sample)-pos-4) {
			return 0, fmt.Errorf("NAL unit length %d out of range", n)
		}
		sample[pos], sample[pos+1], sample[pos+2], sample[pos+3] = 0, 0, 0, 1
		pos += 4 + int(n)
	}
	return pos, nil
}

// Packet returns AccessUnit(i) as a decoder packet.
func (v *VideoTrack) Packet(i int) (hwmediacodec.Packet, error) {
	s, err := v.AccessUnit(i)
	if err != nil {
		return hwmediacodec.Packet{}, err
	}
	return hwmediacodec.Packet{Data: s.Data, PTS: s.PTS, DTS: s.DTS, Keyframe: s.Keyframe}, nil
}
