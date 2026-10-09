//go:build darwin

package videotoolbox

import (
	"bytes"
	"fmt"

	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/videotoolbox/sys"
)

// av1State is the AV1-specific decoder state: the sequence header the
// current decompression session was created for.
type av1State struct {
	seqPayload []byte
	seq        *av1.SequenceHeader
}

// sendAV1 is Send for AV1. p.Data is one temporal unit in the low-overhead
// OBU format (an IVF frame or an ISOBMFF sample), with or without temporal
// delimiter OBUs. A sequence header that differs from the one the session
// was created for (a new stream, a size change) replaces the session; the
// unit is then handed to VideoToolbox whole, since every temporal unit
// yields exactly one shown frame.
func (d *decoder) sendAV1(p codec.Packet) error {
	tu, err := av1.ParseTemporalUnit(p.Data, d.av1.seq)
	if err != nil {
		return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
	}
	if tu.SequenceHeader != nil && !bytes.Equal(tu.SequenceHeader.Payload, d.av1.seqPayload) {
		if err := d.av1Supported(tu.Sequence); err != nil {
			return err
		}
		fd, st := av1FormatDescription(tu.Sequence, tu.SequenceHeader.Payload)
		if st != 0 || fd == 0 {
			return &codec.BackendError{Backend: Name, Op: "CMVideoFormatDescriptionCreate", Status: int64(st), Message: sys.StatusString(st)}
		}
		if err := d.recreateSession(fd); err != nil {
			return err
		}
		d.av1.seqPayload = append([]byte(nil), tu.SequenceHeader.Payload...)
		d.av1.seq = tu.Sequence
	}
	if !tu.HasFrame || d.session == 0 {
		// Nothing to decode, or no sequence header seen yet.
		return nil
	}
	if d.waitKeyframe {
		if !tu.Keyframe {
			return nil
		}
		d.waitKeyframe = false
	}
	return d.decodePacket(av1SampleData(p.Data, tu), inflightFrame{pts: p.PTS, order: codec.PacketOrder(p)})
}

// av1SampleData returns the bytes placed in the sample buffer for a
// temporal unit. The unit goes in as it is, temporal delimiters included:
// VideoToolbox accepts both forms and produces the same frames (measured on
// an M3, 2026-10-09), so no copy is made.
func av1SampleData(data []byte, tu *av1.TemporalUnit) []byte {
	return data
}

// av1Supported reports ErrUnsupported for streams the hardware decoder
// does not take, so that the caller gets a clear reason instead of a
// decode-time failure.
func (d *decoder) av1Supported(sh *av1.SequenceHeader) error {
	reason := ""
	switch {
	case sh.Profile != 0:
		reason = fmt.Sprintf("seq_profile %d; VideoToolbox decodes Main profile (4:2:0) only", sh.Profile)
	case sh.MonoChrome:
		reason = "monochrome streams are not supported"
	}
	if reason != "" {
		return &codec.UnsupportedError{Backend: Name, Codec: codec.AV1, Direction: codec.Decode, Reason: reason}
	}
	return nil
}

// av1ProbeSeqHeader is the sequence header of a 320x240 8-bit Main profile
// stream (written by SVT-AV1); av1SessionAvailable describes a stream with
// it to find out whether VideoToolbox can open an AV1 session at all.
var av1ProbeSeqHeader = []byte{0x02, 0x00, 0x00, 0x05, 0x21, 0xe7, 0xfd, 0xe2, 0x57, 0xc8, 0x02}

// av1SessionAvailable reports whether VideoToolbox can create an AV1
// decompression session under the given hardware policy. NewDecoder calls
// it on machines without the AV1 engine when software is allowed: macOS
// lists a "SW AV1 Decoder" (com.apple.videotoolbox.videodecoder.av1.sw)
// in VTCopyVideoDecoderList but refuses to instantiate it for applications
// (kVTCouldNotFindVideoDecoderErr with hardware decoding disabled, with or
// without VTRegisterSupplementalVideoDecoderIfAvailable; measured on an M3
// with macOS 26, 2026-10-09), so the answer is given at NewDecoder instead
// of at the first Send.
func av1SessionAvailable(allowSoftware bool) bool {
	sh, err := av1.ParseSequenceHeader(av1ProbeSeqHeader)
	if err != nil {
		return false
	}
	fd, st := av1FormatDescription(sh, av1ProbeSeqHeader)
	if st != 0 || fd == 0 {
		return false
	}
	defer sys.Release(fd)
	// A decoder that is never registered: its handle 0 matches no entry,
	// so a callback (there is none without a decode) would be ignored.
	d := &decoder{cfg: codec.DecoderConfig{Codec: codec.AV1, AllowSoftware: allowSoftware}}
	session, st := d.createSession(fd, sys.PixelFormat420YpCbCr8BiPlanarVideoRange)
	if st != 0 {
		return false
	}
	sessionMu.Lock()
	sys.VTDecompressionSessionInvalidate(session)
	sessionMu.Unlock()
	sys.Release(session)
	return true
}

// av1FormatDescription builds the CMVideoFormatDescription of an AV1 stream:
// codec type 'av01', the maximum frame size of the sequence header, and
// the av1C record as the "av1C" sample description extension atom. The
// caller owns the returned reference.
func av1FormatDescription(sh *av1.SequenceHeader, seqPayload []byte) (uintptr, int32) {
	rec := av1.CodecConfigurationRecord(sh, seqPayload)
	data := sys.CFData(rec)
	defer sys.Release(data)
	key := sys.CFString("av1C")
	defer sys.Release(key)
	atoms := sys.NewDictionary()
	defer sys.Release(atoms)
	sys.CFDictionarySetValue(atoms, key, data)
	ext := sys.NewDictionary()
	defer sys.Release(ext)
	sys.CFDictionarySetValue(ext, sys.KCMFormatDescriptionExtensionSampleDescriptionExtensionAtoms, atoms)
	var fd uintptr
	st := sys.CMVideoFormatDescriptionCreate(0, sys.CodecTypeAV1, int32(sh.MaxFrameWidth), int32(sh.MaxFrameHeight), ext, &fd)
	return fd, st
}
