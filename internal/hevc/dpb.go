package hevc

import "errors"

// Errors reported by the DPB.
var (
	// ErrNoKeyframe is returned when decoding is started on a picture that
	// is not an intra random access point.
	ErrNoKeyframe = errors.New("hevc: stream does not start with an IRAP picture")
	// ErrSkipped is returned for RASL pictures associated with the IRAP
	// picture decoding started at: they reference pictures that precede it
	// and are not decodable (8.1.3).
	ErrSkipped = errors.New("hevc: RASL picture skipped after a random access point")
	// ErrPictureInProgress is returned when Start is called twice without
	// Finish.
	ErrPictureInProgress = errors.New("hevc: previous picture was not finished")
)

// Picture is one decoded picture tracked by the DPB.
type Picture struct {
	POC int32
	// LongTerm is set once a reference picture set lists the picture as a
	// long-term reference.
	LongTerm bool
	// Missing marks a stand-in for a reference picture the stream names but
	// never delivered (a broken stream or a lost picture).
	Missing bool
	// Handle is the backend's storage for the picture (for example a VA
	// surface). The DPB only passes it around.
	Handle any

	claimed bool
}

// DPB implements the decoded picture buffer bookkeeping of the decoding
// process: picture order count (8.3.1), the reference picture set (8.3.2)
// and reference picture list construction (8.3.4). Output order is not
// managed here; pictures are handed back to the caller in decoding order.
type DPB struct {
	// Release, when set, is called once for every picture that stops being
	// a reference (dropped from the reference picture set, an IRAP picture
	// that starts a new sequence, or Reset). It is not called for Missing
	// stand-ins.
	Release func(*Picture)
	// MissingHandle, when set, supplies the Handle of a Missing stand-in.
	MissingHandle func() any

	pics []*Picture // reference pictures, the current one excluded
	cur  *Picture
	poc  POCState

	// first is true until the first picture after NewDPB, Reset or
	// EndOfSequence: an IRAP picture seen then has NoRaslOutputFlag set.
	first    bool
	started  bool
	skipRASL bool

	stCurrBefore []*Picture
	stCurrAfter  []*Picture
	ltCurr       []*Picture
	foll         []*Picture
}

// NewDPB returns an empty DPB.
func NewDPB() *DPB { return &DPB{first: true} }

// Current returns the picture being decoded, or nil.
func (d *DPB) Current() *Picture { return d.cur }

// Start begins the picture whose first slice segment header is sh: it
// derives the picture order count and the reference picture set, and drops
// the pictures the set no longer lists. handle is stored in the returned
// picture. Start reports ErrSkipped for RASL pictures that must not be
// decoded and ErrNoKeyframe when decoding has to begin at an IRAP picture
// and sh is not one; neither changes the DPB.
func (d *DPB) Start(sps *SPS, sh *SliceHeader, handle any) (*Picture, error) {
	if d.cur != nil {
		return nil, ErrPictureInProgress
	}
	t := sh.NALType
	irap := IsIRAP(t)
	if !irap && (!d.started || d.first) {
		return nil, ErrNoKeyframe
	}
	if IsRASL(t) && d.skipRASL {
		return nil, ErrSkipped
	}
	noRaslOutput := irap && (IsIDR(t) || IsBLA(t) || d.first)
	switch {
	case noRaslOutput:
		// Pictures before this one are gone for good: the RASL pictures
		// that follow a CRA or BLA picture cannot be decoded.
		d.skipRASL = t == NALCRA || IsBLA(t)
		d.releaseAll()
	case !IsRASL(t) && !IsRADL(t):
		d.skipRASL = false
	}
	poc := d.poc.Next(sps, sh, noRaslOutput)
	d.started, d.first = true, false
	d.cur = &Picture{POC: poc, Handle: handle}
	d.deriveRPS(sps, sh, poc)
	return d.cur, nil
}

// deriveRPS implements 8.3.2 for the current picture.
func (d *DPB) deriveRPS(sps *SPS, sh *SliceHeader, poc int32) {
	d.stCurrBefore = d.stCurrBefore[:0]
	d.stCurrAfter = d.stCurrAfter[:0]
	d.ltCurr = d.ltCurr[:0]
	d.foll = d.foll[:0]
	for _, p := range d.pics {
		p.claimed = false
	}
	if IsIDR(sh.NALType) {
		d.releaseAll()
		return
	}
	maxLsb := int32(1) << uint(sps.Log2MaxPOCLsb)

	// Long-term pictures first: their candidates are all reference
	// pictures, and a picture they claim stops being a short-term one.
	for _, lt := range sh.LongTerm {
		pocLt := int32(lt.POCLsb)
		if lt.DeltaPOCMsbPresent {
			pocLt += poc - int32(lt.DeltaPOCMsbCycle)*maxLsb - (poc & (maxLsb - 1))
		}
		var found *Picture
		for _, p := range d.pics {
			if p.claimed {
				continue
			}
			if (lt.DeltaPOCMsbPresent && p.POC == pocLt) || (!lt.DeltaPOCMsbPresent && p.POC&(maxLsb-1) == pocLt) {
				found = p
				break
			}
		}
		if found == nil {
			if !lt.UsedByCurrPic {
				continue
			}
			found = d.missing(pocLt)
		}
		found.claimed = true
		found.LongTerm = true
		if lt.UsedByCurrPic {
			d.ltCurr = append(d.ltCurr, found)
		} else {
			d.foll = append(d.foll, found)
		}
	}

	if rps := sh.ShortTermRPS; rps != nil {
		shortTerm := func(delta int32, used bool, curr *[]*Picture) {
			want := poc + delta
			var found *Picture
			for _, p := range d.pics {
				if !p.claimed && !p.LongTerm && p.POC == want {
					found = p
					break
				}
			}
			if found == nil {
				if !used {
					return
				}
				found = d.missing(want)
			}
			found.claimed = true
			if used {
				*curr = append(*curr, found)
			} else {
				d.foll = append(d.foll, found)
			}
		}
		for i := 0; i < rps.NumNegativePics; i++ {
			shortTerm(rps.DeltaPocS0[i], rps.UsedByCurrPicS0[i], &d.stCurrBefore)
		}
		for i := 0; i < rps.NumPositivePics; i++ {
			shortTerm(rps.DeltaPocS1[i], rps.UsedByCurrPicS1[i], &d.stCurrAfter)
		}
	}

	// Everything the set does not list is no longer a reference.
	kept := d.pics[:0]
	for _, p := range d.pics {
		if p.claimed {
			kept = append(kept, p)
		} else {
			d.release(p)
		}
	}
	for i := len(kept); i < len(d.pics); i++ {
		d.pics[i] = nil
	}
	d.pics = kept
}

// missing adds a stand-in for a reference picture that is not in the DPB.
func (d *DPB) missing(poc int32) *Picture {
	p := &Picture{POC: poc, Missing: true}
	if d.MissingHandle != nil {
		p.Handle = d.MissingHandle()
	}
	d.pics = append(d.pics, p)
	return p
}

func (d *DPB) release(p *Picture) {
	if d.Release != nil && !p.Missing {
		d.Release(p)
	}
}

func (d *DPB) releaseAll() {
	for i, p := range d.pics {
		d.release(p)
		d.pics[i] = nil
	}
	d.pics = d.pics[:0]
}

// Refs returns the reference pictures of the current picture: the ones it
// may predict from in reference picture set order (RefPicSetStCurrBefore,
// RefPicSetStCurrAfter, RefPicSetLtCurr), followed by the ones only kept
// for later pictures. The slice is valid until the next Start.
func (d *DPB) Refs() []*Picture {
	out := make([]*Picture, 0, len(d.pics))
	out = append(out, d.stCurrBefore...)
	out = append(out, d.stCurrAfter...)
	out = append(out, d.ltCurr...)
	return append(out, d.foll...)
}

// RefPicSets returns RefPicSetStCurrBefore, RefPicSetStCurrAfter and
// RefPicSetLtCurr of the current picture.
func (d *DPB) RefPicSets() (stCurrBefore, stCurrAfter, ltCurr []*Picture) {
	return d.stCurrBefore, d.stCurrAfter, d.ltCurr
}

// RefPicLists builds RefPicList0 and RefPicList1 for one slice of the
// current picture (8.3.4). Entries are nil only for a broken stream whose
// list modification points outside the reference picture set.
func (d *DPB) RefPicLists(sh *SliceHeader) (l0, l1 []*Picture) {
	if sh.SliceType == SliceI {
		return nil, nil
	}
	build := func(a, b []*Picture, n int, modified bool, entries []uint32) []*Picture {
		total := len(a) + len(b) + len(d.ltCurr)
		if total == 0 || n == 0 {
			return nil
		}
		size := max(n, total)
		tmp := make([]*Picture, 0, size)
		for len(tmp) < size {
			for _, set := range [][]*Picture{a, b, d.ltCurr} {
				for _, p := range set {
					if len(tmp) < size {
						tmp = append(tmp, p)
					}
				}
			}
		}
		out := make([]*Picture, n)
		for i := range out {
			idx := i
			if modified {
				idx = len(tmp)
				if i < len(entries) {
					idx = int(entries[i])
				}
			}
			if idx < len(tmp) {
				out[i] = tmp[idx]
			}
		}
		return out
	}
	l0 = build(d.stCurrBefore, d.stCurrAfter, int(sh.NumRefIdxL0Active), sh.RefPicListModificationL0, sh.ListEntryL0)
	if sh.SliceType == SliceB {
		l1 = build(d.stCurrAfter, d.stCurrBefore, int(sh.NumRefIdxL1Active), sh.RefPicListModificationL1, sh.ListEntryL1)
	}
	return l0, l1
}

// Finish ends the current picture. Every decoded picture is a short-term
// reference until a later reference picture set leaves it out.
func (d *DPB) Finish() {
	if d.cur == nil {
		return
	}
	d.pics = append(d.pics, d.cur)
	d.cur = nil
}

// Abort drops the current picture without keeping it as a reference.
func (d *DPB) Abort() {
	if d.cur != nil {
		d.release(d.cur)
		d.cur = nil
	}
}

// EndOfSequence records an end of sequence (or end of bitstream) NAL unit:
// the next picture must be an IRAP picture and starts a new coded video
// sequence.
func (d *DPB) EndOfSequence() { d.first = true }

// Reset drops every picture. Decoding restarts at the next IRAP picture.
func (d *DPB) Reset() {
	d.Abort()
	d.releaseAll()
	d.stCurrBefore, d.stCurrAfter, d.ltCurr, d.foll = nil, nil, nil, nil
	d.poc.Reset()
	d.first, d.started, d.skipRASL = true, false, false
}
