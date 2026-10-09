package h264

import "github.com/shibukawa/hwmediacodec/internal/bitstream"

// Zigzag scan orders (Rec. ITU-T H.264 8.5.6 and 8.5.7, frame scan): the
// position in raster order of the i-th coefficient of the scan.
var (
	zigzag4x4 = [16]int{0, 1, 4, 8, 5, 2, 3, 6, 9, 12, 13, 10, 7, 11, 14, 15}
	zigzag8x8 = [64]int{
		0, 1, 8, 16, 9, 2, 3, 10, 17, 24, 32, 25, 18, 11, 4, 5,
		12, 19, 26, 33, 40, 48, 41, 34, 27, 20, 13, 6, 7, 14, 21, 28,
		35, 42, 49, 56, 57, 50, 43, 36, 29, 22, 15, 23, 30, 37, 44, 51,
		58, 59, 52, 45, 38, 31, 39, 46, 53, 60, 61, 54, 47, 55, 62, 63,
	}
)

// Default scaling lists from Tables 7-3 and 7-4, given as in the tables (in
// zigzag scan index order).
var (
	default4x4Intra = [16]uint8{6, 13, 13, 20, 20, 20, 28, 28, 28, 28, 32, 32, 32, 37, 37, 42}
	default4x4Inter = [16]uint8{10, 14, 14, 20, 20, 20, 24, 24, 24, 24, 27, 27, 27, 30, 30, 34}
	default8x8Intra = [64]uint8{
		6, 10, 10, 13, 11, 13, 16, 16, 16, 16, 18, 18, 18, 18, 18, 23,
		23, 23, 23, 23, 23, 25, 25, 25, 25, 25, 25, 25, 27, 27, 27, 27,
		27, 27, 27, 27, 29, 29, 29, 29, 29, 29, 29, 31, 31, 31, 31, 31,
		31, 33, 33, 33, 33, 33, 36, 36, 36, 36, 38, 38, 38, 40, 40, 42,
	}
	default8x8Inter = [64]uint8{
		9, 13, 13, 15, 13, 15, 17, 17, 17, 17, 19, 19, 19, 19, 19, 21,
		21, 21, 21, 21, 21, 22, 22, 22, 22, 22, 22, 22, 24, 24, 24, 24,
		24, 24, 24, 24, 25, 25, 25, 25, 25, 25, 25, 27, 27, 27, 27, 27,
		27, 28, 28, 28, 28, 28, 30, 30, 30, 30, 32, 32, 32, 33, 33, 35,
	}
)

// Raster-order versions of the default lists, built at init.
var (
	default4x4IntraRaster, default4x4InterRaster [16]uint8
	default8x8IntraRaster, default8x8InterRaster [64]uint8
)

func init() {
	for i, p := range zigzag4x4 {
		default4x4IntraRaster[p] = default4x4Intra[i]
		default4x4InterRaster[p] = default4x4Inter[i]
	}
	for i, p := range zigzag8x8 {
		default8x8IntraRaster[p] = default8x8Intra[i]
		default8x8InterRaster[p] = default8x8Inter[i]
	}
}

// ScalingLists holds the scaling lists in effect for a picture, in raster
// scan order (which is what VA-API and DXVA consume).
//
// L4 is indexed 0 Intra Y, 1 Intra Cb, 2 Intra Cr, 3 Inter Y, 4 Inter Cb,
// 5 Inter Cr. L8 is indexed 0 Intra Y, 1 Inter Y, 2 Intra Cb, 3 Inter Cb,
// 4 Intra Cr, 5 Inter Cr (only the first two exist for 4:2:0).
type ScalingLists struct {
	L4 [6][16]uint8
	L8 [6][64]uint8
}

// flatScalingLists returns Flat_4x4_16 / Flat_8x8_16 for every list.
func flatScalingLists() ScalingLists {
	var s ScalingLists
	for i := range s.L4 {
		for j := range s.L4[i] {
			s.L4[i][j] = 16
		}
	}
	for i := range s.L8 {
		for j := range s.L8[i] {
			s.L8[i][j] = 16
		}
	}
	return s
}

// parseScalingList implements scaling_list() (7.3.2.1.1.1). The list is
// written to dst in raster order. useDefault reports that the bitstream
// requested the default list for this index.
func parseScalingList(r *bitstream.Reader, dst []uint8, zigzag []int) (useDefault bool, err error) {
	last, next := int32(8), int32(8)
	for j := range dst {
		if next != 0 {
			delta, err := r.ReadSE()
			if err != nil {
				return false, err
			}
			next = (last + delta + 256) % 256
			if j == 0 && next == 0 {
				return true, nil
			}
		}
		v := next
		if next == 0 {
			v = last
		}
		dst[zigzag[j]] = uint8(v)
		last = v
	}
	return false, nil
}

// parseScalingMatrix parses count scaling lists guarded by present flags
// and applies the fall-back rules of Table 7-2. fallback is nil for an SPS
// (fall-back rule A) and the SPS lists for a PPS (fall-back rule B).
func parseScalingMatrix(r *bitstream.Reader, count int, fallback *ScalingLists) (ScalingLists, error) {
	var s ScalingLists
	for i := 0; i < 12; i++ {
		present := false
		if i < count {
			var err error
			if present, err = r.ReadFlag(); err != nil {
				return s, err
			}
		}
		useDefault := false
		if present {
			var err error
			if i < 6 {
				useDefault, err = parseScalingList(r, s.L4[i][:], zigzag4x4[:])
			} else {
				useDefault, err = parseScalingList(r, s.L8[i-6][:], zigzag8x8[:])
			}
			if err != nil {
				return s, err
			}
		}
		switch {
		case present && !useDefault:
			// Parsed above.
		case useDefault || fallback == nil:
			// Fall-back rule A for lists 0, 3, 6, 7; the others copy the
			// previous list of the same kind.
			switch i {
			case 0:
				s.L4[0] = default4x4IntraRaster
			case 3:
				s.L4[3] = default4x4InterRaster
			case 6:
				s.L8[0] = default8x8IntraRaster
			case 7:
				s.L8[1] = default8x8InterRaster
			default:
				if useDefault {
					// useDefaultScalingMatrixFlag always means the default
					// table for this list type, never the previous list.
					s.setDefault(i)
				} else {
					s.copyPrevious(i)
				}
			}
		default:
			// Fall-back rule B: lists 0, 3, 6, 7 come from the SPS.
			switch i {
			case 0, 3:
				s.L4[i] = fallback.L4[i]
			case 6, 7:
				s.L8[i-6] = fallback.L8[i-6]
			default:
				s.copyPrevious(i)
			}
		}
	}
	return s, nil
}

func (s *ScalingLists) setDefault(i int) {
	switch {
	case i < 3:
		s.L4[i] = default4x4IntraRaster
	case i < 6:
		s.L4[i] = default4x4InterRaster
	case i%2 == 0:
		s.L8[i-6] = default8x8IntraRaster
	default:
		s.L8[i-6] = default8x8InterRaster
	}
}

func (s *ScalingLists) copyPrevious(i int) {
	switch {
	case i < 6:
		s.L4[i] = s.L4[i-1]
	default:
		s.L8[i-6] = s.L8[i-8]
	}
}
