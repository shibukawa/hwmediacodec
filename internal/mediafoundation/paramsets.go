package mediafoundation

import (
	"sort"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// paramSets remembers the latest VPS/SPS/PPS NAL units by id. After a flush
// the Media Foundation decoder is restarted, so the next keyframe is prefixed
// with the stored sets when it does not carry them itself (streams demuxed
// from containers usually carry parameter sets out of band).
type paramSets struct {
	codec codec.Codec
	sets  [3]map[uint32][]byte // indexed by annexb.ParamVPS/ParamSPS/ParamPPS
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
	return kind, true
}

// covers reports whether a packet that carries the given kinds of parameter
// sets is self-contained, so nothing needs to be prepended.
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
			out = append(out, 0, 0, 0, 1)
			out = append(out, s.sets[kind][uint32(id)]...)
		}
	}
	return out
}
