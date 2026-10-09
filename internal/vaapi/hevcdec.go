//go:build linux

package vaapi

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

// hevcProfiles lists the VA profiles that can decode the 8-bit 4:2:0 HEVC
// streams this backend handles, most specific first. A Main 10 decoder
// handles Main streams too.
var hevcProfiles = []int32{sys.ProfileHEVCMain, sys.ProfileHEVCMain10}

// hevcDecoder is the HEVC bitstream state of a decoder: parameter sets, the
// decoded picture buffer and the scratch buffers of the picture being
// submitted.
type hevcDecoder struct {
	ps  *hevc.ParameterSets
	dpb *hevc.DPB

	headers  []*hevc.SliceHeader
	picParam sys.PictureParameterBufferHEVC
	iqMatrix sys.IQMatrixBufferHEVC
	sliceBuf []sys.SliceParameterBufferHEVC
}

func newHEVCDecoder(d *decoder) *hevcDecoder {
	h := &hevcDecoder{ps: hevc.NewParameterSets(), dpb: hevc.NewDPB()}
	h.dpb.Release = func(p *hevc.Picture) {
		if s, ok := p.Handle.(*surface); ok && !s.dummy {
			s.inDPB = false
		}
	}
	// A reference the stream never delivered is never output and a
	// conforming stream never predicts from it, so all stand-ins share one
	// surface.
	h.dpb.MissingHandle = func() any {
		if d.seq == nil {
			return nil // an untyped nil: no surface to stand in
		}
		return d.seq.dummy
	}
	return h
}

// sendHEVC decodes the access unit in nals. The caller holds d.mu.
func (d *decoder) sendHEVC(p codec.Packet, nals [][]byte) error {
	st := d.hevc
	var slices [][]byte
	endOfSequence := false
	for _, nal := range nals {
		t := hevc.Type(nal)
		switch {
		case t == hevc.NALSPS:
			if _, err := st.ps.AddSPS(nal); err != nil {
				return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
			}
		case t == hevc.NALPPS:
			if _, err := st.ps.AddPPS(nal); err != nil {
				return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
			}
		case t == hevc.NALEOS || t == hevc.NALEOB:
			if len(slices) == 0 {
				st.dpb.EndOfSequence()
			} else {
				endOfSequence = true
			}
		case hevc.IsSlice(t):
			slices = append(slices, nal)
		}
	}
	if len(slices) == 0 {
		return nil
	}

	// Parse every slice segment header up front: it validates the access
	// unit before any state changes.
	headers := st.headers[:0]
	var sps *hevc.SPS
	var pps *hevc.PPS
	var prev *hevc.SliceHeader
	for i, nal := range slices {
		sh, s, pp, err := hevc.ParseSliceHeader(nal, st.ps, prev)
		if err != nil {
			if errors.Is(err, hevc.ErrMissingSPS) || errors.Is(err, hevc.ErrMissingPPS) {
				// Parameter sets have not arrived yet; skip until they do.
				return nil
			}
			return fmt.Errorf("%w: slice segment %d: %v", codec.ErrInvalidData, i, err)
		}
		switch {
		case sh.DataByteOffset()+sh.EmulationPreventionBytes >= len(nal):
			// The driver is told where slice data begins inside the NAL
			// unit; a unit that ends with its header has none.
			return fmt.Errorf("%w: slice segment %d has no slice data", codec.ErrInvalidData, i)
		case i == 0 && !sh.FirstSliceSegmentInPic:
			return fmt.Errorf("%w: the packet does not begin with the first slice segment of a picture", codec.ErrInvalidData)
		case i > 0 && (sh.FirstSliceSegmentInPic || sh.PPSID != headers[0].PPSID || sh.NALType != headers[0].NALType):
			return fmt.Errorf("%w: slice segment %d belongs to a different picture", codec.ErrInvalidData, i)
		}
		headers = append(headers, sh)
		prev = sh
		sps, pps = s, pp
	}
	st.headers = headers
	sh := headers[0]
	irap := hevc.IsIRAP(sh.NALType)
	if d.waitKeyframe && !irap {
		return nil
	}
	if err := checkHEVCStream(sps, pps); err != nil {
		return err
	}
	maxRefs := max(sps.MaxDecPicBuffering()-1, 1)
	if d.seq == nil || d.seq.width != sps.Width || d.seq.height != sps.Height || d.seq.maxRefs < maxRefs || !isHEVCProfile(d.seq.profile) {
		if !irap {
			// A new sequence must start at an IRAP picture.
			d.waitKeyframe = true
			return nil
		}
		if len(d.pending) > 0 {
			// Frames of the old sequence are still waiting for Receive and
			// hold its surfaces; the caller must drain them first.
			return codec.ErrAgain
		}
		st.dpb.Reset()
		if err := d.setupSequence(hevcProfiles, sps.Width, sps.Height, maxRefs,
			fmt.Sprintf("general_profile_idc %d", sps.PTL.ProfileIDC)); err != nil {
			return err
		}
	}
	s := d.freeSurface()
	if s == nil {
		return codec.ErrAgain
	}

	cur, err := st.dpb.Start(sps, sh, s)
	switch {
	case errors.Is(err, hevc.ErrSkipped):
		// A RASL picture after the random access point decoding started at.
		return nil
	case errors.Is(err, hevc.ErrNoKeyframe):
		d.waitKeyframe = true
		return nil
	case err != nil:
		return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
	}
	d.waitKeyframe = false
	s.inDPB = true
	s.pendingOutput = sh.PicOutput
	s.pts = p.PTS
	s.order = codec.PacketOrder(p)
	s.cropX, s.cropY, s.width, s.height = sps.Crop()
	s.codedW, s.codedH = sps.Width, sps.Height

	if err := d.decodeHEVCPicture(sps, pps, headers, cur, slices); err != nil {
		st.dpb.Abort()
		s.pendingOutput = false
		return err
	}
	st.dpb.Finish()
	if endOfSequence {
		st.dpb.EndOfSequence()
	}
	if sh.PicOutput {
		d.pending = append(d.pending, s)
	}
	return nil
}

func isHEVCProfile(p int32) bool {
	return p == sys.ProfileHEVCMain || p == sys.ProfileHEVCMain10
}

// checkHEVCStream rejects stream features the backend does not handle.
func checkHEVCStream(sps *hevc.SPS, pps *hevc.PPS) error {
	rext := sps.Range
	switch {
	case sps.ChromaFormatIDC != 1:
		return unsupported(codec.HEVC, fmt.Sprintf("chroma_format_idc %d is not supported; only 4:2:0", sps.ChromaFormatIDC))
	case sps.BitDepthLuma != 8 || sps.BitDepthChroma != 8:
		return unsupported(codec.HEVC, fmt.Sprintf("%d-bit video is not supported; only 8-bit", max(sps.BitDepthLuma, sps.BitDepthChroma)))
	case rext != (hevc.SPSRangeExtension{}) || pps.RangeExtension:
		return unsupported(codec.HEVC, "range extension coding tools are not supported; only the Main profile")
	case sps.MultilayerExtension || sps.Extension3D || sps.SCCExtension || pps.MultilayerExtension || pps.Extension3D || pps.SCCExtension:
		return unsupported(codec.HEVC, "multilayer, 3D and screen content extensions are not supported; only the Main profile")
	}
	return nil
}

// decodeHEVCPicture submits one picture (all its slice segments) to the
// driver.
func (d *decoder) decodeHEVCPicture(sps *hevc.SPS, pps *hevc.PPS, headers []*hevc.SliceHeader, cur *hevc.Picture, slices [][]byte) error {
	st := d.hevc
	d.bufIDs = d.bufIDs[:0]
	defer d.destroyBuffers()

	refs := st.dpb.Refs()
	fillHEVCPictureParameters(&st.picParam, sps, pps, headers[0], cur, st.dpb, refs)
	if err := d.createBuffer(sys.PictureParameterBufferType, int(unsafe.Sizeof(st.picParam)), unsafe.Pointer(&st.picParam)); err != nil {
		return err
	}
	if sps.ScalingListEnabled {
		lists := &sps.ScalingList
		if pps.ScalingListDataPresent {
			lists = &pps.ScalingList
		}
		fillHEVCIQMatrix(&st.iqMatrix, lists)
		if err := d.createBuffer(sys.IQMatrixBufferType, int(unsafe.Sizeof(st.iqMatrix)), unsafe.Pointer(&st.iqMatrix)); err != nil {
			return err
		}
	}

	if cap(st.sliceBuf) < len(slices) {
		st.sliceBuf = make([]sys.SliceParameterBufferHEVC, len(slices))
	}
	st.sliceBuf = st.sliceBuf[:len(slices)]
	for i, nal := range slices {
		sh := headers[i]
		l0, l1 := st.dpb.RefPicLists(sh)
		sp := &st.sliceBuf[i]
		fillHEVCSliceParameters(sp, sps, sh, len(nal), l0, l1, refs, i == len(slices)-1)
		if err := d.createBuffer(sys.SliceParameterBufferType, int(unsafe.Sizeof(*sp)), unsafe.Pointer(sp)); err != nil {
			return err
		}
		if err := d.createBuffer(sys.SliceDataBufferType, len(nal), unsafe.Pointer(&nal[0])); err != nil {
			return err
		}
	}
	return d.renderBuffers(cur.Handle.(*surface))
}
