// Package reorder turns the decode-order output of a backend decoder into
// display order. It parses parameter sets and slice headers in Go to derive
// picture order counts, tags every packet with a display-order key, and
// holds decoded frames back until the bitstream's reorder bound guarantees
// that no earlier picture is still to come.
package reorder

import (
	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
)

// Tracker follows one elementary stream in decode order and assigns a
// display-order key to every picture.
type Tracker struct {
	codec codec.Codec
	seq   uint32
	// afterReset is true until the first random access point after NewTracker
	// or Reset; an HEVC CRA picture seen then has NoRaslOutputFlag set.
	afterReset bool
	// skipRASL drops the RASL pictures that follow a CRA with
	// NoRaslOutputFlag; they reference pictures the decoder never saw.
	skipRASL bool

	h264PS  *h264.ParameterSets
	h264POC h264.POCState

	hevcSPS map[uint32]*hevc.SPS
	hevcPPS map[uint32]*hevc.PPS
	hevcPOC hevc.POCState
}

// NewTracker returns a Tracker for c.
func NewTracker(c codec.Codec) *Tracker {
	t := &Tracker{
		codec:   c,
		h264PS:  h264.NewParameterSets(),
		hevcSPS: map[uint32]*hevc.SPS{},
		hevcPPS: map[uint32]*hevc.PPS{},
	}
	t.Reset()
	return t
}

// Reset forgets the picture order state (not the parameter sets). The next
// picture is expected to be a random access point and starts a new
// sequence.
func (t *Tracker) Reset() {
	t.afterReset = true
	t.skipRASL = false
	t.h264POC.Reset()
	t.hevcPOC.Reset()
}

// Classify processes one access unit (NAL units without start codes, as
// annexb.Split returns them). It returns the display-order key of the
// picture in the access unit and whether the access unit should be handed
// to the decoder. Access units without a picture are forwarded with a zero
// key; they produce no frame.
func (t *Tracker) Classify(nals [][]byte) (codec.Order, bool) {
	switch t.codec {
	case codec.H264:
		return t.classifyH264(nals)
	case codec.HEVC:
		return t.classifyHEVC(nals)
	}
	return codec.Order{}, true
}

func (t *Tracker) classifyH264(nals [][]byte) (codec.Order, bool) {
	for _, nal := range nals {
		switch annexb.NALUnitType(codec.H264, nal) {
		case h264.NALSPS:
			t.h264PS.AddSPS(nal) //nolint:errcheck // a broken SPS is reported by the backend
		case h264.NALPPS:
			t.h264PS.AddPPS(nal) //nolint:errcheck
		case h264.NALSlice, h264.NALSliceIDR:
			h, sps, _, err := h264.ParseSliceHeader(nal, t.h264PS)
			if err != nil {
				// Unparseable picture: order it after everything seen so
				// far so that it cannot hold other frames back.
				t.seq++
				return codec.Order{Seq: t.seq}, true
			}
			if h.IDR || (h.NALRefIdc != 0 && h.HasMMCO5()) {
				// An IDR picture, or one that resets the picture order
				// count with memory_management_control_operation 5, starts
				// a new output sequence.
				t.seq++
				t.afterReset = false
			}
			poc := t.h264POC.Next(sps, h)
			return codec.Order{Seq: t.seq, POC: poc, Reorder: clampReorder(sps.MaxReorderFrames())}, true
		}
	}
	return codec.Order{}, true
}

func (t *Tracker) classifyHEVC(nals [][]byte) (codec.Order, bool) {
	for _, nal := range nals {
		ty := hevc.Type(nal)
		switch {
		case ty == hevc.NALSPS:
			if s, err := hevc.ParseSPSHead(nal); err == nil {
				t.hevcSPS[s.ID] = s
			}
		case ty == hevc.NALPPS:
			if p, err := hevc.ParsePPSHead(nal); err == nil {
				t.hevcPPS[p.ID] = p
			}
		case ty == hevc.NALEOS || ty == hevc.NALEOB:
			t.afterReset = true
		case hevc.IsVCL(ty):
			if hevc.IsRASL(ty) {
				if t.skipRASL {
					return codec.Order{}, false
				}
			} else if !hevc.IsRADL(ty) {
				// A trailing picture or an IRAP ends the leading pictures
				// of the previous CRA.
				t.skipRASL = false
			}
			h, err := hevc.ParseSliceHead(nal, t.lookupHEVC)
			if err != nil {
				t.seq++
				return codec.Order{Seq: t.seq}, true
			}
			_, sps := t.lookupHEVC(h.PPSID)
			noRaslOutput := false
			if hevc.IsIRAP(ty) {
				noRaslOutput = hevc.IsIDR(ty) || hevc.IsBLA(ty) || t.afterReset
				if noRaslOutput {
					t.seq++
					t.skipRASL = ty == hevc.NALCRA || ty == hevc.NALBLAWLP
				}
				t.afterReset = false
			}
			poc := t.hevcPOC.Next(sps, h, noRaslOutput)
			return codec.Order{Seq: t.seq, POC: poc, Reorder: clampReorder(sps.MaxReorderFrames())}, true
		}
	}
	return codec.Order{}, true
}

func (t *Tracker) lookupHEVC(ppsID uint32) (*hevc.PPS, *hevc.SPS) {
	p := t.hevcPPS[ppsID]
	if p == nil {
		return nil, nil
	}
	return p, t.hevcSPS[p.SPSID]
}

func clampReorder(n int) uint8 {
	if n < 0 {
		return 0
	}
	if n > 16 {
		return 16
	}
	return uint8(n)
}
