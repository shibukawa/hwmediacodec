package h264_test

import (
	"errors"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/h264"
)

func TestParseSPSRejectsBitDepthOverflow(t *testing.T) {
	for _, depth := range []uint32{15, 0, 1, 6} {
		sps := encoderSPS(100)
		sps.BitDepthLuma = depth
		if _, err := h264.ParseSPS(h264.WriteSPS(sps)); !errors.Is(err, h264.ErrSyntax) {
			t.Fatalf("accepted bit depth %d: %v", depth, err)
		}
	}
}

// Frame cropping offsets are ue(v) fields whose sum must leave a picture;
// a stream that crops everything away (or wraps the sum) is refused.
func TestParseSPSRejectsCropLargerThanPicture(t *testing.T) {
	sps := encoderSPS(100) // 320x240 with cropping on, crop units of 2
	sps.CropLeft, sps.CropRight = 80, 80
	if _, err := h264.ParseSPS(h264.WriteSPS(sps)); !errors.Is(err, h264.ErrSyntax) {
		t.Fatalf("accepted a crop of the whole width: %v", err)
	}
	sps = encoderSPS(100)
	sps.CropLeft, sps.CropRight = 79, 80
	if _, err := h264.ParseSPS(h264.WriteSPS(sps)); err != nil {
		t.Fatalf("rejected a crop that leaves two columns: %v", err)
	}
	// Crop on a parameter set built in memory stays within the coded size
	// even when the offsets wrap 32 bits.
	s := &h264.SPS{PicWidthInMbs: 20, PicHeightInMapUnits: 15, FrameMbsOnly: true, ChromaFormatIDC: 1,
		FrameCropping: true, CropLeft: 0x80000000, CropRight: 0x80000000}
	if left, _, width, _ := s.Crop(); left != 0 || width != 320 {
		t.Fatalf("Crop with wrapped offsets = left %d width %d, want 0 and 320", left, width)
	}
}
