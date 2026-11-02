//go:build linux && !android

package vaapi

import (
	"fmt"
	"math"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

// defaultQIndex is the AV1 quantiser index used when neither a bitrate nor
// a quality was requested; it is about what defaultQP is for H.264.
const defaultQIndex = 100

// av1Encoder is the AV1 side of an encoder: the stream layout, the packed
// sequence header and the VA parameter buffers. Every frame is shown; key
// frames refresh all eight reference slots and an inter frame predicts from
// the slot of the frame before it and stores itself in the other one of
// the two the encoder alternates between, which are the two reconstructed
// surfaces.
type av1Encoder struct {
	stream *av1Stream
	seqOBU []byte
	ownSeq *av1.SequenceHeader // seqOBU parsed
	qindex uint8

	// frameNum counts the frames since the last key frame; its low bits
	// are the order hint.
	frameNum int

	seqParam  sys.EncSequenceParameterBufferAV1
	picParam  sys.EncPictureParameterBufferAV1
	tileGroup sys.EncTileGroupBufferAV1
	frameHdr  []byte

	// seq is the sequence header of the driver's output, for telling key
	// frames in it.
	seq *av1.SequenceHeader
	// outputChecked is set once the size of the coded picture was compared
	// with the requested one.
	outputChecked bool
}

// initAV1 sets the coded size: surfaces cover whole 64x64 superblocks.
// An encoder that codes the picture at the size it aligns its surfaces to
// (AMD before VCN 5: 64x16) would make a stream of the wrong size, which no
// decoder crops back, so sizes it cannot take exactly are refused.
func (e *encoder) initAV1() error {
	aw, ah, mw, mh := e.dpy.encodeSurfaceLimits(e.profile, e.entrypoint)
	if e.cfg.Width%aw != 0 || e.cfg.Height%ah != 0 {
		return unsupportedEncode(e.cfg.Codec, fmt.Sprintf("the VA-API AV1 encoder (%s) codes %dx%d as %dx%d; use a width that is a multiple of %d and a height that is a multiple of %d",
			e.dpy.vendor, e.cfg.Width, e.cfg.Height, (e.cfg.Width+aw-1)/aw*aw, (e.cfg.Height+ah-1)/ah*ah, aw, ah))
	}
	if e.cfg.Width < mw || e.cfg.Height < mh {
		return unsupportedEncode(e.cfg.Codec, fmt.Sprintf("%dx%d is below the encoder minimum of %dx%d", e.cfg.Width, e.cfg.Height, mw, mh))
	}
	e.av1 = &av1Encoder{}
	e.alignedW = (e.cfg.Width + 63) &^ 63
	e.alignedH = (e.cfg.Height + 63) &^ 63
	return nil
}

// buildAV1Stream fixes the stream layout once the rate control mode is
// known and writes the sequence header.
func (e *encoder) buildAV1Stream() error {
	cfg := &e.cfg
	a := e.av1
	// The drivers write the frame headers from the packed ones they are
	// given (Intel patches them, Mesa parses them), so both are required.
	if e.packed&(sys.EncPackedHeaderSequence|sys.EncPackedHeaderPicture) != sys.EncPackedHeaderSequence|sys.EncPackedHeaderPicture {
		return unsupportedEncode(cfg.Codec, "the VA-API AV1 encoder ("+e.dpy.vendor+") does not take packed sequence and frame headers")
	}
	var bitrate int64
	if e.rcMode != sys.RCCQP {
		bitrate = int64(e.peakBits)
	}
	s := newAV1Stream(cfg.Width, cfg.Height, av1LevelIdx(cfg.Width, cfg.Height, cfg.FrameRate, bitrate))
	s.bt709 = cfg.BT709
	s.enableCDEF = true

	if ext2, ok := e.dpy.configAttrib(e.profile, e.entrypoint, sys.ConfigAttribEncAV1Ext2); ok {
		s.tileSizeBytes = int(ext2&sys.EncAV1Ext2TileSizeBytesMinus1Mask) + 1
		s.obuSizeBytes = int(ext2>>2&3) + 1
		switch tx := ext2 >> sys.EncAV1Ext2TxModeSupportShift & sys.EncAV1Ext2TxModeSupportMask; {
		case tx&sys.EncAV1TxModeSupportSelect != 0:
			s.txMode = av1TxModeSelect
		case tx&sys.EncAV1TxModeSupportLargest != 0:
			s.txMode = av1TxModeLargest
		default:
			return unsupportedEncode(cfg.Codec, "the VA-API AV1 encoder offers no usable transform mode")
		}
		if maxTiles := int(ext2>>sys.EncAV1Ext2MaxTileNumMinus1Shift&sys.EncAV1Ext2MaxTileNumMinus1Mask) + 1; maxTiles > 1 && s.numTiles() > maxTiles {
			return unsupportedEncode(cfg.Codec, fmt.Sprintf("%dx%d needs %d tiles, the encoder takes at most %d", cfg.Width, cfg.Height, s.numTiles(), maxTiles))
		}
	}
	a.stream = s
	a.seqOBU = s.sequenceHeaderOBU()
	obus, err := av1.Split(a.seqOBU)
	if err != nil {
		return err
	}
	if a.ownSeq, err = av1.ParseSequenceHeader(obus[0].Payload); err != nil {
		return err
	}

	switch {
	case e.rcMode == sys.RCCQP && cfg.Quality > 0:
		a.qindex = uint8(min(max(math.Round(255-235*cfg.Quality), 1), 255))
	case e.rcMode == sys.RCCQP:
		a.qindex = defaultQIndex
	default:
		// The starting point of the rate control.
		a.qindex = 128
	}
	return nil
}

func (e *encoder) fillAV1SequenceParameters() {
	a := e.av1
	s := &a.seqParam
	*s = sys.EncSequenceParameterBufferAV1{}
	s.SeqProfile = 0
	s.SeqLevelIdx = a.stream.level
	s.IntraPeriod = uint32(e.gop)
	s.IPPeriod = 1
	s.BitsPerSecond = e.peakBits
	fields := sys.AV1EncSeqEnableOrderHint | sys.AV1EncSeqSubsamplingX | sys.AV1EncSeqSubsamplingY
	if a.stream.enableCDEF {
		fields |= sys.AV1EncSeqEnableCDEF
	}
	s.SeqFields = fields
	s.OrderHintBitsMinus1 = av1OrderHintBits - 1
}

// av1FrameFor describes the next frame.
func (e *encoder) av1FrameFor(key bool) av1Frame {
	a := e.av1
	f := av1Frame{key: key, orderHint: uint8(a.frameNum), qindex: a.qindex}
	if key {
		f.refreshFlags = 0xff
	} else {
		f.refSlot = uint8(e.reconIdx ^ 1)
		f.refreshFlags = 1 << uint(e.reconIdx)
	}
	return f
}

func (e *encoder) fillAV1PictureParameters(f *av1Frame, hdr av1FrameHeaderBits, recon, ref uint32) {
	a := e.av1
	s := a.stream
	p := &a.picParam
	*p = sys.EncPictureParameterBufferAV1{}
	p.FrameWidthMinus1 = uint16(e.cfg.Width - 1)
	p.FrameHeightMinus1 = uint16(e.cfg.Height - 1)
	p.ReconstructedFrame = recon
	p.CodedBuf = e.codedBuf
	for i := range p.ReferenceFrames {
		p.ReferenceFrames[i] = sys.InvalidID
	}
	p.OrderHint = f.orderHint
	p.RefreshFrameFlags = f.refreshFlags
	var flags uint32
	if f.key {
		p.PrimaryRefFrame = av1PrimaryRefNone
		flags |= uint32(av1.KeyFrame) | sys.AV1EncPicErrorResilientMode
	} else {
		p.ReferenceFrames[f.refSlot] = ref
		for i := range p.RefFrameIdx {
			p.RefFrameIdx[i] = f.refSlot
		}
		p.PrimaryRefFrame = 0
		p.RefFrameCtrlL0 = 1 // search LAST_FRAME only
		flags |= uint32(av1.InterFrame)
	}
	p.PictureFlags = flags
	p.BaseQIndex = f.qindex
	if e.rcMode != sys.RCCQP {
		p.MinBaseQIndex, p.MaxBaseQIndex = 1, 255
	}
	p.ModeControlFlags = uint32(s.txMode) << sys.AV1EncModeControlTxModeShift
	cols, rows := s.tileSizes()
	p.TileCols, p.TileRows = uint8(len(cols)), uint8(len(rows))
	copy(p.WidthInSbsMinus1[:], cols)
	copy(p.HeightInSbsMinus1[:], rows)
	p.BitOffsetQIndex = uint32(hdr.qindex)
	p.BitOffsetSegmentation = uint32(hdr.segmentation)
	p.BitOffsetLoopfilterParams = uint32(hdr.loopFilter)
	p.BitOffsetCDEFParams = uint32(hdr.cdef)
	p.SizeInBitsCDEFParams = uint32(hdr.cdefSize)
	p.SizeInBitsFrameHdrOBU = uint32(hdr.total)
	// The size field follows the one-byte OBU header; on key frames the
	// sequence header OBU comes first in the packed data.
	p.ByteOffsetFrameHdrOBUSize = 1
	if f.key {
		p.ByteOffsetFrameHdrOBUSize += uint32(len(a.seqOBU))
	}
	p.TileGroupOBUHdrInfo = sys.AV1EncTileGroupOBUHasSizeField
}

// encodeAV1Picture submits one AV1 frame and returns its coded data.
func (e *encoder) encodeAV1Picture(surface uint32, key bool) ([]byte, error) {
	a := e.av1
	e.bufIDs = e.bufIDs[:0]
	defer e.destroyBuffers()

	if key {
		a.frameNum = 0
		e.fillAV1SequenceParameters()
		if err := e.newBuffer(sys.EncSequenceParameterBufferType, int(unsafe.Sizeof(a.seqParam)), unsafe.Pointer(&a.seqParam)); err != nil {
			return nil, err
		}
		if err := e.addRateControlBuffers(); err != nil {
			return nil, err
		}
	}

	f := e.av1FrameFor(key)
	hdr, pos := a.stream.frameHeaderOBU(&f)
	a.frameHdr = hdr
	e.fillAV1PictureParameters(&f, pos, e.recon[e.reconIdx], e.recon[e.reconIdx^1])
	if err := e.newBuffer(sys.EncPictureParameterBufferType, int(unsafe.Sizeof(a.picParam)), unsafe.Pointer(&a.picParam)); err != nil {
		return nil, err
	}
	if key {
		if err := e.addPackedHeader(sys.EncPackedHeaderTypeSequence, a.seqOBU, 8*len(a.seqOBU), false); err != nil {
			return nil, err
		}
	}
	if err := e.addPackedHeader(sys.EncPackedHeaderTypePicture, a.frameHdr, 8*len(a.frameHdr), false); err != nil {
		return nil, err
	}
	a.tileGroup = sys.EncTileGroupBufferAV1{TgStart: 0, TgEnd: uint8(a.stream.numTiles() - 1)}
	if err := e.newBuffer(sys.EncSliceParameterBufferType, int(unsafe.Sizeof(a.tileGroup)), unsafe.Pointer(&a.tileGroup)); err != nil {
		return nil, err
	}
	data, err := e.submit(surface)
	if err != nil {
		return nil, err
	}
	a.frameNum++
	return data, nil
}

// av1Packet turns the coded data of one frame into a temporal unit
// without temporal delimiters, with the sequence header in front of key
// frames.
func (e *encoder) av1Packet(data []byte, pts int64) (codec.Packet, error) {
	a := e.av1
	tu, err := av1.ParseTemporalUnit(data, a.seq)
	if err != nil {
		return codec.Packet{}, &codec.BackendError{Backend: Name, Op: "encode", Message: "the driver produced an invalid AV1 temporal unit: " + err.Error()}
	}
	if tu.Sequence != nil {
		a.seq = tu.Sequence
	}
	data = av1.StripTemporalDelimiters(data, tu.OBUs)
	if tu.Keyframe && tu.SequenceHeader == nil {
		out := make([]byte, 0, len(a.seqOBU)+len(data))
		data = append(append(out, a.seqOBU...), data...)
		if a.seq == nil {
			a.seq = a.ownSeq
		}
	}
	if tu.Keyframe && !a.outputChecked && a.seq != nil {
		a.outputChecked = true
		if a.seq.MaxFrameWidth != e.cfg.Width || a.seq.MaxFrameHeight != e.cfg.Height {
			return codec.Packet{}, &codec.BackendError{Backend: Name, Op: "encode", Message: fmt.Sprintf(
				"the driver (%s) encoded a %dx%d picture for the requested %dx%d",
				e.dpy.vendor, a.seq.MaxFrameWidth, a.seq.MaxFrameHeight, e.cfg.Width, e.cfg.Height)}
		}
	}
	return codec.Packet{Data: data, PTS: pts, DTS: pts, Keyframe: tu.Keyframe}, nil
}
