//go:build darwin

package videotoolbox

import (
	"bytes"
	"runtime"
	"sort"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/videotoolbox/sys"
)

// paramSetStore keeps the latest VPS/SPS/PPS NAL units by id so that a
// CMVideoFormatDescription can be built that contains every parameter set
// the stream may reference.
type paramSetStore struct {
	codec codec.Codec
	sets  [3]map[uint32][]byte // 0=VPS 1=SPS 2=PPS
	dirty bool
}

func newParamSetStore(c codec.Codec) *paramSetStore {
	s := &paramSetStore{codec: c}
	for i := range s.sets {
		s.sets[i] = map[uint32][]byte{}
	}
	return s
}

// add records a parameter-set NAL unit and reports whether anything changed.
func (s *paramSetStore) add(nalType int, nal []byte) bool {
	kind, id, ok := annexb.ParameterSetID(s.codec, nalType, nal)
	if !ok {
		return false
	}
	if old, exists := s.sets[kind][id]; exists && bytes.Equal(old, nal) {
		return false
	}
	s.sets[kind][id] = append([]byte(nil), nal...)
	s.dirty = true
	return true
}

func (s *paramSetStore) complete() bool {
	switch s.codec {
	case codec.H264:
		return len(s.sets[1]) > 0 && len(s.sets[2]) > 0
	case codec.HEVC:
		return len(s.sets[0]) > 0 && len(s.sets[1]) > 0 && len(s.sets[2]) > 0
	}
	return false
}

func (s *paramSetStore) ordered() [][]byte {
	var out [][]byte
	for kind := range s.sets {
		ids := make([]int, 0, len(s.sets[kind]))
		for id := range s.sets[kind] {
			ids = append(ids, int(id))
		}
		sort.Ints(ids)
		for _, id := range ids {
			out = append(out, s.sets[kind][uint32(id)])
		}
	}
	return out
}

// formatDescription builds a CMVideoFormatDescription from the stored sets.
// The caller owns the returned reference.
func (s *paramSetStore) formatDescription() (uintptr, int32) {
	sets := s.ordered()
	ptrs := make([]uintptr, len(sets))
	sizes := make([]uintptr, len(sets))
	for i, ps := range sets {
		ptrs[i] = uintptr(unsafe.Pointer(&ps[0]))
		sizes[i] = uintptr(len(ps))
	}
	var fd uintptr
	var status int32
	switch s.codec {
	case codec.H264:
		status = sys.CMVideoFormatDescriptionCreateFromH264ParameterSets(0, uintptr(len(sets)), &ptrs[0], &sizes[0], 4, &fd)
	case codec.HEVC:
		status = sys.CMVideoFormatDescriptionCreateFromHEVCParameterSets(0, uintptr(len(sets)), &ptrs[0], &sizes[0], 4, 0, &fd)
	}
	runtime.KeepAlive(sets)
	s.dirty = false
	return fd, status
}
