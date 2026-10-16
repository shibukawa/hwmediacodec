package mp4

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"time"

	mp4ff "github.com/Eyevinn/mp4ff/mp4"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// MaxSegmentBytes bounds the sample data a Segmenter holds back for the
// segment it is building. A segment closes at a keyframe, so a stream
// without keyframes would otherwise grow in memory without end; the time
// bound in WritePacket (four times the target) catches that when the
// timestamps advance, and this catches it when they barely do. The
// default of 256 MiB is far above any real segment (a 50 Mbit/s stream
// fills it in 40 s).
var MaxSegmentBytes = 256 << 20

// Segment is one fragmented-MP4 media segment (styp + moof + mdat) that
// starts with a keyframe.
type Segment struct {
	Seq      int
	Data     []byte
	Start    time.Duration // presentation time of the first sample
	Duration time.Duration
}

// Segmenter turns a live stream of encoder packets into CMAF/fMP4
// segments for HLS (or DASH, or a MediaSource buffer). The init segment
// (ftyp + moov) is produced once the first keyframe has delivered the
// parameter sets; a media segment is closed at the first keyframe after
// Target has elapsed, so the encoder's keyframe interval decides the real
// segment length. B-frames are not supported here: every sample's PTS
// must equal its DTS.
type Segmenter struct {
	ps        paramSets
	timeScale uint32
	target    time.Duration
	onInit    func([]byte) error
	onSegment func(Segment) error

	initDone bool
	seq      int
	base     int64 // DTS of the first sample, so decode times start at 0
	pending  []pendingSample
	// pendingBytes is the sample data in pending, checked against
	// MaxSegmentBytes.
	pendingBytes int
	lastDur      int64
	closed       bool
}

type pendingSample struct {
	data []byte
	dts  int64
	pts  int64
	sync bool
}

// NewSegmenter creates a segmenter for H.264 or HEVC packets whose PTS
// and DTS are in timeScale units. onInit receives the init segment once,
// onSegment every media segment in order.
//
// Segments close only at keyframes, so the encoder's keyframe interval
// must not exceed target by much: when the samples held back span more
// than four times target without a keyframe arriving, or more than
// MaxSegmentBytes, WritePacket returns an error instead of holding on to
// the whole stream (an encoder with its keyframe interval set to 0, or a
// stream that lost its IDR pictures, would otherwise exhaust memory).
func NewSegmenter(c hwmediacodec.Codec, timeScale uint32, target time.Duration, onInit func([]byte) error, onSegment func(Segment) error) (*Segmenter, error) {
	if c != hwmediacodec.H264 && c != hwmediacodec.HEVC {
		return nil, fmt.Errorf("mp4: cannot segment %s", c)
	}
	if timeScale == 0 || target <= 0 {
		return nil, errors.New("mp4: segmenter needs a time scale and a target duration")
	}
	return &Segmenter{ps: paramSets{codec: c}, timeScale: timeScale, target: target, onInit: onInit, onSegment: onSegment}, nil
}

// WritePacket adds one Annex-B access unit.
func (s *Segmenter) WritePacket(p hwmediacodec.Packet) error {
	if s.closed {
		return errors.New("mp4: segmenter is closed")
	}
	data, sync, err := s.ps.sample(p)
	if err != nil {
		return err
	}
	if data == nil {
		return nil
	}
	if p.PTS != p.DTS {
		return errors.New("mp4: the segmenter needs PTS == DTS (encode without B-frames)")
	}
	if !s.initDone {
		if !sync {
			return nil // wait for the first keyframe
		}
		if err := s.writeInit(); err != nil {
			return err
		}
		s.base = p.DTS
	}
	if sync && len(s.pending) > 0 {
		first := s.pending[0]
		if s.toDuration(p.PTS-first.pts) >= s.target {
			if err := s.emit(p.DTS); err != nil {
				return err
			}
		}
	}
	if len(s.pending) > 0 && p.DTS <= s.pending[len(s.pending)-1].dts {
		return fmt.Errorf("mp4: decode time %d does not increase", p.DTS)
	}
	// A segment that cannot close is a stream without keyframes: report it
	// instead of buffering until memory runs out (see NewSegmenter).
	if len(s.pending) > 0 {
		limit := time.Duration(math.MaxInt64)
		if s.target <= limit/4 {
			limit = 4 * s.target
		}
		if held := s.toDuration(p.PTS - s.pending[0].pts); held > limit {
			return fmt.Errorf("mp4: segmenter: no keyframe for %v, more than four times the %v target; the encoder must emit keyframes at least that often", held, s.target)
		}
		if len(data) > MaxSegmentBytes-s.pendingBytes {
			return fmt.Errorf("mp4: segmenter: %d bytes without a keyframe exceed MaxSegmentBytes (%d)", s.pendingBytes+len(data), MaxSegmentBytes)
		}
	}
	s.pending = append(s.pending, pendingSample{data: data, dts: p.DTS, pts: p.PTS, sync: sync})
	s.pendingBytes += len(data)
	return nil
}

func (s *Segmenter) toDuration(ts int64) time.Duration {
	return time.Duration(codec.RescaleTrunc(ts, int64(s.timeScale), int64(time.Second)))
}

func (s *Segmenter) writeInit() error {
	init := mp4ff.CreateEmptyInit()
	trak := init.AddEmptyTrack(s.timeScale, "video", "und")
	if err := s.ps.setDescriptor(trak); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		return err
	}
	s.initDone = true
	if s.onInit != nil {
		return s.onInit(buf.Bytes())
	}
	return nil
}

// emit closes the pending samples into a segment. nextDTS is the decode
// time of the sample that follows (it gives the last sample its duration)
// or -1 at the end of the stream.
func (s *Segmenter) emit(nextDTS int64) error {
	n := len(s.pending)
	if n == 0 {
		return nil
	}
	s.seq++
	frag, err := mp4ff.CreateFragment(uint32(s.seq), 1)
	if err != nil {
		return err
	}
	var total int64
	for i, ps := range s.pending {
		var dur int64
		switch {
		case i+1 < n:
			dur = s.pending[i+1].dts - ps.dts
		case nextDTS >= 0:
			dur = nextDTS - ps.dts
		default:
			dur = s.lastDur
		}
		if dur <= 0 {
			dur = 1
		}
		if dur > math.MaxUint32 {
			return errors.New("mp4: segment sample duration exceeds the 32-bit field")
		}
		s.lastDur = dur
		total += dur
		flags := uint32(mp4ff.NonSyncSampleFlags)
		if ps.sync {
			flags = mp4ff.SyncSampleFlags
		}
		frag.AddFullSample(mp4ff.FullSample{
			Sample:     mp4ff.Sample{Flags: flags, Dur: uint32(dur), Size: uint32(len(ps.data))},
			DecodeTime: uint64(ps.dts - s.base),
			Data:       ps.data,
		})
	}
	seg := mp4ff.NewMediaSegment()
	seg.AddFragment(frag)
	var buf bytes.Buffer
	if err := seg.Encode(&buf); err != nil {
		return err
	}
	out := Segment{Seq: s.seq, Data: buf.Bytes(), Start: s.toDuration(s.pending[0].pts - s.base), Duration: s.toDuration(total)}
	// pending's backing array survives between segments: clear pointers to
	// encoded packets so a short later segment does not retain old frames.
	clear(s.pending)
	s.pending = s.pending[:0]
	s.pendingBytes = 0
	if s.onSegment != nil {
		return s.onSegment(out)
	}
	return nil
}

// Close emits the last, possibly short, segment.
func (s *Segmenter) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.emit(-1)
}
