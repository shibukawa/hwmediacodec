// Package container reads and writes MP4 files around hwmediacodec, which
// itself only handles Annex-B elementary streams. It is deliberately small:
// progressive (non-fragmented) MP4 and MOV files with one H.264, HEVC or AV1
// video track plus any number of other tracks that are copied through
// untouched, and a progressive writer whose video samples come straight from
// hwmediacodec packets. The ISOBMFF plumbing is done by
// github.com/Eyevinn/mp4ff; this package only maps between its sample tables
// and the codec library's packets and frames.
package container

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/shibukawa/hwmediacodec"
)

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

type sampleInfo struct {
	offset uint64
	size   uint32
	dts    int64
	cto    int32
	dur    uint32
	sync   bool
}

// Track is a track of a demuxed file. Audio and other non-video tracks are
// exposed as plain Tracks so that a transcoder can copy them to its output.
type Track struct {
	ID        uint32
	Handler   string // "vide", "soun", "subt", ...
	TimeScale uint32
	// MediaTimeOffset is the media time at which presentation starts
	// according to the track's edit list (0 without one). Encoders with
	// B-frames give the first picture a PTS above its DTS, and muxers hide
	// that delay with an edit list, so sample PTS minus this offset is the
	// time a player shows the sample at.
	MediaTimeOffset int64
	// Trak is the underlying box tree, exposed for passthrough copying.
	Trak *mp4.TrakBox

	d       *Demuxer
	samples []sampleInfo
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

// Demuxer is an open MP4 file.
type Demuxer struct {
	rs     io.ReadSeeker
	closer io.Closer
	file   *mp4.File
	video  *VideoTrack
	others []*Track
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
// so rs must stay open while the Demuxer is used.
func NewDemuxer(rs io.ReadSeeker) (*Demuxer, error) {
	file, err := mp4.DecodeFile(rs, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return nil, fmt.Errorf("container: parse: %w", err)
	}
	if file.IsFragmented() {
		return nil, errors.New("container: fragmented MP4 files are not supported by this demuxer")
	}
	if file.Moov == nil || file.Mdat == nil {
		return nil, errors.New("container: file has no moov or mdat box")
	}
	d := &Demuxer{rs: rs, file: file}
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
		d.others = append(d.others, t)
	}
	return d, nil
}

// Close closes the file when the Demuxer was created with Open.
func (d *Demuxer) Close() error {
	if d.closer != nil {
		return d.closer.Close()
	}
	return nil
}

// Video returns the decodable video track, or nil when the file has none
// that hwmediacodec understands.
func (d *Demuxer) Video() *VideoTrack { return d.video }

// Others returns every track that is not the video track, in file order.
func (d *Demuxer) Others() []*Track { return d.others }

// Duration is the movie duration from the movie header.
func (d *Demuxer) Duration() time.Duration {
	mvhd := d.file.Moov.Mvhd
	if mvhd == nil || mvhd.Timescale == 0 {
		return 0
	}
	return time.Duration(mvhd.Duration) * time.Second / time.Duration(mvhd.Timescale)
}

func (d *Demuxer) newTrack(trak *mp4.TrakBox) (*Track, error) {
	if trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil || trak.Mdia.Mdhd == nil || trak.Mdia.Hdlr == nil {
		return nil, errors.New("container: track is missing mandatory boxes")
	}
	t := &Track{
		ID:        trak.Tkhd.TrackID,
		Handler:   trak.Mdia.Hdlr.HandlerType,
		TimeScale: trak.Mdia.Mdhd.Timescale,
		Trak:      trak,
		d:         d,
	}
	if trak.Edts != nil {
		for _, elst := range trak.Edts.Elst {
			for _, e := range elst.Entries {
				if e.MediaTime >= 0 {
					t.MediaTimeOffset = e.MediaTime
					break
				}
			}
			break
		}
	}
	stbl := trak.Mdia.Minf.Stbl
	if stbl.Stsz == nil || stbl.Stts == nil || stbl.Stsc == nil || (stbl.Stco == nil && stbl.Co64 == nil) {
		return nil, fmt.Errorf("container: track %d has an incomplete sample table", t.ID)
	}
	n := stbl.Stsz.GetNrSamples()
	t.samples = make([]sampleInfo, n)
	for i := uint32(0); i < n; i++ {
		t.samples[i].size = stbl.Stsz.GetSampleSize(int(i + 1))
	}

	// Decode times and durations from stts.
	var dts int64
	idx := 0
	for i := range stbl.Stts.SampleCount {
		for c := uint32(0); c < stbl.Stts.SampleCount[i] && idx < int(n); c++ {
			t.samples[idx].dts = dts
			t.samples[idx].dur = stbl.Stts.SampleTimeDelta[i]
			dts += int64(stbl.Stts.SampleTimeDelta[i])
			idx++
		}
	}
	if stbl.Ctts != nil {
		ctos, err := stbl.Ctts.CompositionTimeOffsets(n)
		if err != nil {
			return nil, fmt.Errorf("container: track %d ctts: %w", t.ID, err)
		}
		for i := range ctos {
			t.samples[i].cto = ctos[i]
		}
	}
	// Sync samples.
	if stbl.Stss == nil {
		for i := range t.samples {
			t.samples[i].sync = true
		}
	} else {
		for _, nr := range stbl.Stss.SampleNumber {
			if nr >= 1 && nr <= n {
				t.samples[nr-1].sync = true
			}
		}
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
				t.samples[sample].offset = off
				off += uint64(t.samples[sample].size)
				sample++
			}
		}
	}
	if sample != int(n) {
		return nil, fmt.Errorf("container: track %d: chunk table covers %d of %d samples", t.ID, sample, n)
	}
	return t, nil
}

func (d *Demuxer) newVideoTrack(t *Track) (*VideoTrack, error) {
	stsd := t.Trak.Mdia.Minf.Stbl.Stsd
	if stsd == nil || len(stsd.Children) == 0 {
		return nil, nil
	}
	entry, ok := stsd.Children[0].(*mp4.VisualSampleEntryBox)
	if !ok {
		return nil, nil
	}
	v := &VideoTrack{Track: *t, Width: int(entry.Width), Height: int(entry.Height)}
	switch entry.Type() {
	case "avc1", "avc3":
		if entry.AvcC == nil {
			return nil, errors.New("container: avc1 sample entry without avcC")
		}
		v.Codec = hwmediacodec.H264
		v.nalLength = 4 // avcC lengthSizeMinusOne is almost always 3; mp4ff does not expose it
		v.paramSets = append(v.paramSets, entry.AvcC.SPSnalus...)
		v.paramSets = append(v.paramSets, entry.AvcC.PPSnalus...)
	case "hvc1", "hev1":
		if entry.HvcC == nil {
			return nil, errors.New("container: hvc1 sample entry without hvcC")
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

// SampleCount is the number of samples in the track.
func (t *Track) SampleCount() int { return len(t.samples) }

// Duration is the track duration in its time scale.
func (t *Track) Duration() int64 {
	if len(t.samples) == 0 {
		return 0
	}
	last := t.samples[len(t.samples)-1]
	return last.dts + int64(last.dur)
}

// PresentationLength is the length of the track as a player sees it: the
// end of the last presented sample minus the edit-list offset.
func (t *Track) PresentationLength() time.Duration {
	var end int64
	for _, s := range t.samples {
		if e := s.dts + int64(s.cto) + int64(s.dur); e > end {
			end = e
		}
	}
	if d := t.PresentationTime(end); d > 0 {
		return d
	}
	return 0
}

// TimeOf converts a track time to a duration.
func (t *Track) TimeOf(ts int64) time.Duration {
	return time.Duration(ts) * time.Second / time.Duration(t.TimeScale)
}

// PresentationTime is the moment a player shows a sample with the given
// PTS: TimeOf(pts) minus the edit-list offset.
func (t *Track) PresentationTime(pts int64) time.Duration {
	return t.TimeOf(pts - t.MediaTimeOffset)
}

// Sample reads sample i (0-based) as stored in the file.
func (t *Track) Sample(i int) (Sample, error) {
	if i < 0 || i >= len(t.samples) {
		return Sample{}, fmt.Errorf("container: sample %d out of range [0, %d)", i, len(t.samples))
	}
	s := t.samples[i]
	data, err := t.d.file.Mdat.ReadData(int64(s.offset), int64(s.size), t.d.rs)
	if err != nil {
		return Sample{}, fmt.Errorf("container: read sample %d: %w", i, err)
	}
	return Sample{Data: data, DTS: s.dts, PTS: s.dts + int64(s.cto), Duration: s.dur, Keyframe: s.sync}, nil
}

// Info returns the timing of sample i without reading its data.
func (t *Track) Info(i int) (dts, pts int64, keyframe bool) {
	s := t.samples[i]
	return s.dts, s.dts + int64(s.cto), s.sync
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
	var out []int
	for i, s := range v.samples {
		if s.sync {
			out = append(out, i)
		}
	}
	return out
}

// KeyframeAtOrBefore returns the index of the last sync sample whose
// presentation time is not after t, or the first sync sample when t lies
// before it.
func (v *VideoTrack) KeyframeAtOrBefore(t time.Duration) int {
	ts := int64(t)*int64(v.TimeScale)/int64(time.Second) + v.MediaTimeOffset
	best := -1
	for i, s := range v.samples {
		if !s.sync {
			continue
		}
		if s.dts+int64(s.cto) > ts && best >= 0 {
			break
		}
		best = i
	}
	return best
}

// AccessUnit reads sample i and returns it in the form hwmediacodec expects:
// an Annex-B access unit with the parameter sets in front of keyframes for
// H.264 and HEVC, or the temporal unit as stored for AV1.
func (v *VideoTrack) AccessUnit(i int) (Sample, error) {
	s, err := v.Sample(i)
	if err != nil {
		return s, err
	}
	if v.Codec == hwmediacodec.AV1 {
		return s, nil
	}
	out := make([]byte, 0, len(s.Data)+64)
	if s.Keyframe {
		for _, ps := range v.paramSets {
			out = append(out, 0, 0, 0, 1)
			out = append(out, ps...)
		}
	}
	data := s.Data
	for len(data) >= v.nalLength {
		n := 0
		for k := 0; k < v.nalLength; k++ {
			n = n<<8 | int(data[k])
		}
		data = data[v.nalLength:]
		if n <= 0 || n > len(data) {
			return Sample{}, fmt.Errorf("container: sample %d: NAL unit length %d out of range", i, n)
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, data[:n]...)
		data = data[n:]
	}
	s.Data = out
	return s, nil
}

// Packet returns AccessUnit(i) as a decoder packet.
func (v *VideoTrack) Packet(i int) (hwmediacodec.Packet, error) {
	s, err := v.AccessUnit(i)
	if err != nil {
		return hwmediacodec.Packet{}, err
	}
	return hwmediacodec.Packet{Data: s.Data, PTS: s.PTS, DTS: s.DTS, Keyframe: s.Keyframe}, nil
}
