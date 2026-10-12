package webcodecs

import (
	"errors"
	"sort"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// paramSets remembers the latest VPS/SPS/PPS NAL units by id (as the
// MediaCodec and Intel VPL backends do). A keyframe that does not carry
// its parameter sets itself (streams demuxed from containers usually keep
// them out of band) is prefixed with the stored ones: without a
// description in its configuration a WebCodecs decoder reads Annex-B and
// finds the parameter sets nowhere else.
type paramSets struct {
	codec codec.Codec
	sets  [3]map[uint32][]byte // indexed by annexb.ParamVPS/ParamSPS/ParamPPS
	// lastSPS is the SPS NAL unit seen last; the decoder derives the codec
	// string and the picture size of its configuration from it.
	lastSPS []byte
}

func newParamSets(c codec.Codec) *paramSets {
	s := &paramSets{codec: c}
	for i := range s.sets {
		s.sets[i] = map[uint32][]byte{}
	}
	return s
}

// add stores a parameter-set NAL unit (without start code) and returns its
// kind.
func (s *paramSets) add(nalType int, nal []byte) (kind int, ok bool) {
	kind, id, ok := annexb.ParameterSetID(s.codec, nalType, nal)
	if !ok {
		return 0, false
	}
	s.sets[kind][id] = append([]byte(nil), nal...)
	if kind == annexb.ParamSPS {
		s.lastSPS = s.sets[kind][id]
	}
	return kind, true
}

// covers reports whether a packet that carries the given kinds of
// parameter sets is self-contained, so nothing needs to be prepended.
func (s *paramSets) covers(kinds [3]bool) bool {
	switch s.codec {
	case codec.H264:
		return kinds[annexb.ParamSPS] && kinds[annexb.ParamPPS]
	case codec.HEVC:
		return kinds[annexb.ParamVPS] && kinds[annexb.ParamSPS] && kinds[annexb.ParamPPS]
	}
	return true
}

// annexB returns every stored set as an Annex-B byte string (4-byte start
// codes), VPS first, then SPS, then PPS, each in ascending id order.
func (s *paramSets) annexB() []byte {
	var out []byte
	for kind := range s.sets {
		ids := make([]int, 0, len(s.sets[kind]))
		for id := range s.sets[kind] {
			ids = append(ids, int(id))
		}
		sort.Ints(ids)
		for _, id := range ids {
			out = annexb.AppendUnit(out, s.sets[kind][uint32(id)])
		}
	}
	return out
}

// packetInfo is what Send needs to know about an access unit.
type packetInfo struct {
	nals     int
	vcl      bool    // carries picture data
	keyframe bool    // carries a random access picture
	kinds    [3]bool // parameter-set kinds present
}

// inspect classifies the NAL units of an access unit and stores its
// parameter sets.
func (s *paramSets) inspect(data []byte) packetInfo {
	var info packetInfo
	for _, nal := range annexb.Split(data) {
		info.nals++
		t := annexb.NALUnitType(s.codec, nal)
		switch {
		case annexb.IsVCL(s.codec, t):
			info.vcl = true
			if annexb.IsKeyframe(s.codec, t) {
				info.keyframe = true
			}
		case annexb.IsParameterSet(s.codec, t):
			if kind, ok := s.add(t, nal); ok {
				info.kinds[kind] = true
			}
		}
	}
	return info
}

// addDescription stores the parameter sets of a decoder configuration
// record (avcC for H.264, hvcC for HEVC) and returns the size of the NAL
// unit length fields of the samples it describes. An encoder that was
// asked for Annex-B output has no such record; one that ignores the
// request returns it with its first chunk, and its chunks are then
// length-prefixed.
func (s *paramSets) addDescription(rec []byte) (lengthSize int, err error) {
	bad := errors.New("malformed decoder configuration record")
	// take cuts one 16-bit-length-prefixed NAL unit off the record.
	take := func() ([]byte, bool) {
		if len(rec) < 2 {
			return nil, false
		}
		n := int(rec[0])<<8 | int(rec[1])
		if len(rec) < 2+n {
			return nil, false
		}
		nal := rec[2 : 2+n]
		rec = rec[2+n:]
		return nal, true
	}
	store := func(nal []byte) {
		s.add(annexb.NALUnitType(s.codec, nal), nal)
	}
	switch s.codec {
	case codec.H264:
		// AVCDecoderConfigurationRecord: version, profile, compatibility,
		// level, 6 bits + lengthSizeMinusOne, 3 bits + SPS count, the SPS
		// units, PPS count, the PPS units.
		if len(rec) < 6 || rec[0] != 1 {
			return 0, bad
		}
		lengthSize = int(rec[4]&3) + 1
		count := int(rec[5] & 0x1f)
		rec = rec[6:]
		for i := 0; i < count; i++ {
			nal, ok := take()
			if !ok {
				return 0, bad
			}
			store(nal)
		}
		if len(rec) < 1 {
			return 0, bad
		}
		count = int(rec[0])
		rec = rec[1:]
		for i := 0; i < count; i++ {
			nal, ok := take()
			if !ok {
				return 0, bad
			}
			store(nal)
		}
		return lengthSize, nil
	case codec.HEVC:
		// HEVCDecoderConfigurationRecord: 21 bytes of profile, level and
		// format fields, then lengthSizeMinusOne in the low bits of byte
		// 21, the number of arrays, and per array a type byte, a NAL unit
		// count and the units.
		if len(rec) < 23 || rec[0] != 1 {
			return 0, bad
		}
		lengthSize = int(rec[21]&3) + 1
		arrays := int(rec[22])
		rec = rec[23:]
		for i := 0; i < arrays; i++ {
			if len(rec) < 3 {
				return 0, bad
			}
			count := int(rec[1])<<8 | int(rec[2])
			rec = rec[3:]
			for j := 0; j < count; j++ {
				nal, ok := take()
				if !ok {
					return 0, bad
				}
				store(nal)
			}
		}
		return lengthSize, nil
	}
	return 0, bad
}
