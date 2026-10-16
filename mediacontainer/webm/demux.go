// Package webm reads and writes WebM files around hwmediacodec, which
// itself only handles elementary streams. WebM is the Matroska subset of
// the web; this package handles its AV1 video ("V_AV1") and Opus audio
// ("A_OPUS") tracks, the pair browsers record and play everywhere, and
// leaves the rest (VP8, VP9, Vorbis) alone.
//
// The Demuxer indexes every frame of the AV1 and Opus tracks when it opens
// a file, so it needs neither Cues nor known element sizes: the files a
// browser's MediaRecorder leaves behind (one Segment and Clusters of
// unknown size, no Cues) open and seek like any other, and a file cut off
// while it was being written opens with the frames it has. The Muxer
// writes a seekable WebM file with Cues, interleaving the tracks by time.
//
// Opus needs no Ogg container here: a WebM block holds one Opus packet as
// it comes from hwmediacodec.NewAudioEncoder, and the OpusHead goes into
// the track's CodecPrivate.
package webm

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/opus"
)

// MaxFrames bounds the number of frames NewDemuxer indexes over all the
// tracks it reads. The index costs 32 bytes per frame; the default of 8
// million frames is about 37 hours of 60 fps video or 44 hours of 20 ms
// Opus packets. A file with more fails NewDemuxer with an error naming
// this limit; raise it before opening a longer file.
var MaxFrames = 8_000_000

// maxHeaderElement bounds the size of the elements read into memory
// whole: the Info, the track list and a codec configuration.
const maxHeaderElement = 16 << 20

// Track is a track of the file as its TrackEntry describes it.
type Track struct {
	Number uint64
	// Type is 1 for video, 2 for audio (the Matroska TrackType).
	Type int
	// CodecID is the Matroska codec ID: "V_AV1", "A_OPUS", "V_VP9", ...
	CodecID string
	// CodecPrivate is the codec configuration: the av1C record for AV1,
	// the OpusHead for Opus.
	CodecPrivate []byte

	codecDelay      uint64 // ns; for Opus the pre-skip when the track gives none
	seekPreRoll     uint64 // ns
	defaultDuration uint64 // ns
	encoded         bool   // ContentEncodings: compressed or encrypted
	width, height   int
	channels        int

	d      *Demuxer
	frames []frame
}

// frame is one frame of the index. A laced block contributes one frame
// per lace.
type frame struct {
	offset  int64 // in the file's own frame, where its first byte is 0
	ts      int64 // in Segment ticks
	dur     int64 // in Segment ticks
	discard int64 // DiscardPadding in ns
	size    uint32
	key     bool
	// given says that dur is the BlockDuration of the file, which (as
	// ffmpeg writes it) already leaves the DiscardPadding out; otherwise
	// dur is derived and the padding is still in it.
	given bool
}

// VideoTrack is the AV1 track of a file.
type VideoTrack struct {
	*Track
	Codec  hwmediacodec.Codec
	Width  int
	Height int
	// seqHeader is the sequence header OBU from the av1C record, put in
	// front of a keyframe that does not carry its own.
	seqHeader []byte
}

// AudioTrack is the Opus track of a file.
type AudioTrack struct {
	*Track
	format hwmediacodec.AudioFormat
}

// Demuxer is an open WebM file. Its tracks may be read from different
// goroutines (a player decodes pictures and sound side by side); the
// reads of the file itself take turns.
type Demuxer struct {
	rs     io.ReadSeeker
	closer io.Closer
	start  int64
	// scale is the TimestampScale: nanoseconds per tick of the block
	// timestamps.
	scale    int64
	duration float64 // in ticks, 0 when the Info does not give it
	tracks   []*Track
	video    *VideoTrack
	audio    *AudioTrack
	nframes  int
	mu       sync.Mutex
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

// NewDemuxer parses the header of rs and indexes its AV1 and Opus frames.
// Frame data is read on demand, so rs must stay open while the Demuxer is
// used. The file is taken to start at the current position of rs.
func NewDemuxer(rs io.ReadSeeker) (*Demuxer, error) {
	start, err := rs.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, fmt.Errorf("webm: reader position: %w", err)
	}
	end, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("webm: reader size: %w", err)
	}
	if start < 0 || end < start {
		return nil, errors.New("webm: reader position lies beyond its end")
	}
	if _, err := rs.Seek(start, io.SeekStart); err != nil {
		return nil, fmt.Errorf("webm: restore reader position: %w", err)
	}
	d := &Demuxer{rs: rs, start: start, scale: 1_000_000}
	s := newScanner(rs, start, end-start)
	if err := d.parse(s); err != nil {
		return nil, err
	}
	if d.video == nil && d.audio == nil {
		ids := make([]string, len(d.tracks))
		for i, t := range d.tracks {
			ids[i] = t.CodecID
		}
		return nil, fmt.Errorf("webm: no AV1 or Opus track (the file has %v)", ids)
	}
	return d, nil
}

func (d *Demuxer) parse(s *scanner) error {
	id, size, _, err := s.header()
	if err != nil || id != idEBML {
		return errors.New("webm: not an EBML file")
	}
	if size < 0 || size > 4096 {
		return fmt.Errorf("%w: EBML header of size %d", errInvalid, size)
	}
	b, err := s.read(size)
	if err != nil {
		return fmt.Errorf("webm: read EBML header: %w", err)
	}
	els, err := children(b)
	if err != nil {
		return err
	}
	for _, e := range els {
		if e.id == idDocType {
			if t := e.string(); t != "webm" && t != "matroska" {
				return fmt.Errorf("webm: document type %q is not WebM or Matroska", t)
			}
		}
	}
	// Elements may sit between the EBML header and the Segment (a Void).
	for {
		id, size, _, err = s.header()
		if err != nil {
			return fmt.Errorf("webm: no Segment: %w", err)
		}
		if id == idSegment {
			break
		}
		if size < 0 {
			return fmt.Errorf("%w: element 0x%X of unknown size before the Segment", errInvalid, id)
		}
		if err := s.seek(s.pos + size); err != nil {
			return err
		}
	}
	segEnd := s.end
	if size >= 0 && size < s.end-s.pos {
		segEnd = s.pos + size
	}
	pos := s.pos
	tracksSeen := false
	for pos < segEnd {
		if err := s.seek(pos); err != nil {
			return err
		}
		id, size, n, err := s.header()
		if err != nil {
			if !tracksSeen {
				return fmt.Errorf("webm: read element at %d: %w", pos, err)
			}
			break // a file cut off while it was written: keep what it has
		}
		data := pos + int64(n)
		if size >= 0 && size > segEnd-data {
			if id != idCluster {
				return fmt.Errorf("%w: element 0x%X runs past the Segment", errInvalid, id)
			}
			size = unknownSize // a cut-off Cluster
		}
		switch id {
		case idInfo, idTracks:
			if size < 0 || size > maxHeaderElement {
				return fmt.Errorf("%w: element 0x%X of size %d", errInvalid, id, size)
			}
			b, err := s.read(size)
			if err != nil {
				return fmt.Errorf("webm: read element 0x%X: %w", id, err)
			}
			if id == idInfo {
				err = d.parseInfo(b)
			} else {
				if tracksSeen {
					break
				}
				tracksSeen = true
				err = d.parseTracks(b)
			}
			if err != nil {
				return err
			}
		case idCluster:
			if !tracksSeen {
				return errors.New("webm: a Cluster comes before the track list")
			}
			next, err := d.scanCluster(s, data, size, segEnd)
			if err != nil {
				return err
			}
			pos = next
			continue
		default:
			if size < 0 {
				return fmt.Errorf("%w: element 0x%X of unknown size in the Segment", errInvalid, id)
			}
		}
		pos = data + size
	}
	if !tracksSeen {
		return errors.New("webm: the file has no track list")
	}
	for _, t := range []*Track{d.videoTrack(), d.audioTrack()} {
		if t != nil {
			t.finish()
		}
	}
	return nil
}

func (d *Demuxer) videoTrack() *Track {
	if d.video == nil {
		return nil
	}
	return d.video.Track
}

func (d *Demuxer) audioTrack() *Track {
	if d.audio == nil {
		return nil
	}
	return d.audio.Track
}

func (d *Demuxer) parseInfo(b []byte) error {
	els, err := children(b)
	if err != nil {
		return err
	}
	for _, e := range els {
		switch e.id {
		case idTimestScale:
			v, err := e.uint()
			if err != nil {
				return err
			}
			if v == 0 || v > 1e12 {
				return fmt.Errorf("%w: TimestampScale %d", errInvalid, v)
			}
			d.scale = int64(v)
		case idDuration:
			v, err := e.float()
			if err != nil {
				return err
			}
			if v > 0 && !math.IsInf(v, 0) {
				d.duration = v
			}
		}
	}
	return nil
}

func (d *Demuxer) parseTracks(b []byte) error {
	els, err := children(b)
	if err != nil {
		return err
	}
	for _, e := range els {
		if e.id != idTrackEntry {
			continue
		}
		t, err := d.parseTrackEntry(e.data)
		if err != nil {
			return err
		}
		d.tracks = append(d.tracks, t)
		switch {
		case d.video == nil && t.Type == trackVideo && t.CodecID == "V_AV1" && !t.encoded:
			d.video = newVideoTrack(t)
		case d.audio == nil && t.Type == trackAudio && t.CodecID == "A_OPUS" && !t.encoded:
			d.audio = newAudioTrack(t)
		}
	}
	return nil
}

func (d *Demuxer) parseTrackEntry(b []byte) (*Track, error) {
	els, err := children(b)
	if err != nil {
		return nil, err
	}
	t := &Track{d: d}
	for _, e := range els {
		var err error
		var v uint64
		switch e.id {
		case idTrackNumber:
			t.Number, err = e.uint()
		case idTrackType:
			v, err = e.uint()
			t.Type = int(min(v, 255))
		case idCodecID:
			t.CodecID = e.string()
		case idCodecPrivate:
			t.CodecPrivate = append([]byte(nil), e.data...)
		case idCodecDelay:
			t.codecDelay, err = e.uint()
		case idSeekPreRoll:
			t.seekPreRoll, err = e.uint()
		case idDefaultDuration:
			t.defaultDuration, err = e.uint()
		case idContentEncs:
			t.encoded = true
		case idVideo:
			err = t.parseVideo(e.data)
		case idAudio:
			err = t.parseAudio(e.data)
		}
		if err != nil {
			return nil, err
		}
	}
	if t.Number == 0 {
		return nil, fmt.Errorf("%w: track without a number", errInvalid)
	}
	return t, nil
}

func (t *Track) parseVideo(b []byte) error {
	els, err := children(b)
	if err != nil {
		return err
	}
	for _, e := range els {
		switch e.id {
		case idPixelWidth, idPixelHeight:
			v, err := e.uint()
			if err != nil {
				return err
			}
			v = min(v, 1<<16)
			if e.id == idPixelWidth {
				t.width = int(v)
			} else {
				t.height = int(v)
			}
		}
	}
	return nil
}

func (t *Track) parseAudio(b []byte) error {
	els, err := children(b)
	if err != nil {
		return err
	}
	for _, e := range els {
		if e.id == idChannels {
			v, err := e.uint()
			if err != nil {
				return err
			}
			t.channels = int(min(v, 255))
		}
	}
	return nil
}

func newVideoTrack(t *Track) *VideoTrack {
	v := &VideoTrack{Track: t, Codec: hwmediacodec.AV1, Width: t.width, Height: t.height}
	// av1C: four bytes of fields, then the configuration OBUs.
	if len(t.CodecPrivate) > 4 {
		if obus, err := av1.Split(t.CodecPrivate[4:]); err == nil {
			for _, o := range obus {
				if o.Type == av1.OBUSequenceHeader {
					v.seqHeader = append([]byte(nil), o.Raw...)
					if v.Width == 0 || v.Height == 0 {
						if sh, err := av1.ParseSequenceHeader(o.Payload); err == nil {
							v.Width, v.Height = sh.MaxFrameWidth, sh.MaxFrameHeight
						}
					}
				}
			}
		}
	}
	return v
}

func newAudioTrack(t *Track) *AudioTrack {
	head, err := opus.ParseHead(t.CodecPrivate)
	if err != nil {
		if t.channels != 1 && t.channels != 2 {
			return nil
		}
		// A file without an OpusHead: mono or stereo needs no mapping.
		head = opus.Head{Channels: t.channels, InputSampleRate: opus.SampleRate}
	}
	a := &AudioTrack{Track: t}
	a.format = hwmediacodec.AudioFormat{
		Codec:      hwmediacodec.Opus,
		Config:     head.Marshal(),
		SampleRate: opus.SampleRate,
		Channels:   head.Channels,
		Delay:      head.PreSkip,
	}
	if t.codecDelay > 0 {
		a.format.Delay = int(codec.Rescale(int64(min(t.codecDelay, math.MaxInt64)), int64(time.Second), opus.SampleRate))
	} else {
		// The packets' times count from the presentation either way.
		t.codecDelay = uint64(codec.Rescale(int64(head.PreSkip), opus.SampleRate, int64(time.Second)))
	}
	if t.defaultDuration > 0 {
		a.format.FrameSamples = int(codec.Rescale(int64(min(t.defaultDuration, math.MaxInt64)), int64(time.Second), opus.SampleRate))
	}
	return a
}

// scanCluster indexes the blocks of the Cluster whose payload starts at
// data and returns where the next element of the Segment starts.
func (d *Demuxer) scanCluster(s *scanner, data, size, segEnd int64) (int64, error) {
	end := segEnd
	if size >= 0 {
		end = data + size
	}
	var clusterTS int64
	pos := data
	for pos < end {
		if err := s.seek(pos); err != nil {
			return 0, err
		}
		id, csize, n, err := s.header()
		if err != nil {
			return end, nil // cut off inside the Cluster
		}
		if size < 0 && isTopLevel(id) {
			return pos, nil
		}
		cdata := pos + int64(n)
		if csize < 0 || csize > end-cdata {
			return end, nil // cut off, or broken: keep the frames before it
		}
		switch id {
		case idTimestamp:
			if csize > 8 {
				return 0, fmt.Errorf("%w: Cluster Timestamp of %d bytes", errInvalid, csize)
			}
			b, err := s.read(csize)
			if err != nil {
				return end, nil
			}
			v, _ := element{data: b}.uint()
			clusterTS = int64(min(v, math.MaxInt64/2))
		case idSimpleBlock:
			if err := d.indexBlock(s, cdata, csize, clusterTS, true, blockExtras{}); err != nil {
				return 0, err
			}
		case idBlockGroup:
			if err := d.scanBlockGroup(s, cdata, csize, clusterTS); err != nil {
				return 0, err
			}
		}
		pos = cdata + csize
	}
	return end, nil
}

// blockExtras are the BlockGroup children that describe its Block.
type blockExtras struct {
	duration  int64 // ticks
	reference bool
	discard   int64 // ns
}

func (d *Demuxer) scanBlockGroup(s *scanner, data, size, clusterTS int64) error {
	if size > maxHeaderElement {
		// A BlockGroup is read whole, so that the Block can be indexed
		// with what comes after it; frames are rarely that big.
		return fmt.Errorf("%w: BlockGroup of %d bytes", errInvalid, size)
	}
	if err := s.seek(data); err != nil {
		return err
	}
	b, err := s.read(size)
	if err != nil {
		return nil // cut off
	}
	els, err := children(b)
	if err != nil {
		return err
	}
	var ex blockExtras
	var block []byte
	for _, e := range els {
		switch e.id {
		case idBlock:
			block = e.data
		case idBlockDuration:
			v, err := e.uint()
			if err != nil {
				return err
			}
			ex.duration = int64(min(v, math.MaxInt64/2))
		case idReferenceBlock:
			ex.reference = true
		case idDiscardPadding:
			if ex.discard, err = e.int(); err != nil {
				return err
			}
		}
	}
	if block == nil {
		return nil
	}
	// The Block's payload offset in the file: its data aliases b.
	off := data + int64(cap(b)-cap(block))
	return d.indexBlockBytes(block, off, clusterTS, false, ex)
}

// indexBlock reads the header of the (Simple)Block at data and indexes
// its frames.
func (d *Demuxer) indexBlock(s *scanner, data, size, clusterTS int64, simple bool, ex blockExtras) error {
	// Track number, timestamp and flags fit in 12 bytes. The sizes of
	// Xiph and EBML lacing can take many more, but never more than the
	// block.
	if err := s.seek(data); err != nil {
		return err
	}
	head, err := s.read(min(size, 12))
	if err != nil {
		return nil // cut off
	}
	if n := vintLen(head); n > 0 && len(head) > n+2 && head[n+2]>>1&3 != 0 && head[n+2]>>1&3 != 3 {
		if err := s.seek(data); err != nil {
			return err
		}
		if head, err = s.read(min(size, 64<<10)); err != nil {
			return nil
		}
	}
	return d.indexBlockHeader(head, data, size, clusterTS, simple, ex)
}

// vintLen is the length of the variable-length integer at the start of b,
// or 0 when b does not start with one.
func vintLen(b []byte) int {
	_, n, ok := readVint(b, false)
	if !ok {
		return 0
	}
	return n
}

func (d *Demuxer) indexBlockBytes(block []byte, off, clusterTS int64, simple bool, ex blockExtras) error {
	return d.indexBlockHeader(block, off, int64(len(block)), clusterTS, simple, ex)
}

// indexBlockHeader indexes the frames of a block whose first bytes are
// head; the block has size bytes from offset off.
func (d *Demuxer) indexBlockHeader(head []byte, off, size, clusterTS int64, simple bool, ex blockExtras) error {
	num, n, ok := readVint(head, false)
	if !ok || len(head) < n+3 {
		return fmt.Errorf("%w: block header at %d", errInvalid, off)
	}
	var t *Track
	switch {
	case d.video != nil && num == d.video.Number:
		t = d.video.Track
	case d.audio != nil && num == d.audio.Number:
		t = d.audio.Track
	default:
		return nil
	}
	ts := clusterTS + int64(int16(uint16(head[n])<<8|uint16(head[n+1])))
	flags := head[n+2]
	key := !ex.reference
	if simple {
		key = flags&0x80 != 0
	}
	p := n + 3
	sizes, p, err := laceSizes(head, p, size, flags>>1&3)
	if err != nil {
		return fmt.Errorf("%w (block at %d)", err, off)
	}
	if d.nframes+len(sizes) > MaxFrames || MaxFrames < 0 {
		return fmt.Errorf("webm: the file has more than MaxFrames (%d) frames", MaxFrames)
	}
	pos := off + int64(p)
	step := int64(0)
	if len(sizes) > 1 && t.defaultDuration > 0 {
		step = int64(t.defaultDuration) / d.scale
	}
	for i, sz := range sizes {
		f := frame{offset: pos, ts: ts + int64(i)*step, size: uint32(sz), key: key}
		if i == len(sizes)-1 {
			f.dur, f.discard, f.given = ex.duration, ex.discard, ex.duration > 0
		}
		t.frames = append(t.frames, f)
		pos += int64(sz)
	}
	d.nframes += len(sizes)
	return nil
}

// laceSizes returns the sizes of the frames of a block whose lace header
// starts at head[p], and the offset of the first frame.
func laceSizes(head []byte, p int, size int64, lacing byte) ([]int64, int, error) {
	if lacing == 0 {
		if size-int64(p) > math.MaxUint32 {
			return nil, 0, fmt.Errorf("%w: frame of %d bytes", errInvalid, size-int64(p))
		}
		return []int64{size - int64(p)}, p, nil
	}
	if p >= len(head) {
		return nil, 0, fmt.Errorf("%w: truncated lace header", errInvalid)
	}
	count := int(head[p]) + 1
	p++
	sizes := make([]int64, count)
	var sum int64
	switch lacing {
	case 1: // Xiph
		for i := 0; i < count-1; i++ {
			for {
				if p >= len(head) {
					return nil, 0, fmt.Errorf("%w: truncated Xiph lace sizes", errInvalid)
				}
				sizes[i] += int64(head[p])
				p++
				if head[p-1] != 255 {
					break
				}
			}
			sum += sizes[i]
		}
	case 3: // fixed
		rest := size - int64(p)
		if rest%int64(count) != 0 {
			return nil, 0, fmt.Errorf("%w: %d bytes do not split into %d equal laces", errInvalid, rest, count)
		}
		for i := range sizes {
			sizes[i] = rest / int64(count)
		}
		return sizes, p, nil
	case 2: // EBML
		v, n, ok := readVint(head[p:], false)
		if !ok {
			return nil, 0, fmt.Errorf("%w: bad EBML lace size", errInvalid)
		}
		p += n
		sizes[0] = int64(min(v, math.MaxInt32))
		sum = sizes[0]
		for i := 1; i < count-1; i++ {
			v, n, ok := readVint(head[p:], false)
			if !ok {
				return nil, 0, fmt.Errorf("%w: bad EBML lace size", errInvalid)
			}
			p += n
			sizes[i] = sizes[i-1] + int64(v) - (1<<(7*n-1) - 1)
			if sizes[i] < 0 || sizes[i] > math.MaxInt32 {
				return nil, 0, fmt.Errorf("%w: EBML lace size out of range", errInvalid)
			}
			sum += sizes[i]
		}
	}
	last := size - int64(p) - sum
	if last < 0 {
		return nil, 0, fmt.Errorf("%w: laces exceed the block", errInvalid)
	}
	sizes[count-1] = last
	return sizes, p, nil
}

// finish fills in the durations the blocks did not give: the gap to the
// next frame, and for the last one the track's default duration or the
// gap before it.
func (t *Track) finish() {
	sort.SliceStable(t.frames, func(i, j int) bool { return t.frames[i].ts < t.frames[j].ts })
	for i := range t.frames {
		f := &t.frames[i]
		if f.dur > 0 {
			continue
		}
		switch {
		case i+1 < len(t.frames):
			f.dur = t.frames[i+1].ts - f.ts
		case t.defaultDuration > 0:
			f.dur = int64(t.defaultDuration) / t.d.scale
		case i > 0:
			f.dur = f.ts - t.frames[i-1].ts
		}
	}
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

// Video returns the AV1 track, or nil when the file has none.
func (d *Demuxer) Video() *VideoTrack { return d.video }

// Audio returns the Opus track, or nil when the file has none.
func (d *Demuxer) Audio() *AudioTrack { return d.audio }

// Tracks lists every track of the file in file order, including the ones
// this package does not read.
func (d *Demuxer) Tracks() []*Track { return d.tracks }

// TimestampScale is the number of nanoseconds per tick of the file's
// block timestamps (1,000,000 in nearly every file).
func (d *Demuxer) TimestampScale() int64 { return d.scale }

// Duration is the Segment duration from the Info, or the end of the last
// frame when the Info does not give it (a live recording).
func (d *Demuxer) Duration() time.Duration {
	if d.duration > 0 {
		ns := d.duration * float64(d.scale)
		if ns >= math.MaxInt64 {
			return time.Duration(math.MaxInt64)
		}
		return time.Duration(ns)
	}
	var end time.Duration
	for _, t := range []*Track{d.videoTrack(), d.audioTrack()} {
		if t != nil {
			end = max(end, t.length())
		}
	}
	return end
}

func (d *Demuxer) readAt(off int64, buf []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.rs.Seek(d.start+off, io.SeekStart); err != nil {
		return err
	}
	_, err := io.ReadFull(d.rs, buf)
	return err
}

// FrameCount is the number of frames of the track.
func (t *Track) FrameCount() int { return len(t.frames) }

// TimeOf converts a block timestamp of the file to a duration.
func (t *Track) TimeOf(ticks int64) time.Duration {
	return time.Duration(codec.RescaleTrunc(ticks, 1, t.d.scale))
}

// length is the end of the last frame.
func (t *Track) length() time.Duration {
	if len(t.frames) == 0 {
		return 0
	}
	f := t.frames[len(t.frames)-1]
	end := t.TimeOf(f.ts + f.dur)
	if !f.given {
		end -= time.Duration(f.discard)
	}
	return max(end, 0)
}

// read reads the data of frame i.
func (t *Track) read(i int) ([]byte, error) {
	if i < 0 || i >= len(t.frames) {
		return nil, fmt.Errorf("webm: frame %d out of range [0, %d)", i, len(t.frames))
	}
	f := t.frames[i]
	buf := make([]byte, f.size)
	if err := t.d.readAt(f.offset, buf); err != nil {
		return nil, fmt.Errorf("webm: read frame %d: %w", i, err)
	}
	return buf, nil
}

// TimeScale is the unit of the video packets' PTS: the ticks of the file
// when a second holds a whole number of them (1000 for the usual
// millisecond timestamps), nanoseconds otherwise.
func (v *VideoTrack) TimeScale() int32 {
	if time.Second%time.Duration(v.d.scale) == 0 {
		return int32(int64(time.Second) / v.d.scale)
	}
	return int32(time.Second)
}

func (v *VideoTrack) units(ticks int64) int64 {
	if time.Second%time.Duration(v.d.scale) != 0 {
		return codec.Rescale(ticks, 1, v.d.scale) // nanoseconds
	}
	return ticks
}

// Packet reads frame i as a decoder packet: one temporal unit, with the
// sequence header of the track's configuration in front of a keyframe
// that does not carry its own. PTS and DTS are in units of TimeScale.
func (v *VideoTrack) Packet(i int) (hwmediacodec.Packet, error) {
	data, err := v.read(i)
	if err != nil {
		return hwmediacodec.Packet{}, err
	}
	f := v.frames[i]
	if f.key && v.seqHeader != nil {
		if obus, err := av1.Split(data); err == nil && !hasSequenceHeader(obus) {
			data = append(append(make([]byte, 0, len(v.seqHeader)+len(data)), v.seqHeader...), data...)
		}
	}
	pts := v.units(f.ts)
	return hwmediacodec.Packet{Data: data, PTS: pts, DTS: pts, Duration: v.units(f.dur), Keyframe: f.key}, nil
}

func hasSequenceHeader(obus []av1.OBU) bool {
	for _, o := range obus {
		if o.Type == av1.OBUSequenceHeader {
			return true
		}
	}
	return false
}

// Length is the presentation length of the track.
func (v *VideoTrack) Length() time.Duration { return v.length() }

// KeyframeAtOrBefore returns the index of the last keyframe whose time is
// not after t, or the first keyframe when t lies before it (-1 without
// any).
func (v *VideoTrack) KeyframeAtOrBefore(t time.Duration) int {
	i := sort.Search(len(v.frames), func(i int) bool { return v.TimeOf(v.frames[i].ts) > t })
	for k := i - 1; k >= 0; k-- {
		if v.frames[k].key {
			return k
		}
	}
	for k := i; k < len(v.frames); k++ {
		if v.frames[k].key {
			return k
		}
	}
	return -1
}

// Format describes the stream: hand it to hwmediacodec.NewAudioDecoder.
// Its Delay is the CodecDelay of the track (the OpusHead's pre-skip when
// the track has none).
func (a *AudioTrack) Format() hwmediacodec.AudioFormat {
	f := a.format
	f.Config = append([]byte(nil), f.Config...)
	return f
}

// Packet reads frame i as a decoder packet. Its PTS is in samples at
// 48 kHz on the presentation's time line: the block timestamp minus the
// codec delay, so the first packet starts before 0. Its Duration is the
// samples the packet decodes to, less the DiscardPadding of the last one.
func (a *AudioTrack) Packet(i int) (hwmediacodec.Packet, error) {
	data, err := a.read(i)
	if err != nil {
		return hwmediacodec.Packet{}, err
	}
	f := a.frames[i]
	p := hwmediacodec.Packet{Data: data, PTS: a.pts(f.ts), Keyframe: true}
	p.DTS = p.PTS
	n, err := opus.PacketSamples(data)
	if err == nil {
		p.Duration = int64(n)
	} else {
		p.Duration = codec.Rescale(codec.RescaleTrunc(f.dur, 1, a.d.scale), int64(time.Second), opus.SampleRate)
	}
	if f.discard > 0 && (err == nil || !f.given) {
		p.Duration = max(p.Duration-codec.Rescale(f.discard, int64(time.Second), opus.SampleRate), 0)
	}
	return p, nil
}

// pts converts a block timestamp to the presentation's samples at 48 kHz.
func (a *AudioTrack) pts(ticks int64) int64 {
	return codec.Rescale(codec.RescaleTrunc(ticks, 1, a.d.scale)-int64(min(a.codecDelay, math.MaxInt64/2)), int64(time.Second), opus.SampleRate)
}

// Length is the presentation length of the track, without the codec
// delay: the end of the samples of the last packet, which the block
// timestamps in milliseconds would only round to.
func (a *AudioTrack) Length() time.Duration {
	if len(a.frames) > 0 {
		if p, err := a.Packet(len(a.frames) - 1); err == nil {
			return max(time.Duration((p.PTS+p.Duration)*int64(time.Second)/opus.SampleRate), 0)
		}
	}
	return max(a.length()-time.Duration(min(a.codecDelay, math.MaxInt64/2)), 0)
}
