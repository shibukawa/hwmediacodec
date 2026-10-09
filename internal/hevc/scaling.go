package hevc

// ScalingList holds scaling_list_data() (7.3.4). Every list is kept in the
// order its coefficients are coded (the up-right diagonal scan), which is
// also the order VAIQMatrixBufferHEVC expects.
//
// The 32x32 lists exist for matrixId 0 (intra) and 3 (inter) only; they are
// stored at index 0 and 1.
type ScalingList struct {
	L4   [6][16]uint8
	L8   [6][64]uint8
	L16  [6][64]uint8
	L32  [2][64]uint8
	DC16 [6]uint8
	DC32 [2]uint8
}

// Default 8x8 (and larger) lists of Table 7-6, in coded order.
var (
	defaultScalingIntra = [64]uint8{
		16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 17, 16, 17, 16, 17, 18,
		17, 18, 18, 17, 18, 21, 19, 20, 21, 20, 19, 21, 24, 22, 22, 24,
		24, 22, 22, 24, 25, 25, 27, 30, 27, 25, 25, 29, 31, 35, 35, 31,
		29, 36, 41, 44, 41, 36, 47, 54, 54, 47, 65, 70, 65, 88, 88, 115,
	}
	defaultScalingInter = [64]uint8{
		16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 17, 17, 17, 17, 17, 18,
		18, 18, 18, 18, 18, 20, 20, 20, 20, 20, 20, 20, 24, 24, 24, 24,
		24, 24, 24, 24, 25, 25, 25, 25, 25, 25, 25, 28, 28, 28, 28, 28,
		28, 33, 33, 33, 33, 33, 41, 41, 41, 41, 54, 54, 54, 71, 71, 91,
	}
)

// DefaultScalingList returns the lists used when scaling_list_enabled_flag
// is set and no scaling_list_data() is present (Tables 7-5 and 7-6).
func DefaultScalingList() ScalingList {
	var sl ScalingList
	for m := 0; m < 6; m++ {
		sl.setDefault(0, m)
		sl.setDefault(1, m)
		sl.setDefault(2, m)
	}
	sl.setDefault(3, 0)
	sl.setDefault(3, 3)
	return sl
}

// list returns the coefficients and the DC entry (nil below 16x16) of one
// list. For sizeID 3, matrixID must be 0 or 3.
func (sl *ScalingList) list(sizeID, matrixID int) (coef []uint8, dc *uint8) {
	switch sizeID {
	case 0:
		return sl.L4[matrixID][:], nil
	case 1:
		return sl.L8[matrixID][:], nil
	case 2:
		return sl.L16[matrixID][:], &sl.DC16[matrixID]
	}
	return sl.L32[matrixID/3][:], &sl.DC32[matrixID/3]
}

func (sl *ScalingList) setDefault(sizeID, matrixID int) {
	coef, dc := sl.list(sizeID, matrixID)
	switch {
	case sizeID == 0:
		for i := range coef {
			coef[i] = 16
		}
	case matrixID < 3:
		copy(coef, defaultScalingIntra[:])
	default:
		copy(coef, defaultScalingInter[:])
	}
	if dc != nil {
		*dc = 16
	}
}

// scalingListData reads scaling_list_data() for 4:2:0 and 4:2:2 streams.
func (r *reader) scalingListData(sl *ScalingList) {
	for sizeID := 0; sizeID < 4; sizeID++ {
		step := 1
		if sizeID == 3 {
			step = 3
		}
		for m := 0; m < 6; m += step {
			coef, dc := sl.list(sizeID, m)
			if !r.flag() { // scaling_list_pred_mode_flag
				delta := int(r.ue()) * step // scaling_list_pred_matrix_id_delta
				if r.err != nil {
					return
				}
				if delta > m {
					r.fail("scaling_list_pred_matrix_id_delta out of range")
					return
				}
				if delta == 0 {
					sl.setDefault(sizeID, m)
				} else {
					ref, refDC := sl.list(sizeID, m-delta)
					copy(coef, ref)
					if dc != nil {
						*dc = *refDC
					}
				}
				continue
			}
			next := int32(8)
			if dc != nil {
				next = r.seRange("scaling_list_dc_coef_minus8", -7, 247) + 8
				*dc = uint8(next)
			}
			for i := range coef {
				next = (next + r.seRange("scaling_list_delta_coef", -128, 127) + 256) % 256
				coef[i] = uint8(next)
			}
			if r.err != nil {
				return
			}
		}
	}
}
