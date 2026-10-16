package webm

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// Element IDs (RFC 9559), with their marker bits as they appear in a file.
const (
	idEBML               = 0x1A45DFA3
	idEBMLVersion        = 0x4286
	idEBMLReadVersion    = 0x42F7
	idEBMLMaxIDLength    = 0x42F2
	idEBMLMaxSizeLength  = 0x42F3
	idDocType            = 0x4282
	idDocTypeVersion     = 0x4287
	idDocTypeReadVersion = 0x4285

	idSegment     = 0x18538067
	idSeekHead    = 0x114D9B74
	idSeek        = 0x4DBB
	idSeekID      = 0x53AB
	idSeekPos     = 0x53AC
	idInfo        = 0x1549A966
	idTimestScale = 0x2AD7B1
	idDuration    = 0x4489
	idMuxingApp   = 0x4D80
	idWritingApp  = 0x5741
	idTracks      = 0x1654AE6B
	idCluster     = 0x1F43B675
	idCues        = 0x1C53BB6B
	idTags        = 0x1254C367
	idChapters    = 0x1043A770
	idAttachments = 0x1941A469
	idVoid        = 0xEC
	idCRC32       = 0xBF

	idTrackEntry      = 0xAE
	idTrackNumber     = 0xD7
	idTrackUID        = 0x73C5
	idTrackType       = 0x83
	idFlagLacing      = 0x9C
	idCodecID         = 0x86
	idCodecPrivate    = 0x63A2
	idCodecDelay      = 0x56AA
	idSeekPreRoll     = 0x56BB
	idLanguage        = 0x22B59C
	idDefaultDuration = 0x23E383
	idContentEncs     = 0x6D80
	idVideo           = 0xE0
	idPixelWidth      = 0xB0
	idPixelHeight     = 0xBA
	idAudio           = 0xE1
	idSamplingFreq    = 0xB5
	idChannels        = 0x9F

	idTimestamp      = 0xE7
	idSimpleBlock    = 0xA3
	idBlockGroup     = 0xA0
	idBlock          = 0xA1
	idBlockDuration  = 0x9B
	idReferenceBlock = 0xFB
	idDiscardPadding = 0x75A2

	idCuePoint           = 0xBB
	idCueTime            = 0xB3
	idCueTrackPositions  = 0xB7
	idCueTrack           = 0xF7
	idCueClusterPosition = 0xF1
)

// Track types of TrackType.
const (
	trackVideo = 1
	trackAudio = 2
)

// unknownSize marks an element whose size field is all ones: a live
// recording (a browser's MediaRecorder) writes its Segment and Clusters
// that way, and they end where an element that cannot be their child
// begins.
const unknownSize = -1

var errInvalid = errors.New("webm: invalid file")

// isTopLevel reports whether id is a child of the Segment, which ends a
// Cluster of unknown size.
func isTopLevel(id uint32) bool {
	switch id {
	case idSeekHead, idInfo, idTracks, idCluster, idCues, idTags, idChapters, idAttachments, idEBML, idSegment:
		return true
	}
	return false
}

// scanner reads the element tree sequentially, skipping payloads with a
// seek when they are longer than what is buffered. Positions count from
// the first byte of the file.
type scanner struct {
	rs    io.ReadSeeker
	start int64 // position of the file in rs
	end   int64 // file size
	br    *bufio.Reader
	pos   int64
}

func newScanner(rs io.ReadSeeker, start, end int64) *scanner {
	return &scanner{rs: rs, start: start, end: end, br: bufio.NewReaderSize(rs, 64<<10)}
}

func (s *scanner) seek(pos int64) error {
	if pos >= s.pos && pos-s.pos <= int64(s.br.Buffered()) {
		_, err := s.br.Discard(int(pos - s.pos))
		s.pos = pos
		return err
	}
	if _, err := s.rs.Seek(s.start+pos, io.SeekStart); err != nil {
		return err
	}
	s.br.Reset(s.rs)
	s.pos = pos
	return nil
}

func (s *scanner) readByte() (byte, error) {
	b, err := s.br.ReadByte()
	if err == nil {
		s.pos++
	}
	return b, err
}

// read returns the next n bytes. n must have been checked against the
// element it lies in; it is also checked against the file here.
func (s *scanner) read(n int64) ([]byte, error) {
	if n < 0 || n > s.end-s.pos {
		return nil, io.ErrUnexpectedEOF
	}
	b := make([]byte, n)
	_, err := io.ReadFull(s.br, b)
	s.pos += int64(len(b))
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return b, err
}

// vint reads a variable-length integer. With id set the marker bit is
// kept (an element ID); otherwise it is removed and an all-ones value
// returns unknown.
func (s *scanner) vint(id bool) (v uint64, n int, unknown bool, err error) {
	first, err := s.readByte()
	if err != nil {
		return 0, 0, false, err
	}
	n = 1
	for first&(0x80>>(n-1)) == 0 {
		n++
		if n > 8 || (id && n > 4) {
			return 0, 0, false, fmt.Errorf("%w: variable-length integer longer than allowed at %d", errInvalid, s.pos-1)
		}
	}
	v = uint64(first)
	if !id {
		v &= uint64(0xFF >> n)
	}
	allOnes := v == uint64(0xFF>>n)
	for i := 1; i < n; i++ {
		b, err := s.readByte()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return 0, 0, false, err
		}
		v = v<<8 | uint64(b)
		allOnes = allOnes && b == 0xFF
	}
	return v, n, !id && allOnes, nil
}

// header reads an element header and returns its ID, its payload size
// (unknownSize for an all-ones size) and the header length.
func (s *scanner) header() (id uint32, size int64, n int, err error) {
	v, in, _, err := s.vint(true)
	if err != nil {
		return 0, 0, 0, err
	}
	sz, sn, unknown, err := s.vint(false)
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, 0, 0, err
	}
	if unknown {
		return uint32(v), unknownSize, in + sn, nil
	}
	if sz > math.MaxInt64 {
		return 0, 0, 0, fmt.Errorf("%w: element size %d", errInvalid, sz)
	}
	return uint32(v), int64(sz), in + sn, nil
}

// element is one parsed child of a master element read into memory.
type element struct {
	id   uint32
	data []byte
}

// children splits the payload of a master element held in memory.
func children(b []byte) ([]element, error) {
	var out []element
	for len(b) > 0 {
		id, n, ok := readVint(b, true)
		if !ok {
			return nil, fmt.Errorf("%w: bad element ID", errInvalid)
		}
		b = b[n:]
		size, n, ok := readVint(b, false)
		if !ok || size > uint64(len(b)-n) {
			return nil, fmt.Errorf("%w: element 0x%X runs past its parent", errInvalid, id)
		}
		b = b[n:]
		out = append(out, element{id: uint32(id), data: b[:size]})
		b = b[size:]
	}
	return out, nil
}

// readVint parses a variable-length integer at the start of b.
func readVint(b []byte, id bool) (v uint64, n int, ok bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false
	}
	n = 1
	for b[0]&(0x80>>(n-1)) == 0 {
		n++
	}
	if len(b) < n || (id && n > 4) {
		return 0, 0, false
	}
	v = uint64(b[0])
	if !id {
		v &= uint64(0xFF >> n)
	}
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v, n, true
}

func (e element) uint() (uint64, error) {
	if len(e.data) > 8 {
		return 0, fmt.Errorf("%w: unsigned integer element 0x%X of %d bytes", errInvalid, e.id, len(e.data))
	}
	var v uint64
	for _, c := range e.data {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

func (e element) int() (int64, error) {
	v, err := e.uint()
	if err != nil || len(e.data) == 0 {
		return 0, err
	}
	shift := 64 - 8*uint(len(e.data))
	return int64(v<<shift) >> shift, nil
}

func (e element) float() (float64, error) {
	switch len(e.data) {
	case 0:
		return 0, nil
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(e.data))), nil
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(e.data)), nil
	}
	return 0, fmt.Errorf("%w: float element 0x%X of %d bytes", errInvalid, e.id, len(e.data))
}

func (e element) string() string {
	b := e.data
	for i, c := range b {
		if c == 0 {
			b = b[:i]
			break
		}
	}
	return string(b)
}

// Writing.

func appendID(b []byte, id uint32) []byte {
	switch {
	case id >= 1<<24:
		return append(b, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	case id >= 1<<16:
		return append(b, byte(id>>16), byte(id>>8), byte(id))
	case id >= 1<<8:
		return append(b, byte(id>>8), byte(id))
	}
	return append(b, byte(id))
}

// appendSize appends n as a size field of the shortest length.
func appendSize(b []byte, n uint64) []byte {
	l := 1
	for l < 8 && n >= 1<<(7*l)-1 {
		l++
	}
	return appendSizeLen(b, n, l)
}

// appendSizeLen appends n as a size field of exactly l bytes.
func appendSizeLen(b []byte, n uint64, l int) []byte {
	v := n | 1<<(7*l)
	for i := l - 1; i >= 0; i-- {
		b = append(b, byte(v>>(8*i)))
	}
	return b
}

func appendElement(b []byte, id uint32, payload []byte) []byte {
	b = appendID(b, id)
	b = appendSize(b, uint64(len(payload)))
	return append(b, payload...)
}

func appendUint(b []byte, id uint32, v uint64) []byte {
	n := 1
	for n < 8 && v >= 1<<(8*n) {
		n++
	}
	b = appendID(b, id)
	b = appendSize(b, uint64(n))
	for i := n - 1; i >= 0; i-- {
		b = append(b, byte(v>>(8*i)))
	}
	return b
}

func appendInt(b []byte, id uint32, v int64) []byte {
	n := 1
	for n < 8 && (v < -(1<<(8*n-1)) || v >= 1<<(8*n-1)) {
		n++
	}
	b = appendID(b, id)
	b = appendSize(b, uint64(n))
	for i := n - 1; i >= 0; i-- {
		b = append(b, byte(v>>(8*i)))
	}
	return b
}

func appendFloat(b []byte, id uint32, v float64) []byte {
	b = appendID(b, id)
	b = appendSize(b, 8)
	return binary.BigEndian.AppendUint64(b, math.Float64bits(v))
}

func appendString(b []byte, id uint32, s string) []byte {
	return appendElement(b, id, []byte(s))
}

// appendVoid appends a Void element that occupies exactly n bytes (n >= 2).
func appendVoid(b []byte, n int) []byte {
	b = append(b, idVoid)
	if n-2 < 127 {
		b = appendSizeLen(b, uint64(n-2), 1)
		return append(b, make([]byte, n-2)...)
	}
	b = appendSizeLen(b, uint64(n-9), 8)
	return append(b, make([]byte, n-9)...)
}
