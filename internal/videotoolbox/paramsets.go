//go:build darwin

package videotoolbox

import (
	"bytes"
	"runtime"
	"sort"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/bitstream"
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
	kind, id, ok := parameterSetID(s.codec, nalType, nal)
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

// parameterSetID returns the kind index (0 VPS, 1 SPS, 2 PPS) and id of a
// parameter-set NAL unit.
func parameterSetID(c codec.Codec, nalType int, nal []byte) (kind int, id uint32, ok bool) {
	switch c {
	case codec.H264:
		if len(nal) < 2 {
			return 0, 0, false
		}
		r := bitstream.NewRBSP(nal[1:])
		switch nalType {
		case annexb.H264NALSPS:
			if err := r.SkipBits(24); err != nil { // profile_idc, constraint flags, level_idc
				return 0, 0, false
			}
			id, err := r.ReadUE()
			if err != nil {
				return 0, 0, false
			}
			return 1, id, true
		case annexb.H264NALPPS:
			id, err := r.ReadUE()
			if err != nil {
				return 0, 0, false
			}
			return 2, id, true
		}
	case codec.HEVC:
		if len(nal) < 3 {
			return 0, 0, false
		}
		r := bitstream.NewRBSP(nal[2:])
		switch nalType {
		case annexb.HEVCNALVPS:
			id, err := r.ReadBits(4)
			if err != nil {
				return 0, 0, false
			}
			return 0, uint32(id), true
		case annexb.HEVCNALSPS:
			if err := r.SkipBits(4); err != nil { // sps_video_parameter_set_id
				return 0, 0, false
			}
			maxSubLayersMinus1, err := r.ReadBits(3)
			if err != nil {
				return 0, 0, false
			}
			if err := r.SkipBits(1); err != nil { // temporal_id_nesting_flag
				return 0, 0, false
			}
			if err := skipProfileTierLevel(r, int(maxSubLayersMinus1)); err != nil {
				return 0, 0, false
			}
			id, err := r.ReadUE()
			if err != nil {
				return 0, 0, false
			}
			return 1, id, true
		case annexb.HEVCNALPPS:
			id, err := r.ReadUE()
			if err != nil {
				return 0, 0, false
			}
			return 2, id, true
		}
	}
	return 0, 0, false
}

// skipProfileTierLevel skips profile_tier_level(1, maxSubLayersMinus1).
func skipProfileTierLevel(r *bitstream.Reader, maxSubLayersMinus1 int) error {
	// general_profile_space(2) tier(1) idc(5) compat(32) flags(48) = 88 bits,
	// then general_level_idc(8).
	if err := r.SkipBits(88 + 8); err != nil {
		return err
	}
	profilePresent := make([]bool, maxSubLayersMinus1)
	levelPresent := make([]bool, maxSubLayersMinus1)
	for i := 0; i < maxSubLayersMinus1; i++ {
		p, err := r.ReadBit()
		if err != nil {
			return err
		}
		l, err := r.ReadBit()
		if err != nil {
			return err
		}
		profilePresent[i] = p == 1
		levelPresent[i] = l == 1
	}
	if maxSubLayersMinus1 > 0 {
		if err := r.SkipBits(2 * (8 - maxSubLayersMinus1)); err != nil {
			return err
		}
	}
	for i := 0; i < maxSubLayersMinus1; i++ {
		if profilePresent[i] {
			if err := r.SkipBits(88); err != nil {
				return err
			}
		}
		if levelPresent[i] {
			if err := r.SkipBits(8); err != nil {
				return err
			}
		}
	}
	return nil
}
