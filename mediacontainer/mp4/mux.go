package mp4

import (
	"errors"
	"fmt"
	"io"
	"os"

	mp4ff "github.com/Eyevinn/mp4ff/mp4"

	"github.com/shibukawa/hwmediacodec"
)

const movieTimeScale = 1000

// Muxer writes a progressive MP4 file: ftyp, then one mdat that grows as
// samples arrive, then moov when Close is called. The output must be
// seekable because the mdat size and the sample tables are only known at
// the end.
type Muxer struct {
	w      io.WriteSeeker
	closer io.Closer
	tracks []*trackWriter
	pos    int64
	mdat   int64 // offset of the mdat box header
	last   *trackWriter
	closed bool
}

type outSample struct {
	size uint32
	dts  int64
	pts  int64
	dur  uint32
	sync bool
}

type trackWriter struct {
	m         *Muxer
	id        uint32
	timeScale uint32
	samples   []outSample
	chunkOff  []uint64
	chunkN    []uint32
	build     func(stbl *mp4ff.StblBox) (*mp4ff.TrakBox, error)
	edts      *mp4ff.EdtsBox // copied from a source track
}

// Create creates the file at path.
func Create(path string) (*Muxer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	m, err := NewMuxer(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	m.closer = f
	return m, nil
}

// NewMuxer starts writing a file to w.
func NewMuxer(w io.WriteSeeker) (*Muxer, error) {
	m := &Muxer{w: w}
	ftyp := mp4ff.NewFtyp("isom", 512, []string{"isom", "iso2", "avc1", "mp41"})
	if err := ftyp.Encode(w); err != nil {
		return nil, err
	}
	m.pos = int64(ftyp.Size())
	m.mdat = m.pos
	// 16-byte mdat header with a 64-bit size, patched by Close.
	hdr := []byte{0, 0, 0, 1, 'm', 'd', 'a', 't', 0, 0, 0, 0, 0, 0, 0, 0}
	if _, err := w.Write(hdr); err != nil {
		return nil, err
	}
	m.pos += int64(len(hdr))
	return m, nil
}

// VideoWriter receives hwmediacodec packets for the video track.
type VideoWriter struct {
	*trackWriter
	ps paramSets
}

// AddVideoTrack adds an H.264 or HEVC track whose samples are written with
// WritePacket. Parameter sets are taken from the packets themselves, so the
// first packet must be a keyframe carrying SPS/PPS (hwmediacodec encoders
// always emit them in-band). The time scale is the unit of Packet.PTS and
// Packet.DTS.
func (m *Muxer) AddVideoTrack(c hwmediacodec.Codec, timeScale uint32) (*VideoWriter, error) {
	if c != hwmediacodec.H264 && c != hwmediacodec.HEVC {
		return nil, fmt.Errorf("mp4: cannot write %s samples", c)
	}
	v := &VideoWriter{ps: paramSets{codec: c}}
	v.trackWriter = m.addTrack(timeScale, v.buildTrak)
	return v, nil
}

// TrackWriter copies samples of a demuxed track into the output.
type TrackWriter struct {
	*trackWriter
	src *Track
}

// AddPassthroughTrack adds a track with the same sample description, time
// scale, handler and edit list as src; feed it with WriteSample.
func (m *Muxer) AddPassthroughTrack(src *Track) *TrackWriter {
	t := &TrackWriter{src: src}
	t.trackWriter = m.addTrack(src.TimeScale, t.buildTrak)
	t.trackWriter.edts = src.Trak.Edts
	return t
}

func (m *Muxer) addTrack(timeScale uint32, build func(*mp4ff.StblBox) (*mp4ff.TrakBox, error)) *trackWriter {
	t := &trackWriter{m: m, id: uint32(len(m.tracks) + 1), timeScale: timeScale, build: build}
	m.tracks = append(m.tracks, t)
	return t
}

func (m *Muxer) write(t *trackWriter, data []byte, s outSample) error {
	if m.closed {
		return errors.New("mp4: muxer is closed")
	}
	if m.last != t {
		t.chunkOff = append(t.chunkOff, uint64(m.pos))
		t.chunkN = append(t.chunkN, 0)
		m.last = t
	}
	n, err := m.w.Write(data)
	m.pos += int64(n)
	if err != nil {
		return err
	}
	s.size = uint32(len(data))
	t.samples = append(t.samples, s)
	t.chunkN[len(t.chunkN)-1]++
	return nil
}

// WritePacket writes one Annex-B access unit from an encoder. Parameter
// sets and access-unit delimiters are removed from the sample and the
// parameter sets go to the sample description instead.
func (v *VideoWriter) WritePacket(p hwmediacodec.Packet) error {
	data, sync, err := v.ps.sample(p)
	if err != nil {
		return err
	}
	if data == nil {
		return nil // parameter sets only
	}
	return v.m.write(v.trackWriter, data, outSample{dts: p.DTS, pts: p.PTS, sync: sync})
}

func (v *VideoWriter) buildTrak(stbl *mp4ff.StblBox) (*mp4ff.TrakBox, error) {
	entry, width, height, err := v.ps.sampleEntry()
	if err != nil {
		return nil, err
	}
	stsd := mp4ff.NewStsdBox()
	stsd.AddChild(entry)
	hdlr, _ := mp4ff.CreateHdlr("video")
	return newTrak(v.trackWriter, hdlr, mp4ff.CreateVmhd(), stsd, stbl, width, height, "und"), nil
}

// WriteSample copies one sample of the source track. Samples must come in
// decode order, which Track.Sample(i) with increasing i gives.
func (t *TrackWriter) WriteSample(s Sample) error {
	return t.m.write(t.trackWriter, s.Data, outSample{dts: s.DTS, pts: s.PTS, dur: s.Duration, sync: s.Keyframe})
}

func (t *TrackWriter) buildTrak(stbl *mp4ff.StblBox) (*mp4ff.TrakBox, error) {
	src := t.src.Trak
	var mediaHeader mp4ff.Box
	switch {
	case src.Mdia.Minf.Vmhd != nil:
		mediaHeader = src.Mdia.Minf.Vmhd
	case src.Mdia.Minf.Smhd != nil:
		mediaHeader = src.Mdia.Minf.Smhd
	case src.Mdia.Minf.Sthd != nil:
		mediaHeader = src.Mdia.Minf.Sthd
	default:
		mediaHeader = &mp4ff.NmhdBox{}
	}
	lang := src.Mdia.Mdhd.GetLanguage()
	width := int(src.Tkhd.Width >> 16)
	height := int(src.Tkhd.Height >> 16)
	trak := newTrak(t.trackWriter, src.Mdia.Hdlr, mediaHeader, src.Mdia.Minf.Stbl.Stsd, stbl, width, height, lang)
	trak.Tkhd.Volume = src.Tkhd.Volume
	trak.Tkhd.AlternateGroup = src.Tkhd.AlternateGroup
	return trak, nil
}

// newTrak assembles trak(tkhd, [edts], mdia(mdhd, hdlr, minf(mediaHeader,
// dinf, stbl))) around a finished sample table.
func newTrak(t *trackWriter, hdlr *mp4ff.HdlrBox, mediaHeader mp4ff.Box, stsd *mp4ff.StsdBox, stbl *mp4ff.StblBox, width, height int, lang string) *mp4ff.TrakBox {
	stbl.Children = append([]mp4ff.Box{stsd}, stbl.Children...)
	stbl.Stsd = stsd

	trak := mp4ff.NewTrakBox()
	tkhd := mp4ff.CreateTkhd()
	tkhd.TrackID = t.id
	tkhd.Width = mp4ff.Fixed32(uint32(width) << 16)
	tkhd.Height = mp4ff.Fixed32(uint32(height) << 16)
	trak.AddChild(tkhd)

	mdia := mp4ff.NewMdiaBox()
	mdhd := &mp4ff.MdhdBox{Timescale: t.timeScale}
	mdhd.SetLanguage(lang)
	mdia.AddChild(mdhd)
	mdia.AddChild(hdlr)
	minf := mp4ff.NewMinfBox()
	minf.AddChild(mediaHeader)
	dinf := &mp4ff.DinfBox{}
	dinf.AddChild(mp4ff.CreateDref())
	minf.AddChild(dinf)
	minf.AddChild(stbl)
	mdia.AddChild(minf)
	trak.AddChild(mdia)
	return trak
}

// finish normalises the timing of a track and builds its sample table.
// Decode times are shifted so that the first sample decodes at 0;
// composition offsets stay PTS-DTS. The presentation start (the smallest
// PTS) is returned so that an edit list can hide the decoder delay.
func (t *trackWriter) finish() (stbl *mp4ff.StblBox, mediaDuration uint64, presStart, presEnd int64, err error) {
	if len(t.samples) == 0 {
		return nil, 0, 0, 0, fmt.Errorf("mp4: track %d has no samples", t.id)
	}
	n := len(t.samples)
	// Some encoders (VideoToolbox with B-frames) number decode times from
	// the first PTS, so a reordered picture can have DTS > PTS. MP4 needs
	// DTS <= PTS; shifting every DTS back by the largest lag keeps the gaps
	// (and so the durations) intact and the edit list below hides the
	// resulting start delay.
	var lag int64
	for _, s := range t.samples {
		if d := s.dts - s.pts; d > lag {
			lag = d
		}
	}
	for i := range t.samples {
		t.samples[i].dts -= lag
	}
	base := t.samples[0].dts
	// Durations: explicit when given (passthrough), otherwise the DTS gap to
	// the next sample; the last sample repeats the previous duration.
	for i := range t.samples {
		s := &t.samples[i]
		if s.dur != 0 {
			continue
		}
		switch {
		case i+1 < n:
			d := t.samples[i+1].dts - s.dts
			if d <= 0 {
				return nil, 0, 0, 0, fmt.Errorf("mp4: track %d: decode time does not increase at sample %d", t.id, i)
			}
			s.dur = uint32(d)
		case i > 0:
			s.dur = t.samples[i-1].dur
		default:
			s.dur = 1
		}
	}
	stbl = mp4ff.NewStblBox()
	stts := &mp4ff.SttsBox{}
	var ctts *mp4ff.CttsBox
	stss := &mp4ff.StssBox{}
	stsz := &mp4ff.StszBox{}
	allSync := true
	presStart = t.samples[0].pts - base
	presEnd = presStart
	for i, s := range t.samples {
		dts := s.dts - base
		if k := len(stts.SampleCount); k > 0 && stts.SampleTimeDelta[k-1] == s.dur {
			stts.SampleCount[k-1]++
		} else {
			stts.SampleCount = append(stts.SampleCount, 1)
			stts.SampleTimeDelta = append(stts.SampleTimeDelta, s.dur)
		}
		cto := s.pts - s.dts
		if cto < 0 {
			return nil, 0, 0, 0, fmt.Errorf("mp4: track %d: sample %d has PTS before DTS", t.id, i)
		}
		if ctts == nil && cto != 0 {
			ctts = &mp4ff.CttsBox{}
			for range i {
				ctts.SampleOffset = append(ctts.SampleOffset, 0)
			}
		}
		if ctts != nil {
			ctts.SampleOffset = append(ctts.SampleOffset, int32(cto))
		}
		if s.sync {
			stss.SampleNumber = append(stss.SampleNumber, uint32(i+1))
		} else {
			allSync = false
		}
		stsz.SampleSize = append(stsz.SampleSize, s.size)
		if p := s.pts - base; p < presStart {
			presStart = p
		}
		if e := s.pts - base + int64(s.dur); e > presEnd {
			presEnd = e
		}
		mediaDuration = uint64(dts) + uint64(s.dur)
	}
	stsz.SampleNumber = uint32(n)
	stbl.AddChild(stts)
	if ctts != nil {
		counts := make([]uint32, len(ctts.SampleOffset))
		for i := range counts {
			counts[i] = 1
		}
		offsets := ctts.SampleOffset
		ctts = &mp4ff.CttsBox{}
		if err := ctts.AddSampleCountsAndOffset(counts, offsets); err != nil {
			return nil, 0, 0, 0, err
		}
		stbl.AddChild(ctts)
	}
	if !allSync {
		stbl.AddChild(stss)
	}
	stsc := &mp4ff.StscBox{}
	for i, cnt := range t.chunkN {
		if k := len(stsc.Entries); k > 0 && stsc.Entries[k-1].SamplesPerChunk == cnt {
			continue
		}
		if err := stsc.AddEntry(uint32(i+1), cnt, 1); err != nil {
			return nil, 0, 0, 0, err
		}
	}
	stbl.AddChild(stsc)
	stbl.AddChild(stsz)
	if t.m.pos < 1<<32 {
		stco := &mp4ff.StcoBox{}
		for _, o := range t.chunkOff {
			stco.ChunkOffset = append(stco.ChunkOffset, uint32(o))
		}
		stbl.AddChild(stco)
	} else {
		stbl.AddChild(&mp4ff.Co64Box{ChunkOffset: t.chunkOff})
	}
	return stbl, mediaDuration, presStart, presEnd, nil
}

// Close finishes the mdat box, writes moov and closes the file when the
// Muxer was created with Create.
func (m *Muxer) Close() error {
	if m.closed {
		return nil
	}
	m.closed = true
	err := m.writeMoov()
	if m.closer != nil {
		if cerr := m.closer.Close(); err == nil {
			err = cerr
		}
	}
	return err
}

func (m *Muxer) writeMoov() error {
	// Patch the mdat size.
	size := uint64(m.pos - m.mdat)
	var buf [8]byte
	for i := range buf {
		buf[i] = byte(size >> (56 - 8*i))
	}
	if _, err := m.w.Seek(m.mdat+8, io.SeekStart); err != nil {
		return err
	}
	if _, err := m.w.Write(buf[:]); err != nil {
		return err
	}
	if _, err := m.w.Seek(m.pos, io.SeekStart); err != nil {
		return err
	}

	moov := mp4ff.NewMoovBox()
	mvhd := mp4ff.CreateMvhd()
	mvhd.Timescale = movieTimeScale
	mvhd.NextTrackID = uint32(len(m.tracks) + 1)
	moov.AddChild(mvhd)
	var movieDuration uint64
	for _, t := range m.tracks {
		if len(t.samples) == 0 {
			continue // a track that never received a sample is dropped
		}
		stbl, mediaDur, presStart, presEnd, err := t.finish()
		if err != nil {
			return err
		}
		trak, err := t.build(stbl)
		if err != nil {
			return err
		}
		trak.Mdia.Mdhd.Duration = mediaDur
		presDur := uint64(presEnd-presStart) * movieTimeScale / uint64(t.timeScale)
		trak.Tkhd.Duration = presDur
		switch {
		case t.edts != nil:
			trak.AddChild(t.edts)
			if len(t.edts.Elst) > 0 && len(t.edts.Elst[0].Entries) > 0 {
				trak.Tkhd.Duration = t.edts.Elst[0].Entries[0].SegmentDuration
			}
		case presStart > 0:
			// Hide the decoder delay of a B-frame stream: presentation
			// starts at the first PTS, not at decode time 0.
			elst := &mp4ff.ElstBox{Version: 1}
			elst.Entries = []mp4ff.ElstEntry{{SegmentDuration: presDur, MediaTime: presStart, MediaRateInteger: 1}}
			edts := &mp4ff.EdtsBox{}
			edts.AddChild(elst)
			trak.AddChild(edts)
		}
		if trak.Tkhd.Duration > movieDuration {
			movieDuration = trak.Tkhd.Duration
		}
		moov.AddChild(trak)
	}
	mvhd.Duration = movieDuration
	if len(moov.Traks) == 0 {
		return errors.New("mp4: no track received any sample")
	}
	return moov.Encode(m.w)
}
