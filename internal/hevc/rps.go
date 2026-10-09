package hevc

import "github.com/shibukawa/hwmediacodec/internal/bitstream"

// maxRPSPics bounds the pictures of one short-term reference picture set
// (sps_max_dec_pic_buffering_minus1 is at most 15).
const maxRPSPics = 16

// ShortTermRPS is one st_ref_pic_set() (7.3.7) with the variables derived
// from it (7.4.8): the pictures before the current one in output order
// (S0, closest first, negative deltas) and after it (S1, closest first,
// positive deltas).
type ShortTermRPS struct {
	// InterRPSPrediction is inter_ref_pic_set_prediction_flag; the syntax
	// elements below are only meaningful when it is set.
	InterRPSPrediction bool
	DeltaIdxMinus1     uint32
	DeltaRPSSign       bool
	AbsDeltaRPSMinus1  uint32
	UsedByCurrPicFlag  []bool
	UseDeltaFlag       []bool

	NumNegativePics int
	NumPositivePics int
	DeltaPocS0      [maxRPSPics]int32
	DeltaPocS1      [maxRPSPics]int32
	UsedByCurrPicS0 [maxRPSPics]bool
	UsedByCurrPicS1 [maxRPSPics]bool
}

// NumDeltaPocs is NumNegativePics + NumPositivePics.
func (s *ShortTermRPS) NumDeltaPocs() int { return s.NumNegativePics + s.NumPositivePics }

// NumUsedByCurr counts the pictures the current picture may reference.
func (s *ShortTermRPS) NumUsedByCurr() int {
	n := 0
	for i := 0; i < s.NumNegativePics; i++ {
		if s.UsedByCurrPicS0[i] {
			n++
		}
	}
	for i := 0; i < s.NumPositivePics; i++ {
		if s.UsedByCurrPicS1[i] {
			n++
		}
	}
	return n
}

// shortTermRPS reads st_ref_pic_set(idx). sets holds the sets of the SPS
// parsed so far (all of them when the set is coded in a slice header, where
// idx equals num_short_term_ref_pic_sets).
func (r *reader) shortTermRPS(idx int, sets []ShortTermRPS, inSlice bool) ShortTermRPS {
	var s ShortTermRPS
	if idx != 0 {
		s.InterRPSPrediction = r.flag()
	}
	if !s.InterRPSPrediction {
		neg := int(r.ueMax("num_negative_pics", maxRPSPics))
		pos := int(r.ueMax("num_positive_pics", maxRPSPics))
		if r.err != nil {
			return s
		}
		if neg+pos > maxRPSPics {
			r.fail("short-term reference picture set holds %d pictures", neg+pos)
			return s
		}
		s.NumNegativePics, s.NumPositivePics = neg, pos
		poc := int32(0)
		for i := 0; i < neg; i++ {
			poc -= int32(r.ueMax("delta_poc_s0_minus1", 1<<15-1)) + 1
			s.DeltaPocS0[i] = poc
			s.UsedByCurrPicS0[i] = r.flag()
		}
		poc = 0
		for i := 0; i < pos; i++ {
			poc += int32(r.ueMax("delta_poc_s1_minus1", 1<<15-1)) + 1
			s.DeltaPocS1[i] = poc
			s.UsedByCurrPicS1[i] = r.flag()
		}
		return s
	}

	if inSlice {
		s.DeltaIdxMinus1 = r.ueMax("delta_idx_minus1", uint32(idx-1))
	}
	s.DeltaRPSSign = r.flag()
	s.AbsDeltaRPSMinus1 = r.ueMax("abs_delta_rps_minus1", 1<<15-1)
	if r.err != nil {
		return s
	}
	ref := &sets[idx-int(s.DeltaIdxMinus1)-1]
	n := ref.NumDeltaPocs()
	s.UsedByCurrPicFlag = make([]bool, n+1)
	s.UseDeltaFlag = make([]bool, n+1)
	for j := 0; j <= n; j++ {
		s.UsedByCurrPicFlag[j] = r.flag()
		s.UseDeltaFlag[j] = true
		if !s.UsedByCurrPicFlag[j] {
			s.UseDeltaFlag[j] = r.flag()
		}
	}
	if r.err != nil {
		return s
	}
	if !s.derive(ref) {
		r.fail("predicted short-term reference picture set is too large")
	}
	return s
}

// derive computes the S0 and S1 lists of an inter-predicted set from its
// reference set (equations 7-61 and 7-62). It reports false when the result
// does not fit.
func (s *ShortTermRPS) derive(ref *ShortTermRPS) bool {
	deltaRPS := int32(s.AbsDeltaRPSMinus1) + 1
	if s.DeltaRPSSign {
		deltaRPS = -deltaRPS
	}
	refNeg, refPos := ref.NumNegativePics, ref.NumPositivePics
	n := refNeg + refPos

	i := 0
	add0 := func(dPoc int32, j int) {
		if i < maxRPSPics {
			s.DeltaPocS0[i] = dPoc
			s.UsedByCurrPicS0[i] = s.UsedByCurrPicFlag[j]
		}
		i++
	}
	for j := refPos - 1; j >= 0; j-- {
		if dPoc := ref.DeltaPocS1[j] + deltaRPS; dPoc < 0 && s.UseDeltaFlag[refNeg+j] {
			add0(dPoc, refNeg+j)
		}
	}
	if deltaRPS < 0 && s.UseDeltaFlag[n] {
		add0(deltaRPS, n)
	}
	for j := 0; j < refNeg; j++ {
		if dPoc := ref.DeltaPocS0[j] + deltaRPS; dPoc < 0 && s.UseDeltaFlag[j] {
			add0(dPoc, j)
		}
	}
	s.NumNegativePics = i

	k := 0
	add1 := func(dPoc int32, j int) {
		if k < maxRPSPics {
			s.DeltaPocS1[k] = dPoc
			s.UsedByCurrPicS1[k] = s.UsedByCurrPicFlag[j]
		}
		k++
	}
	for j := refNeg - 1; j >= 0; j-- {
		if dPoc := ref.DeltaPocS0[j] + deltaRPS; dPoc > 0 && s.UseDeltaFlag[j] {
			add1(dPoc, j)
		}
	}
	if deltaRPS > 0 && s.UseDeltaFlag[n] {
		add1(deltaRPS, n)
	}
	for j := 0; j < refPos; j++ {
		if dPoc := ref.DeltaPocS1[j] + deltaRPS; dPoc > 0 && s.UseDeltaFlag[refNeg+j] {
			add1(dPoc, refNeg+j)
		}
	}
	s.NumPositivePics = k

	if i+k > maxRPSPics {
		s.NumNegativePics, s.NumPositivePics = 0, 0
		return false
	}
	return true
}

// write emits st_ref_pic_set(idx) without inter RPS prediction.
func (s *ShortTermRPS) write(w *bitstream.Writer, idx int) {
	if idx != 0 {
		w.WriteFlag(false) // inter_ref_pic_set_prediction_flag
	}
	w.WriteUE(uint32(s.NumNegativePics))
	w.WriteUE(uint32(s.NumPositivePics))
	prev := int32(0)
	for i := 0; i < s.NumNegativePics; i++ {
		w.WriteUE(uint32(prev - s.DeltaPocS0[i] - 1))
		w.WriteFlag(s.UsedByCurrPicS0[i])
		prev = s.DeltaPocS0[i]
	}
	prev = 0
	for i := 0; i < s.NumPositivePics; i++ {
		w.WriteUE(uint32(s.DeltaPocS1[i] - prev - 1))
		w.WriteFlag(s.UsedByCurrPicS1[i])
		prev = s.DeltaPocS1[i]
	}
}
