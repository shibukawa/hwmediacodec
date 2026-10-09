//go:build linux

package vaapi

import (
	"bytes"
	"errors"
	"fmt"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

// av1Profiles lists the VA profiles that decode the 8-bit 4:2:0 AV1 streams
// this backend handles.
var av1Profiles = []int32{sys.ProfileAV1Profile0}

// av1Slot is what the backend keeps for one reference frame slot: the
// surface later frames predict from, the surface to show when the frame is
// output again with show_existing_frame (the same one unless film grain was
// applied), and the size of the frame.
type av1Slot struct {
	recon, display *surface
	width, height  int
}

// av1Picture is the frame being assembled from the OBUs of a temporal
// unit.
type av1Picture struct {
	h       *av1.Header
	slot    av1Slot
	tiles   int
	groups  []*av1.TileGroup
	started bool
}

// av1Decoder is the AV1 bitstream state of a decoder: the frame header
// parser's reference state, the surfaces of the reference slots and the
// frames waiting for Receive.
type av1Decoder struct {
	state      av1.State
	seqPayload []byte
	slots      [av1.NumRefFrames]av1Slot
	frames     []*codec.Frame

	picParam sys.DecPictureParameterBufferAV1
	tileBuf  []sys.SliceParameterBufferAV1
}

// av1SurfaceCount returns the number of surfaces the reference slots can
// hold besides the frame being decoded: one per slot, and a second one per
// slot for streams with film grain, whose shown pictures differ from the
// references.
func av1SurfaceCount(seq *av1.SequenceHeader) int {
	if seq.FilmGrainParamsPresent {
		return 2*av1.NumRefFrames + 1
	}
	return av1.NumRefFrames
}

// checkAV1Stream rejects sequences the backend does not handle.
func checkAV1Stream(seq *av1.SequenceHeader) error {
	switch {
	case seq.Profile != 0:
		return unsupported(codec.AV1, fmt.Sprintf("seq_profile %d is not supported; only the Main profile (4:2:0)", seq.Profile))
	case seq.MonoChrome:
		return unsupported(codec.AV1, "monochrome streams are not supported")
	case seq.BitDepth != 8:
		return unsupported(codec.AV1, fmt.Sprintf("%d-bit video is not supported; only 8-bit", seq.BitDepth))
	case seq.OperatingPointIdc>>8&(seq.OperatingPointIdc>>8-1) != 0:
		return unsupported(codec.AV1, "streams with several spatial layers are not supported")
	}
	return nil
}

// av1InOperatingPoint reports whether an OBU belongs to operating point 0,
// the one that is decoded (Section 7.1 drops the others).
func av1InOperatingPoint(seq *av1.SequenceHeader, o *av1.OBU) bool {
	if seq == nil || !o.HasExtension || seq.OperatingPointIdc == 0 {
		return true
	}
	idc := seq.OperatingPointIdc
	return idc>>o.TemporalID&1 != 0 && idc>>(o.SpatialID+8)&1 != 0
}

// sendAV1 decodes the temporal unit in p. The caller holds d.mu.
func (d *decoder) sendAV1(p codec.Packet) error {
	st := d.av1
	if len(st.frames) >= outputSlack {
		return codec.ErrAgain
	}
	tu, err := av1.ParseTemporalUnit(p.Data, st.state.Seq)
	if err != nil {
		return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
	}
	if d.waitKeyframe && tu.HasFrame && !tu.Keyframe {
		// Decoding (re)starts at a shown key frame; a sequence header in
		// the skipped unit still counts.
		if tu.Sequence != nil {
			st.setSequence(tu)
		}
		return nil
	}
	err = d.decodeAV1Unit(p, tu)
	if err != nil && !errors.Is(err, codec.ErrAgain) {
		// The reference state may no longer match the stream.
		d.waitKeyframe = true
	}
	return err
}

func (st *av1Decoder) setSequence(tu *av1.TemporalUnit) {
	if bytes.Equal(tu.SequenceHeader.Payload, st.seqPayload) {
		return
	}
	st.seqPayload = append(st.seqPayload[:0], tu.SequenceHeader.Payload...)
	st.state.Seq = tu.Sequence
}

func (d *decoder) decodeAV1Unit(p codec.Packet, tu *av1.TemporalUnit) error {
	st := d.av1
	var pic *av1Picture
	for i := range tu.OBUs {
		o := &tu.OBUs[i]
		if o.Type == av1.OBUSequenceHeader {
			st.setSequence(tu)
			continue
		}
		if !av1InOperatingPoint(st.state.Seq, o) {
			continue
		}
		var groupData []byte
		switch o.Type {
		case av1.OBUFrameHeader, av1.OBUFrame:
			if pic != nil {
				if o.Type == av1.OBUFrame {
					return fmt.Errorf("%w: a frame begins before the previous one has all its tiles", codec.ErrInvalidData)
				}
				continue // a repeated frame header
			}
			if st.state.Seq == nil {
				return nil // nothing can be decoded before a sequence header
			}
			h, err := st.state.ParseHeader(o.Payload, o.TemporalID, o.SpatialID)
			if err != nil {
				return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
			}
			if h.ShowExistingFrame {
				if err := d.showExistingAV1(h, p.PTS); err != nil {
					return err
				}
				continue
			}
			if pic, err = d.beginAV1Picture(h); err != nil || pic == nil {
				return err
			}
			if o.Type != av1.OBUFrame {
				continue
			}
			groupData = o.Payload[(h.HeaderBits+7)/8:]
		case av1.OBUTileGroup:
			if pic == nil {
				return fmt.Errorf("%w: tile group without a frame header", codec.ErrInvalidData)
			}
			groupData = o.Payload
		case av1.OBUTileList:
			return unsupported(codec.AV1, "large scale tile streams are not supported")
		default:
			continue
		}
		tg, err := pic.h.ParseTileGroup(groupData)
		if err != nil {
			return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
		}
		if tg.Start != pic.tiles {
			return fmt.Errorf("%w: tile group starts at tile %d, expected %d", codec.ErrInvalidData, tg.Start, pic.tiles)
		}
		pic.groups = append(pic.groups, tg)
		pic.tiles = tg.End + 1
		if pic.tiles < pic.h.NumTiles() {
			continue
		}
		if err := d.finishAV1Picture(pic, p.PTS); err != nil {
			return err
		}
		pic = nil
	}
	if pic != nil {
		return fmt.Errorf("%w: the temporal unit ends with %d of %d tiles of a frame", codec.ErrInvalidData, pic.tiles, pic.h.NumTiles())
	}
	return nil
}

// beginAV1Picture prepares the sequence and the surfaces for a frame. It
// returns nil without an error when the frame cannot be decoded yet.
func (d *decoder) beginAV1Picture(h *av1.Header) (*av1Picture, error) {
	st := d.av1
	seq := st.state.Seq
	if err := checkAV1Stream(seq); err != nil {
		return nil, err
	}
	start := h.FrameType == av1.KeyFrame && h.ShowFrame
	if d.seq == nil || d.seq.width != seq.MaxFrameWidth || d.seq.height != seq.MaxFrameHeight ||
		d.seq.maxRefs < av1SurfaceCount(seq) || d.seq.profile != sys.ProfileAV1Profile0 {
		if !start {
			// A new sequence must start at a shown key frame.
			d.waitKeyframe = true
			return nil, nil
		}
		st.slots = [av1.NumRefFrames]av1Slot{}
		if err := d.setupSequence(av1Profiles, seq.MaxFrameWidth, seq.MaxFrameHeight, av1SurfaceCount(seq),
			fmt.Sprintf("seq_profile %d", seq.Profile)); err != nil {
			return nil, err
		}
	}
	if h.UpscaledWidth > d.seq.width || h.FrameHeight > d.seq.height {
		return nil, fmt.Errorf("%w: frame of %dx%d in a %dx%d sequence", codec.ErrInvalidData, h.UpscaledWidth, h.FrameHeight, d.seq.width, d.seq.height)
	}
	if !start {
		for i, slot := range st.slots {
			if slot.recon == nil {
				return nil, fmt.Errorf("%w: reference slot %d is empty", codec.ErrInvalidData, i)
			}
		}
	}
	d.waitKeyframe = false

	pic := &av1Picture{h: h}
	pic.slot = av1Slot{width: h.UpscaledWidth, height: h.FrameHeight}
	pic.slot.recon = d.freeSurface()
	if pic.slot.recon == nil {
		return nil, &codec.BackendError{Backend: Name, Op: "decode", Message: "every surface is held by a reference frame"}
	}
	pic.slot.display = pic.slot.recon
	if h.FilmGrain.ApplyGrain {
		// The reference stays free of grain; the picture to show goes to
		// a surface of its own.
		pic.slot.recon.inDPB = true
		pic.slot.display = d.freeSurface()
		pic.slot.recon.inDPB = false
		if pic.slot.display == nil {
			return nil, &codec.BackendError{Backend: Name, Op: "decode", Message: "every surface is held by a reference frame"}
		}
	}
	return pic, nil
}

// finishAV1Picture submits a frame whose tile groups are complete, updates
// the reference slots and queues the frame when it is shown.
func (d *decoder) finishAV1Picture(pic *av1Picture, pts int64) error {
	st := d.av1
	h := pic.h
	d.bufIDs = d.bufIDs[:0]
	defer d.destroyBuffers()

	var refs [av1.NumRefFrames]uint32
	for i, slot := range st.slots {
		refs[i] = sys.InvalidSurface
		if slot.recon != nil {
			refs[i] = slot.recon.id
		}
	}
	fillAV1PictureParameters(&st.picParam, st.state.Seq, h, pic.slot.recon.id, pic.slot.display.id, &refs)
	if err := d.createBuffer(sys.PictureParameterBufferType, int(unsafe.Sizeof(st.picParam)), unsafe.Pointer(&st.picParam)); err != nil {
		return err
	}
	st.tileBuf = st.tileBuf[:0]
	for _, tg := range pic.groups {
		st.tileBuf = appendAV1TileParameters(st.tileBuf, tg)
	}
	next := 0
	for _, tg := range pic.groups {
		// One parameter buffer with an element per tile, then the data
		// the tiles' offsets count into.
		params := st.tileBuf[next : next+len(tg.Tiles)]
		next += len(tg.Tiles)
		var id uint32
		if s := sys.CreateBuffer(d.dpy.dpy, d.seq.context, sys.SliceParameterBufferType, uint32(unsafe.Sizeof(params[0])),
			uint32(len(params)), unsafe.Pointer(&params[0]), &id); s != sys.StatusSuccess {
			return vaError("vaCreateBuffer", s)
		}
		d.bufIDs = append(d.bufIDs, id)
		if err := d.createBuffer(sys.SliceDataBufferType, len(tg.Data), unsafe.Pointer(&tg.Data[0])); err != nil {
			return err
		}
	}
	// The render target is the reference picture; the picture to show is
	// named in the parameters (ffmpeg's VA-API decoder does the same).
	if err := d.renderBuffers(pic.slot.recon); err != nil {
		return err
	}

	st.state.Update(h)
	for i := range st.slots {
		if h.RefreshFrameFlags>>uint(i)&1 != 0 {
			st.slots[i] = pic.slot
		}
	}
	st.markSurfaces(d.seq)
	if !h.ShowFrame {
		return nil
	}
	return d.outputAV1(pic.slot, pts)
}

// showExistingAV1 outputs the frame in a reference slot again. A key frame
// shown this way restarts the stream: it takes over every slot.
func (d *decoder) showExistingAV1(h *av1.Header, pts int64) error {
	st := d.av1
	slot := st.slots[h.FrameToShowMapIdx]
	if d.seq == nil || slot.display == nil {
		return fmt.Errorf("%w: show_existing_frame names the empty slot %d", codec.ErrInvalidData, h.FrameToShowMapIdx)
	}
	st.state.Update(h)
	if h.RefreshFrameFlags != 0 {
		for i := range st.slots {
			st.slots[i] = slot
		}
		st.markSurfaces(d.seq)
	}
	return d.outputAV1(slot, pts)
}

// markSurfaces records which surfaces the reference slots hold, so that
// freeSurface hands out the others.
func (st *av1Decoder) markSurfaces(seq *sequence) {
	for _, s := range seq.surfaces {
		s.inDPB = false
	}
	for _, slot := range st.slots {
		if slot.recon != nil {
			slot.recon.inDPB = true
			slot.display.inDPB = true
		}
	}
}

// outputAV1 copies the shown picture of a slot to CPU memory at once: the
// surface stays with the reference slots and may be shown again, so it
// cannot wait for Receive the way H.264 and HEVC pictures do.
func (d *decoder) outputAV1(slot av1Slot, pts int64) error {
	s := slot.display
	s.pts = pts
	s.order = codec.Order{}
	s.cropX, s.cropY, s.width, s.height = 0, 0, slot.width, slot.height
	s.codedW, s.codedH = d.seq.width, d.seq.height
	f, err := d.copySurface(s)
	if err != nil {
		return err
	}
	d.av1.frames = append(d.av1.frames, f)
	return nil
}
