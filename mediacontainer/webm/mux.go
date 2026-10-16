package webm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/opus"
)

// The writer's TimestampScale: block timestamps in milliseconds, as every
// WebM writer does.
const (
	tickNs       = 1_000_000
	ticksPerSec  = int64(time.Second) / tickNs
	seekHeadRoom = 96 // bytes reserved for the SeekHead written by Close
)

// A Cluster is closed at a video keyframe, before its relative block
// timestamps (16 bits) run out, and in an audio-only file every five
// seconds.
const (
	maxClusterSpan   = 30_000 // ticks
	audioClusterSpan = 5_000
	maxClusterBytes  = 32 << 20
)

// InterleaveWindow is how far (in time) the Muxer lets one track run ahead
// of another before it writes the earlier packets regardless. Packets of
// a track that falls further behind than this are still written, but in
// Clusters after later ones, which players handle less well: write the
// tracks interleaved, as a capture or a conversion that reads its input by
// time does.
const InterleaveWindow = time.Second

// Muxer writes a WebM file: the EBML header, a Segment with Info, Tracks,
// Clusters and Cues, and a SeekHead pointing at them. The output must be
// seekable because the Segment size, the duration and the SeekHead are
// only known at the end.
type Muxer struct {
	w      io.WriteSeeker
	closer io.Closer
	pos    int64
	// segment is the offset of the Segment's payload, from which the
	// SeekHead and Cues positions count.
	segment     int64
	durationPos int64
	infoPos     int64
	tracksPos   int64
	tracks      []*track
	header      bool

	cluster      []byte
	clusterTS    int64
	clusterOpen  bool
	clusterStart int64 // offset of the open cluster in the file
	cues         []cuePoint
	end          int64 // end of the last block in ticks

	closed bool
	err    error
}

type cuePoint struct {
	ts      int64
	track   uint64
	cluster int64 // relative to the Segment payload
}

type block struct {
	data    []byte
	ts      int64 // ticks
	key     bool
	dur     int64 // ticks
	discard int64 // ns
}

type track struct {
	m         *Muxer
	number    uint64
	video     bool
	timeScale int64
	pending   []block
	// entry builds the TrackEntry once the codec configuration is known.
	entry func() ([]byte, error)
	ready func() bool
	last  int64 // timestamp of the last block, for a duration estimate
	wrote bool  // a block of the track has been written
	seen  bool  // a packet of the track has been queued
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
	var ebml []byte
	ebml = appendUint(ebml, idEBMLVersion, 1)
	ebml = appendUint(ebml, idEBMLReadVersion, 1)
	ebml = appendUint(ebml, idEBMLMaxIDLength, 4)
	ebml = appendUint(ebml, idEBMLMaxSizeLength, 8)
	ebml = appendString(ebml, idDocType, "webm")
	ebml = appendUint(ebml, idDocTypeVersion, 4)
	ebml = appendUint(ebml, idDocTypeReadVersion, 2)
	b := appendElement(nil, idEBML, ebml)
	// The Segment size is patched by Close; until then it says unknown,
	// which a reader of an unfinished file understands.
	b = appendID(b, idSegment)
	b = append(b, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
	m.segment = int64(len(b))
	b = appendVoid(b, seekHeadRoom)
	if err := m.write(b); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Muxer) write(b []byte) error {
	if m.err != nil {
		return m.err
	}
	n, err := m.w.Write(b)
	m.pos += int64(n)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	m.err = err
	return err
}

func (m *Muxer) addTrack(video bool, timeScale uint32) (*track, error) {
	if m.closed {
		return nil, errors.New("webm: muxer is closed")
	}
	if m.header {
		return nil, errors.New("webm: tracks must be added before the first packet is written")
	}
	if timeScale == 0 || timeScale > math.MaxInt32 {
		return nil, errors.New("webm: a track needs a positive 32-bit time scale")
	}
	t := &track{m: m, number: uint64(len(m.tracks) + 1), video: video, timeScale: int64(timeScale)}
	m.tracks = append(m.tracks, t)
	return t, nil
}

// VideoWriter receives AV1 packets for the video track.
type VideoWriter struct {
	t   *track
	seq *av1.SequenceHeader
	// config is the av1C record from the first sequence header.
	config []byte
}

// AddVideoTrack adds an AV1 track whose packets are written with
// WritePacket. The codec configuration and the picture size are taken
// from the sequence header, so the first packet must be a keyframe that
// carries one (hwmediacodec encoders always put it in-band, and
// mp4.VideoTrack and ivf frames have it too). The time scale is the unit
// of Packet.PTS.
func (m *Muxer) AddVideoTrack(c hwmediacodec.Codec, timeScale uint32) (*VideoWriter, error) {
	if c != hwmediacodec.AV1 {
		return nil, fmt.Errorf("webm: cannot write %s video (WebM carries AV1 here)", c)
	}
	t, err := m.addTrack(true, timeScale)
	if err != nil {
		return nil, err
	}
	v := &VideoWriter{t: t}
	t.ready = func() bool { return v.config != nil }
	t.entry = v.entry
	return v, nil
}

// WritePacket writes one AV1 temporal unit. Temporal delimiters are
// removed, as the Matroska AV1 mapping asks.
func (v *VideoWriter) WritePacket(p hwmediacodec.Packet) error {
	m := v.t.m
	if err := m.check(); err != nil {
		return err
	}
	obus, err := av1.Split(p.Data)
	if err != nil {
		return fmt.Errorf("webm: %w", err)
	}
	for _, o := range obus {
		if o.Type == av1.OBUSequenceHeader && v.config == nil {
			sh, err := av1.ParseSequenceHeader(o.Payload)
			if err != nil {
				return fmt.Errorf("webm: %w", err)
			}
			v.seq, v.config = sh, av1.CodecConfigurationRecord(sh, o.Payload)
		}
	}
	if v.config == nil {
		return errors.New("webm: the first AV1 packet has no sequence header")
	}
	data := av1.StripTemporalDelimiters(p.Data, obus)
	if len(data) == 0 {
		return nil
	}
	return m.queue(v.t, block{data: append([]byte(nil), data...), ts: v.t.ticks(p.PTS), key: p.Keyframe})
}

func (v *VideoWriter) entry() ([]byte, error) {
	if v.config == nil {
		return nil, errors.New("webm: no packet was written to the video track")
	}
	var video []byte
	video = appendUint(video, idPixelWidth, uint64(v.seq.MaxFrameWidth))
	video = appendUint(video, idPixelHeight, uint64(v.seq.MaxFrameHeight))
	b := v.t.entryHead(trackVideo, "V_AV1", v.config)
	return appendElement(b, idVideo, video), nil
}

// AudioWriter receives Opus packets for the audio track.
type AudioWriter struct {
	t      *track
	format hwmediacodec.AudioFormat
	head   opus.Head
}

// AddAudioTrack adds an Opus track whose packets are written with
// WritePacket: pass AudioEncoder.Format. The time scale is the unit of
// Packet.PTS and Packet.Duration, which for an encoder opened without
// WithTimeScale is the sample rate. The encoder's delay becomes the
// track's CodecDelay (and the OpusHead's pre-skip), so players start at
// the first real sample; a last packet shorter than the samples it
// decodes to gets the difference as DiscardPadding.
func (m *Muxer) AddAudioTrack(format hwmediacodec.AudioFormat, timeScale uint32) (*AudioWriter, error) {
	if format.Codec != hwmediacodec.Opus {
		return nil, fmt.Errorf("webm: cannot write %s audio (WebM carries Opus here)", format.Codec)
	}
	head := opus.Head{Channels: format.Channels, PreSkip: format.Delay, InputSampleRate: format.SampleRate}
	if len(format.Config) > 0 {
		var err error
		if head, err = opus.ParseHead(format.Config); err != nil {
			return nil, fmt.Errorf("webm: %w", err)
		}
		head.PreSkip = format.Delay
	}
	if head.Channels <= 0 || head.Channels > 255 || format.Delay < 0 || format.Delay > math.MaxUint16 {
		return nil, fmt.Errorf("webm: an Opus track needs 1 to 255 channels and a delay below 65536 samples, got %d and %d", head.Channels, format.Delay)
	}
	if head.InputSampleRate <= 0 {
		head.InputSampleRate = opus.SampleRate
	}
	t, err := m.addTrack(false, timeScale)
	if err != nil {
		return nil, err
	}
	a := &AudioWriter{t: t, format: format, head: head}
	t.ready = func() bool { return true }
	t.entry = a.entry
	return a, nil
}

// WritePacket writes one Opus packet. Its PTS is on the presentation's
// time line, as an encoder gives it: the first packet starts Delay
// samples before 0.
func (a *AudioWriter) WritePacket(p hwmediacodec.Packet) error {
	m := a.t.m
	if err := m.check(); err != nil {
		return err
	}
	if len(p.Data) == 0 {
		return nil
	}
	// Encoder packets start the codec delay before PTS 0; in the file
	// the first block is at 0 and CodecDelay says how much to drop.
	start := codec.Rescale(p.PTS, a.t.timeScale, opus.SampleRate) + int64(a.format.Delay)
	b := block{data: append([]byte(nil), p.Data...), ts: codec.Rescale(start, opus.SampleRate, ticksPerSec), key: true}
	if n, err := opus.PacketSamples(p.Data); err == nil && p.Duration > 0 {
		dur := codec.Rescale(p.Duration, a.t.timeScale, opus.SampleRate)
		if dur < int64(n) {
			b.discard = codec.Rescale(int64(n)-dur, opus.SampleRate, int64(time.Second))
		}
		b.dur = codec.Rescale(dur, opus.SampleRate, ticksPerSec)
	} else if p.Duration > 0 {
		b.dur = codec.Rescale(p.Duration, a.t.timeScale, ticksPerSec)
	}
	return m.queue(a.t, b)
}

func (a *AudioWriter) entry() ([]byte, error) {
	b := a.t.entryHead(trackAudio, "A_OPUS", a.head.Marshal())
	b = appendUint(b, idCodecDelay, uint64(codec.Rescale(int64(a.format.Delay), opus.SampleRate, int64(time.Second))))
	b = appendUint(b, idSeekPreRoll, uint64(80*time.Millisecond))
	var audio []byte
	audio = appendFloat(audio, idSamplingFreq, opus.SampleRate)
	audio = appendUint(audio, idChannels, uint64(a.head.Channels))
	return appendElement(b, idAudio, audio), nil
}

func (t *track) entryHead(typ int, codecID string, private []byte) []byte {
	var b []byte
	b = appendUint(b, idTrackNumber, t.number)
	b = appendUint(b, idTrackUID, t.number)
	b = appendUint(b, idTrackType, uint64(typ))
	b = appendUint(b, idFlagLacing, 0)
	b = appendString(b, idLanguage, "und")
	b = appendString(b, idCodecID, codecID)
	return appendElement(b, idCodecPrivate, private)
}

// ticks converts a packet time to milliseconds.
func (t *track) ticks(pts int64) int64 {
	return codec.Rescale(pts, t.timeScale, ticksPerSec)
}

func (m *Muxer) check() error {
	if m.err != nil {
		return m.err
	}
	if m.closed {
		return errors.New("webm: muxer is closed")
	}
	return nil
}

// queue adds a block to its track and writes what the interleaving
// allows.
func (m *Muxer) queue(t *track, b block) error {
	if b.ts < 0 {
		return fmt.Errorf("webm: packet at %d ms lies before the start of the file", b.ts)
	}
	t.pending = append(t.pending, b)
	t.seen = true
	return m.flush(false)
}

// flush writes pending blocks in time order. Before the track list is
// written every track has to have seen a packet (the video track's
// configuration comes from its first one); after that the earliest block
// is written once every track has one waiting, or once another track has
// run InterleaveWindow ahead of it. At the end everything goes.
func (m *Muxer) flush(all bool) error {
	if !m.header {
		for _, t := range m.tracks {
			if !all && (!t.seen || !t.ready()) {
				return nil
			}
		}
		if err := m.writeHeader(); err != nil {
			return err
		}
	}
	window := int64(InterleaveWindow / tickNs)
	for {
		var first *track
		var latest int64 = math.MinInt64
		waiting := true
		for _, t := range m.tracks {
			if len(t.pending) == 0 {
				waiting = false
				continue
			}
			if first == nil || t.pending[0].ts < first.pending[0].ts {
				first = t
			}
			latest = max(latest, t.pending[len(t.pending)-1].ts)
		}
		if first == nil {
			return nil
		}
		if !all && !waiting && latest-first.pending[0].ts <= window {
			return nil
		}
		b := first.pending[0]
		first.pending = first.pending[1:]
		if err := m.writeBlock(first, b); err != nil {
			return err
		}
	}
}

func (m *Muxer) writeHeader() error {
	m.header = true
	m.infoPos = m.pos
	var info []byte
	info = appendUint(info, idTimestScale, tickNs)
	info = appendString(info, idMuxingApp, "hwmediacodec")
	info = appendString(info, idWritingApp, "hwmediacodec")
	durationAt := len(info) + 3 // after the Duration element's ID and size
	info = appendFloat(info, idDuration, 0)
	b := appendElement(nil, idInfo, info)
	m.durationPos = m.pos + int64(len(b)-len(info)+durationAt)
	m.tracksPos = m.pos + int64(len(b))
	var tracks []byte
	for _, t := range m.tracks {
		e, err := t.entry()
		if err != nil {
			m.err = err
			return err
		}
		tracks = appendElement(tracks, idTrackEntry, e)
	}
	b = appendElement(b, idTracks, tracks)
	return m.write(b)
}

func (m *Muxer) hasVideo() bool {
	for _, t := range m.tracks {
		if t.video {
			return true
		}
	}
	return false
}

func (m *Muxer) writeBlock(t *track, b block) error {
	span := b.ts - m.clusterTS
	newCluster := !m.clusterOpen || span > maxClusterSpan || span < -maxClusterSpan ||
		len(m.cluster) > maxClusterBytes ||
		(t.video && b.key && len(m.cluster) > 0) ||
		(!m.hasVideo() && span >= audioClusterSpan)
	if newCluster {
		if err := m.closeCluster(); err != nil {
			return err
		}
		m.clusterOpen = true
		m.clusterTS = b.ts
		m.clusterStart = m.pos
		m.cluster = appendUint(m.cluster[:0], idTimestamp, uint64(b.ts))
		if (t.video && b.key) || !m.hasVideo() {
			m.cues = append(m.cues, cuePoint{ts: b.ts, track: t.number, cluster: m.clusterStart - m.segment})
		}
	}
	head := appendSize(nil, t.number)
	head = binary.BigEndian.AppendUint16(head, uint16(int16(b.ts-m.clusterTS)))
	if b.discard > 0 {
		// A BlockGroup: the Block, its duration and the padding to drop.
		var g []byte
		g = appendID(g, idBlock)
		g = appendSize(g, uint64(len(head)+1+len(b.data)))
		g = append(g, head...)
		g = append(g, 0)
		g = append(g, b.data...)
		g = appendUint(g, idBlockDuration, uint64(max(b.dur, 0)))
		g = appendInt(g, idDiscardPadding, b.discard)
		m.cluster = appendElement(m.cluster, idBlockGroup, g)
	} else {
		flags := byte(0)
		if b.key {
			flags = 0x80
		}
		m.cluster = appendID(m.cluster, idSimpleBlock)
		m.cluster = appendSize(m.cluster, uint64(len(head)+1+len(b.data)))
		m.cluster = append(m.cluster, head...)
		m.cluster = append(m.cluster, flags)
		m.cluster = append(m.cluster, b.data...)
	}
	end := b.ts + b.dur
	if b.dur <= 0 && t.wrote && b.ts > t.last {
		end = b.ts + (b.ts - t.last) // a video frame lasts as long as the one before
	}
	t.last, t.wrote = b.ts, true
	m.end = max(m.end, end)
	return nil
}

func (m *Muxer) closeCluster() error {
	if !m.clusterOpen {
		return nil
	}
	m.clusterOpen = false
	return m.write(appendElement(nil, idCluster, m.cluster))
}

// Close writes the remaining packets, the Cues and the SeekHead, patches
// the Segment size and the duration, and closes the file when the Muxer
// was created with Create.
func (m *Muxer) Close() error {
	if m.closed {
		return m.err
	}
	err := m.finish()
	m.closed = true
	if m.closer != nil {
		if cerr := m.closer.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil && m.err == nil {
		m.err = err
	}
	return err
}

func (m *Muxer) finish() error {
	if len(m.tracks) == 0 {
		return errors.New("webm: the file has no track")
	}
	if err := m.flush(true); err != nil {
		return err
	}
	if err := m.closeCluster(); err != nil {
		return err
	}
	cuesPos := m.pos
	var cues []byte
	for _, c := range m.cues {
		var pos []byte
		pos = appendUint(pos, idCueTrack, c.track)
		pos = appendUint(pos, idCueClusterPosition, uint64(c.cluster))
		var cp []byte
		cp = appendUint(cp, idCueTime, uint64(c.ts))
		cp = appendElement(cp, idCueTrackPositions, pos)
		cues = appendElement(cues, idCuePoint, cp)
	}
	if len(cues) > 0 {
		if err := m.write(appendElement(nil, idCues, cues)); err != nil {
			return err
		}
	}
	end := m.pos

	var seek []byte
	for _, e := range []struct {
		id  uint32
		pos int64
	}{{idInfo, m.infoPos}, {idTracks, m.tracksPos}, {idCues, cuesPos}} {
		if e.id == idCues && len(cues) == 0 {
			continue
		}
		var s []byte
		s = appendElement(s, idSeekID, appendID(nil, e.id))
		s = appendUint(s, idSeekPos, uint64(e.pos-m.segment))
		seek = appendElement(seek, idSeek, s)
	}
	head := appendElement(nil, idSeekHead, seek)
	head = appendVoid(head, seekHeadRoom-len(head))
	if err := m.patch(m.segment, head); err != nil {
		return err
	}
	var dur [8]byte
	binary.BigEndian.PutUint64(dur[:], math.Float64bits(float64(m.end)))
	if err := m.patch(m.durationPos, dur[:]); err != nil {
		return err
	}
	if err := m.patch(m.segment-7, appendSizeLen(nil, uint64(end-m.segment), 8)[1:]); err != nil {
		return err
	}
	_, err := m.w.Seek(end, io.SeekStart)
	return err
}

// patch overwrites bytes already written.
func (m *Muxer) patch(at int64, b []byte) error {
	if m.err != nil {
		return m.err
	}
	if _, err := m.w.Seek(at, io.SeekStart); err != nil {
		m.err = err
		return err
	}
	_, err := m.w.Write(b)
	if err != nil {
		m.err = err
	}
	return err
}
