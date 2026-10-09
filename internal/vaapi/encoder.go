//go:build linux

package vaapi

import (
	"context"
	"fmt"
	"io"
	"math"
	"sync"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

const (
	// maxPendingPackets bounds the packets held for Receive; Send reports
	// ErrAgain beyond it.
	maxPendingPackets = 64
	// defaultQP is the constant quantiser used when neither a bitrate nor a
	// quality was requested.
	defaultQP = 26
	// encLog2MaxFrameNum and encLog2MaxPOCLsb size frame_num and
	// pic_order_cnt_lsb in the streams this encoder writes.
	encLog2MaxFrameNum = 8
	encLog2MaxPOCLsb   = 8
	// VUI video signal description: video_format 5 is "unspecified" and
	// 1 stands for BT.709 in colour_primaries, transfer_characteristics
	// and matrix_coeffs alike.
	videoFormatUnspecified = 5
	colourBT709            = 1
)

// encoder is an H.264 or HEVC encoder on VAEntrypointEncSlice: I and P
// frames with one reference, frames in presentation order, synchronous per
// picture.
type encoder struct {
	mu  sync.Mutex
	cfg codec.EncoderConfig
	dpy *display

	profile    int32
	entrypoint int32
	config     uint32
	context    uint32
	packed     uint32 // packed header types sent to the driver
	rcMode     uint32
	qp         int
	peakBits   uint32
	targetPct  uint32
	gop        int

	sps       *h264.SPS
	pps       *h264.PPS
	paramSets []byte // SPS and PPS NAL units with start codes
	mbW, mbH  int
	alignedW  int
	alignedH  int

	ids         []uint32 // every surface, for teardown
	input       [2]uint32
	recon       [2]uint32
	codedBuf    uint32
	image       sys.Image
	hasImage    bool
	deriveWorks int

	inputIdx       int
	reconIdx       int
	haveRef        bool
	refFrameNum    int
	refPOC         int
	frameIndex     int // since start or Flush; drives the keyframe interval
	framesSinceIDR int
	idrPicID       int
	forceNext      bool
	flushed        bool
	closed         bool

	pending []codec.Packet
	bufIDs  []uint32

	seqParam   sys.EncSequenceParameterBufferH264
	picParam   sys.EncPictureParameterBufferH264
	sliceParam sys.EncSliceParameterBufferH264
	rcParam    sys.EncMiscParameterRateControl
	frParam    sys.EncMiscParameterFrameRate
	hrdParam   sys.EncMiscParameterHRD
	packedHdr  [2]sys.EncPackedHeaderParameterBuffer
	sliceHdr   []byte

	// hevc holds the HEVC parameter sets and buffers; it is nil for H.264
	// encoders.
	hevc *hevcEncoder
}

// encodeProfiles maps the requested profile to VA profiles in preference
// order; it is empty when the codec has no such profile here.
func encodeProfiles(c codec.Codec, p codec.Profile) []int32 {
	if c == codec.HEVC {
		// 8-bit 4:2:0 only.
		if p == codec.ProfileDefault || p == codec.ProfileMain {
			return []int32{sys.ProfileHEVCMain}
		}
		return nil
	}
	if c != codec.H264 {
		return nil
	}
	switch p {
	case codec.ProfileBaseline:
		return []int32{sys.ProfileH264ConstrainedBaseline}
	case codec.ProfileMain:
		return []int32{sys.ProfileH264Main}
	case codec.ProfileHigh:
		return []int32{sys.ProfileH264High}
	}
	return []int32{sys.ProfileH264High, sys.ProfileH264Main, sys.ProfileH264ConstrainedBaseline}
}

func newEncoder(cfg codec.EncoderConfig, dpy *display) (*encoder, error) {
	e := &encoder{cfg: cfg, dpy: dpy, forceNext: true}
	fail := func(reason string) (*encoder, error) { return nil, unsupportedEncode(cfg.Codec, reason) }

	if cfg.Width%2 != 0 || cfg.Height%2 != 0 {
		return fail(fmt.Sprintf("picture size %dx%d: 4:2:0 encoding needs even dimensions", cfg.Width, cfg.Height))
	}
	// Macroblock alignment; an HEVC encoder replaces the aligned size by its
	// minimum coding block alignment in initHEVC.
	e.mbW = (cfg.Width + 15) / 16
	e.mbH = (cfg.Height + 15) / 16
	e.alignedW, e.alignedH = e.mbW*16, e.mbH*16

	// Profile and entrypoint.
	candidates := encodeProfiles(cfg.Codec, cfg.Profile)
	if len(candidates) == 0 {
		return fail("profile " + cfg.Profile.String() + " is not available for " + cfg.Codec.String() + " on the vaapi backend")
	}
	for _, p := range candidates {
		if ep, ok := dpy.encodeEntrypoint(p); ok {
			e.profile, e.entrypoint = p, ep
			break
		}
	}
	if e.entrypoint == 0 {
		if cfg.Profile != codec.ProfileDefault {
			return fail("profile " + cfg.Profile.String() + " has no " + cfg.Codec.String() + " encode entrypoint on this driver (" + dpy.vendor + ")")
		}
		return fail("the VA-API driver (" + dpy.vendor + ") offers no " + cfg.Codec.String() + " encode entrypoint")
	}
	if cfg.Codec == codec.HEVC {
		if err := e.initHEVC(); err != nil {
			return nil, err
		}
	}
	if mw, ok := dpy.configAttrib(e.profile, e.entrypoint, sys.ConfigAttribMaxPictureWidth); ok && int(mw) < cfg.Width {
		return fail(fmt.Sprintf("width %d exceeds the encoder maximum of %d", cfg.Width, mw))
	}
	if mh, ok := dpy.configAttrib(e.profile, e.entrypoint, sys.ConfigAttribMaxPictureHeight); ok && int(mh) < cfg.Height {
		return fail(fmt.Sprintf("height %d exceeds the encoder maximum of %d", cfg.Height, mh))
	}
	if rt, ok := dpy.configAttrib(e.profile, e.entrypoint, sys.ConfigAttribRTFormat); ok && rt&sys.RTFormatYUV420 == 0 {
		return fail("the encoder does not accept 4:2:0 8-bit input")
	}

	// Rate control.
	rcSupported, ok := dpy.configAttrib(e.profile, e.entrypoint, sys.ConfigAttribRateControl)
	if !ok {
		rcSupported = sys.RCCQP | sys.RCCBR | sys.RCVBR
	}
	switch {
	case cfg.Quality > 0:
		if rcSupported&sys.RCCQP == 0 {
			return fail("constant quality (CQP) is not supported by this encoder")
		}
		e.rcMode = sys.RCCQP
		e.qp = int(math.Round(51 - 45*cfg.Quality))
		e.qp = min(max(e.qp, 1), 51)
	case cfg.Bitrate > 0:
		if cfg.Bitrate > math.MaxInt32 {
			return fail(fmt.Sprintf("bitrate %d is out of range", cfg.Bitrate))
		}
		if cfg.RateControl == codec.CBR {
			if rcSupported&sys.RCCBR == 0 {
				return fail("constant bitrate is not supported by this encoder")
			}
			e.rcMode, e.peakBits, e.targetPct = sys.RCCBR, uint32(cfg.Bitrate), 100
		} else {
			if rcSupported&sys.RCVBR == 0 {
				return fail("variable bitrate is not supported by this encoder")
			}
			e.rcMode, e.peakBits, e.targetPct = sys.RCVBR, uint32(min(2*cfg.Bitrate, math.MaxInt32)), 50
		}
		e.qp = defaultQP
	default:
		switch {
		case rcSupported&sys.RCCQP != 0:
			e.rcMode, e.qp = sys.RCCQP, defaultQP
		case rcSupported&sys.RCVBR != 0:
			// About 0.1 bit per pixel at the frame rate, like a mid-quality
			// stream.
			fps := cfg.FrameRate
			if fps <= 0 {
				fps = 30
			}
			bits := int(float64(cfg.Width*cfg.Height) * fps * 0.1)
			e.rcMode, e.peakBits, e.targetPct, e.qp = sys.RCVBR, uint32(min(2*bits, math.MaxInt32)), 50, defaultQP
		default:
			return fail("no usable rate control mode on this encoder")
		}
	}

	// Keyframe interval.
	switch {
	case cfg.KeyframeInterval > 0:
		e.gop = cfg.KeyframeInterval
	case cfg.FrameRate > 0:
		e.gop = int(cfg.FrameRate*2 + 0.5)
	default:
		e.gop = 60
	}

	// Packed headers: send what the driver accepts (Intel requires them,
	// Mesa generates its own when they are absent).
	if supported, ok := dpy.configAttrib(e.profile, e.entrypoint, sys.ConfigAttribEncPackedHeaders); ok {
		e.packed = supported & (sys.EncPackedHeaderSequence | sys.EncPackedHeaderSlice)
	}

	if e.hevc != nil {
		e.buildHEVCParameterSets()
	} else {
		e.buildParameterSets()
	}

	if err := e.createContext(); err != nil {
		e.teardown()
		return nil, err
	}
	return e, nil
}

// buildParameterSets derives the SPS and PPS the stream will carry.
func (e *encoder) buildParameterSets() {
	cfg := &e.cfg
	profileIDC := uint8(100)
	var constraints uint8
	cabac, t8x8 := true, true
	switch e.profile {
	case sys.ProfileH264ConstrainedBaseline:
		profileIDC, constraints, cabac, t8x8 = 66, 0xc0, false, false
	case sys.ProfileH264Main:
		profileIDC, t8x8 = 77, false
	}
	num, den := frameRateFraction(cfg.FrameRate)
	bitrateKbps := int(e.peakBits / 1000)
	mbps := 0
	if cfg.FrameRate > 0 {
		mbps = int(math.Ceil(float64(e.mbW*e.mbH) * cfg.FrameRate))
	}
	sps := &h264.SPS{
		ProfileIDC: profileIDC, ConstraintFlags: constraints,
		LevelIDC:              levelIDC(e.mbW*e.mbH, mbps, bitrateKbps, e.profile == sys.ProfileH264High),
		ChromaFormatIDC:       1,
		BitDepthLuma:          8,
		BitDepthChroma:        8,
		Log2MaxFrameNum:       encLog2MaxFrameNum,
		PicOrderCntType:       0,
		Log2MaxPicOrderCntLsb: encLog2MaxPOCLsb,
		MaxNumRefFrames:       1,
		PicWidthInMbs:         uint32(e.mbW),
		PicHeightInMapUnits:   uint32(e.mbH),
		FrameMbsOnly:          true,
		Direct8x8Inference:    true,
		VUIPresent:            true,
	}
	if e.alignedW != cfg.Width || e.alignedH != cfg.Height {
		sps.FrameCropping = true
		sps.CropRight = uint32(e.alignedW-cfg.Width) / 2
		sps.CropBottom = uint32(e.alignedH-cfg.Height) / 2
	}
	sps.VUI = h264.VUI{
		BitstreamRestriction:           true,
		MotionVectorsOverPicBoundaries: true,
		Log2MaxMvLengthHorizontal:      15,
		Log2MaxMvLengthVertical:        15,
		MaxNumReorderFrames:            0,
		MaxDecFrameBuffering:           1,
	}
	if num > 0 {
		sps.VUI.TimingInfoPresent = true
		sps.VUI.NumUnitsInTick = den
		sps.VUI.TimeScale = 2 * num
		sps.VUI.FixedFrameRate = true
	}
	if cfg.BT709 {
		sps.VUI.VideoSignalTypePresent = true
		sps.VUI.VideoFormat = videoFormatUnspecified
		sps.VUI.ColourDescriptionPresent = true
		sps.VUI.ColourPrimaries = colourBT709
		sps.VUI.TransferCharacteristics = colourBT709
		sps.VUI.MatrixCoefficients = colourBT709
	}
	pps := &h264.PPS{
		EntropyCodingMode:              cabac,
		NumSliceGroups:                 1,
		NumRefIdxL0DefaultActive:       1,
		NumRefIdxL1DefaultActive:       1,
		PicInitQpMinus26:               int32(e.qp - 26),
		DeblockingFilterControlPresent: true,
		Transform8x8Mode:               t8x8,
	}
	e.sps, e.pps = sps, pps
	spsNAL, ppsNAL := h264.WriteSPS(sps), h264.WritePPS(pps)
	e.paramSets = make([]byte, 0, 8+len(spsNAL)+len(ppsNAL))
	e.paramSets = append(append(e.paramSets, 0, 0, 0, 1), spsNAL...)
	e.paramSets = append(append(e.paramSets, 0, 0, 0, 1), ppsNAL...)
}

// frameRateFraction turns fps into a numerator/denominator pair that fits
// VA-API's 16-bit fields; (0, 0) when the rate is unknown.
func frameRateFraction(fps float64) (num, den uint32) {
	if fps <= 0 {
		return 0, 0
	}
	if fps == math.Trunc(fps) && fps < 65536 {
		return uint32(fps), 1
	}
	if n := math.Round(fps * 1001); n < 65536 {
		return uint32(n), 1001
	}
	if n := math.Round(fps * 100); n < 65536 {
		return uint32(n), 100
	}
	return uint32(math.Round(fps)), 1
}

// levelIDC picks the lowest level of Table A-1 that fits the frame size,
// macroblock rate and bitrate.
func levelIDC(frameMbs, mbps, bitrateKbps int, high bool) uint8 {
	levels := []struct {
		idc            uint8
		maxFS, maxMBPS int
		maxBR          int
	}{
		{10, 99, 1485, 64}, {11, 396, 3000, 192}, {12, 396, 6000, 384}, {13, 396, 11880, 768},
		{20, 396, 11880, 2000}, {21, 792, 19800, 4000}, {22, 1620, 20250, 4000},
		{30, 1620, 40500, 10000}, {31, 3600, 108000, 14000}, {32, 5120, 216000, 20000},
		{40, 8192, 245760, 20000}, {41, 8192, 245760, 50000}, {42, 8704, 522240, 50000},
		{50, 22080, 589824, 135000}, {51, 36864, 983040, 240000}, {52, 36864, 2073600, 240000},
		{60, 139264, 4177920, 240000}, {61, 139264, 8355840, 480000}, {62, 139264, 16711680, 800000},
	}
	for _, l := range levels {
		maxBR := l.maxBR
		if high {
			maxBR = maxBR * 5 / 4
		}
		if frameMbs <= l.maxFS && mbps <= l.maxMBPS && bitrateKbps <= maxBR {
			return l.idc
		}
	}
	return 62
}

func (e *encoder) createContext() error {
	dpy := e.dpy.dpy
	attrs := []sys.ConfigAttrib{
		{Type: sys.ConfigAttribRTFormat, Value: sys.RTFormatYUV420},
		{Type: sys.ConfigAttribRateControl, Value: e.rcMode},
	}
	if e.packed != 0 {
		attrs = append(attrs, sys.ConfigAttrib{Type: sys.ConfigAttribEncPackedHeaders, Value: e.packed})
	}
	if st := sys.CreateConfig(dpy, e.profile, e.entrypoint, &attrs[0], int32(len(attrs)), &e.config); st != sys.StatusSuccess {
		switch st {
		case sys.StatusErrorUnsupportedProfile, sys.StatusErrorUnsupportedEntrypoint, sys.StatusErrorUnsupportedRTFormat, sys.StatusErrorAttrNotSupported:
			return unsupportedEncode(e.cfg.Codec, "vaCreateConfig: "+statusMessage(st))
		}
		return vaError("vaCreateConfig", st)
	}
	e.ids = make([]uint32, 4)
	pixfmt := sys.IntegerAttrib(sys.SurfaceAttribPixelFormat, int32(sys.FourccNV12))
	if st := sys.CreateSurfaces(dpy, sys.RTFormatYUV420, uint32(e.alignedW), uint32(e.alignedH), &e.ids[0], uint32(len(e.ids)), &pixfmt, 1); st != sys.StatusSuccess {
		e.ids = nil
		if st == sys.StatusErrorResolutionNotSupported {
			return unsupportedEncode(e.cfg.Codec, fmt.Sprintf("%dx%d: %s", e.cfg.Width, e.cfg.Height, statusMessage(st)))
		}
		return vaError("vaCreateSurfaces", st)
	}
	e.input = [2]uint32{e.ids[0], e.ids[1]}
	e.recon = [2]uint32{e.ids[2], e.ids[3]}
	if st := sys.CreateContext(dpy, e.config, int32(e.alignedW), int32(e.alignedH), sys.Progressive, &e.ids[2], 2, &e.context); st != sys.StatusSuccess {
		return vaError("vaCreateContext", st)
	}
	// A coded buffer large enough for an intra frame at the lowest
	// quantiser: three bytes per pixel is well above worst case.
	size := e.alignedW*e.alignedH*3 + 65536
	if st := sys.CreateBuffer(dpy, e.context, sys.EncCodedBufferType, uint32(size), 1, nil, &e.codedBuf); st != sys.StatusSuccess {
		return vaError("vaCreateBuffer(coded)", st)
	}
	return nil
}

func (e *encoder) teardown() {
	dpy := e.dpy.dpy
	if e.codedBuf != 0 {
		sys.DestroyBuffer(dpy, e.codedBuf)
		e.codedBuf = 0
	}
	if e.hasImage {
		sys.DestroyImage(dpy, e.image.ImageID)
		e.hasImage = false
	}
	if e.context != 0 {
		sys.DestroyContext(dpy, e.context)
		e.context = 0
	}
	if len(e.ids) > 0 {
		sys.DestroySurfaces(dpy, &e.ids[0], int32(len(e.ids)))
		e.ids = nil
	}
	if e.config != 0 {
		sys.DestroyConfig(dpy, e.config)
		e.config = 0
	}
}

func (e *encoder) checkFrame(f *codec.Frame) error {
	switch {
	case f == nil:
		return fmt.Errorf("%w: nil frame", codec.ErrInvalidData)
	case f.Format != e.cfg.InputFormat:
		return fmt.Errorf("%w: frame format %s, encoder expects %s", codec.ErrInvalidData, f.Format, e.cfg.InputFormat)
	case f.Width != e.cfg.Width || f.Height != e.cfg.Height:
		return fmt.Errorf("%w: frame size %dx%d, encoder expects %dx%d", codec.ErrInvalidData, f.Width, f.Height, e.cfg.Width, e.cfg.Height)
	case len(f.Planes) != 2 || len(f.Strides) != 2:
		return fmt.Errorf("%w: NV12 frame needs 2 planes and 2 strides, got %d and %d", codec.ErrInvalidData, len(f.Planes), len(f.Strides))
	}
	rows := [2]int{f.Height, (f.Height + 1) / 2}
	rowBytes := [2]int{f.Width, (f.Width + 1) / 2 * 2}
	for i := range f.Planes {
		if f.Strides[i] < rowBytes[i] {
			return fmt.Errorf("%w: plane %d stride %d is smaller than its row of %d bytes", codec.ErrInvalidData, i, f.Strides[i], rowBytes[i])
		}
		if need := (rows[i]-1)*f.Strides[i] + rowBytes[i]; len(f.Planes[i]) < need {
			return fmt.Errorf("%w: plane %d holds %d bytes, need %d", codec.ErrInvalidData, i, len(f.Planes[i]), need)
		}
	}
	return nil
}

func (e *encoder) Send(ctx context.Context, f *codec.Frame) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.checkFrame(f); err != nil {
		return err
	}
	if len(e.pending) >= maxPendingPackets {
		return codec.ErrAgain
	}

	idr := e.forceNext || f.ForceKeyframe || (e.gop > 0 && e.frameIndex%e.gop == 0)
	surface := e.input[e.inputIdx]
	if err := e.upload(surface, f); err != nil {
		return err
	}
	data, err := e.encodePicture(surface, idr)
	if err != nil {
		return err
	}

	// Bookkeeping for the next picture.
	e.haveRef = true
	e.refFrameNum = e.framesSinceIDR % (1 << encLog2MaxFrameNum)
	e.refPOC = e.pictureOrderCount(e.framesSinceIDR)
	e.framesSinceIDR++
	e.frameIndex++
	e.inputIdx ^= 1
	e.reconIdx ^= 1
	e.forceNext = false
	e.flushed = false

	pkt := e.packetFromStream(data, f.PTS)
	if e.hevc != nil && pkt.Keyframe {
		if err := e.checkHEVCOutput(pkt.Data); err != nil {
			return err
		}
	}
	e.pending = append(e.pending, pkt)
	return nil
}

// pictureOrderCount returns the picture order count of the n-th picture
// after an IDR picture: H.264 counts fields, HEVC pictures.
func (e *encoder) pictureOrderCount(n int) int {
	if e.hevc != nil {
		return n
	}
	return 2 * n
}

// upload copies the frame into the input surface, replicating the last
// column and row into the macroblock padding.
func (e *encoder) upload(surface uint32, f *codec.Frame) error {
	dpy := e.dpy.dpy
	var img sys.Image
	derived := false
	if e.deriveWorks >= 0 {
		if st := sys.DeriveImage(dpy, surface, &img); st == sys.StatusSuccess {
			if img.Format.Fourcc == sys.FourccNV12 && img.NumPlanes == 2 {
				derived = true
				e.deriveWorks = 1
			} else {
				sys.DestroyImage(dpy, img.ImageID)
				e.deriveWorks = -1
			}
		} else {
			e.deriveWorks = -1
		}
	}
	if !derived {
		if !e.hasImage {
			format := sys.ImageFormat{Fourcc: sys.FourccNV12, ByteOrder: sys.LSBFirst, BitsPerPixel: 12}
			if st := sys.CreateImage(dpy, &format, int32(e.alignedW), int32(e.alignedH), &e.image); st != sys.StatusSuccess {
				return vaError("vaCreateImage", st)
			}
			e.hasImage = true
		}
		img = e.image
	}
	var base *byte
	if st := sys.MapBuffer(dpy, img.Buf, &base); st != sys.StatusSuccess {
		if derived {
			sys.DestroyImage(dpy, img.ImageID)
		}
		return vaError("vaMapBuffer", st)
	}
	if base == nil || img.NumPlanes < 2 {
		sys.UnmapBuffer(dpy, img.Buf)
		if derived {
			sys.DestroyImage(dpy, img.ImageID)
		}
		return &codec.BackendError{Backend: Name, Op: "vaMapBuffer", Message: "mapped image has no planes"}
	}
	dst := unsafe.Slice(base, int(img.DataSize))
	err := fillImage(dst, &img, f, int(img.Width), int(img.Height))
	sys.UnmapBuffer(dpy, img.Buf)
	if derived {
		sys.DestroyImage(dpy, img.ImageID)
		return err
	}
	if err != nil {
		return err
	}
	if st := sys.PutImage(dpy, surface, img.ImageID, 0, 0, uint32(img.Width), uint32(img.Height), 0, 0, uint32(img.Width), uint32(img.Height)); st != sys.StatusSuccess {
		return vaError("vaPutImage", st)
	}
	return nil
}

// fillImage writes the NV12 frame into a mapped image of at least the frame
// size, padding with edge pixels.
func fillImage(dst []byte, img *sys.Image, f *codec.Frame, imgW, imgH int) error {
	for p := 0; p < 2; p++ {
		pitch := int(img.Pitches[p])
		off := int(img.Offsets[p])
		rows, rowBytes := f.Height, f.Width
		imgRows := imgH
		if p == 1 {
			rows, rowBytes = (f.Height+1)/2, (f.Width+1)/2*2
			imgRows = (imgH + 1) / 2
		}
		if rowBytes > pitch || off+(imgRows-1)*pitch+pitch > len(dst) {
			return &codec.BackendError{Backend: Name, Op: "upload frame", Message: "image smaller than the picture"}
		}
		padBytes := imgW - rowBytes
		if padBytes > pitch-rowBytes {
			padBytes = pitch - rowBytes
		}
		src := f.Planes[p]
		stride := f.Strides[p]
		for r := 0; r < rows; r++ {
			row := dst[off+r*pitch : off+r*pitch+pitch]
			copy(row, src[r*stride:r*stride+rowBytes])
			if padBytes > 0 {
				if p == 0 {
					last := row[rowBytes-1]
					for i := rowBytes; i < rowBytes+padBytes; i++ {
						row[i] = last
					}
				} else {
					cb, cr := row[rowBytes-2], row[rowBytes-1]
					for i := rowBytes; i+1 < rowBytes+padBytes; i += 2 {
						row[i], row[i+1] = cb, cr
					}
				}
			}
		}
		if rows > 0 {
			last := dst[off+(rows-1)*pitch : off+(rows-1)*pitch+rowBytes+padBytes]
			for r := rows; r < imgRows; r++ {
				copy(dst[off+r*pitch:], last)
			}
		}
	}
	return nil
}

// encodePicture submits one picture and returns its coded data.
func (e *encoder) encodePicture(surface uint32, idr bool) ([]byte, error) {
	dpy, ctx := e.dpy.dpy, e.context
	e.bufIDs = e.bufIDs[:0]
	defer func() {
		for _, id := range e.bufIDs {
			sys.DestroyBuffer(dpy, id)
		}
	}()
	create := func(typ int32, size int, data unsafe.Pointer) error {
		var id uint32
		if st := sys.CreateBuffer(dpy, ctx, typ, uint32(size), 1, data, &id); st != sys.StatusSuccess {
			return vaError("vaCreateBuffer", st)
		}
		e.bufIDs = append(e.bufIDs, id)
		return nil
	}

	if idr {
		e.framesSinceIDR = 0
		e.idrPicID = (e.idrPicID + 1) & 0xffff
	}
	frameNum := e.framesSinceIDR % (1 << encLog2MaxFrameNum)
	poc := e.pictureOrderCount(e.framesSinceIDR)
	recon := e.recon[e.reconIdx]
	ref := e.recon[e.reconIdx^1]

	if idr {
		if h := e.hevc; h != nil {
			e.fillHEVCSequenceParameters()
			if err := create(sys.EncSequenceParameterBufferType, int(unsafe.Sizeof(h.seqParam)), unsafe.Pointer(&h.seqParam)); err != nil {
				return nil, err
			}
		} else {
			e.fillSequenceParameters()
			if err := create(sys.EncSequenceParameterBufferType, int(unsafe.Sizeof(e.seqParam)), unsafe.Pointer(&e.seqParam)); err != nil {
				return nil, err
			}
		}
		if num, den := frameRateFraction(e.cfg.FrameRate); num > 0 {
			e.frParam = sys.EncMiscParameterFrameRate{Type: sys.EncMiscParameterTypeFrameRate, Framerate: num | den<<16}
			if err := create(sys.EncMiscParameterBufferType, int(unsafe.Sizeof(e.frParam)), unsafe.Pointer(&e.frParam)); err != nil {
				return nil, err
			}
		}
		if e.rcMode != sys.RCCQP {
			e.rcParam = sys.EncMiscParameterRateControl{
				Type: sys.EncMiscParameterTypeRateControl, BitsPerSecond: e.peakBits, TargetPercentage: e.targetPct,
				WindowSize: 1000, RcFlags: sys.RcFlagDisableFrameSkip,
			}
			if err := create(sys.EncMiscParameterBufferType, int(unsafe.Sizeof(e.rcParam)), unsafe.Pointer(&e.rcParam)); err != nil {
				return nil, err
			}
			e.hrdParam = sys.EncMiscParameterHRD{Type: sys.EncMiscParameterTypeHRD, BufferSize: e.peakBits, InitialBufferFullness: e.peakBits / 4 * 3}
			if err := create(sys.EncMiscParameterBufferType, int(unsafe.Sizeof(e.hrdParam)), unsafe.Pointer(&e.hrdParam)); err != nil {
				return nil, err
			}
		}
	}

	if h := e.hevc; h != nil {
		e.fillHEVCPictureParameters(idr, recon, ref, poc)
		if err := create(sys.EncPictureParameterBufferType, int(unsafe.Sizeof(h.picParam)), unsafe.Pointer(&h.picParam)); err != nil {
			return nil, err
		}
	} else {
		e.fillPictureParameters(idr, recon, ref, frameNum, poc)
		if err := create(sys.EncPictureParameterBufferType, int(unsafe.Sizeof(e.picParam)), unsafe.Pointer(&e.picParam)); err != nil {
			return nil, err
		}
	}
	if idr && e.packed&sys.EncPackedHeaderSequence != 0 {
		e.packedHdr[0] = sys.EncPackedHeaderParameterBuffer{Type: sys.EncPackedHeaderTypeSequence, BitLength: uint32(8 * len(e.paramSets)), HasEmulationBytes: 1}
		if err := create(sys.EncPackedHeaderParameterBufferType, int(unsafe.Sizeof(e.packedHdr[0])), unsafe.Pointer(&e.packedHdr[0])); err != nil {
			return nil, err
		}
		if err := create(sys.EncPackedHeaderDataBufferType, len(e.paramSets), unsafe.Pointer(&e.paramSets[0])); err != nil {
			return nil, err
		}
	}

	// sliceBits is the size of the packed slice header after its start code.
	var sliceBits int
	if h := e.hevc; h != nil {
		sh := hevcSliceHeader(h.sps, h.pps, idr, poc)
		e.fillHEVCSliceParameters(sh, idr)
		if err := create(sys.EncSliceParameterBufferType, int(unsafe.Sizeof(h.sliceParam)), unsafe.Pointer(&h.sliceParam)); err != nil {
			return nil, err
		}
		if e.packed&sys.EncPackedHeaderSlice != 0 {
			// The header ends with byte_alignment(), so it is whole bytes.
			hdr := hevc.WriteSliceHeader(sh, h.sps, h.pps)
			e.sliceHdr = append(append(e.sliceHdr[:0], 0, 0, 0, 1), hdr...)
			sliceBits = 8 * len(hdr)
		}
	} else {
		sh := e.sliceHeader(idr, frameNum, poc)
		e.fillSliceParameters(sh, idr, ref, frameNum)
		if err := create(sys.EncSliceParameterBufferType, int(unsafe.Sizeof(e.sliceParam)), unsafe.Pointer(&e.sliceParam)); err != nil {
			return nil, err
		}
		if e.packed&sys.EncPackedHeaderSlice != 0 {
			hdr, bits := h264.WriteSliceHeader(sh, e.sps, e.pps)
			e.sliceHdr = append(append(e.sliceHdr[:0], 0, 0, 0, 1), hdr...)
			sliceBits = bits
		}
	}
	if e.packed&sys.EncPackedHeaderSlice != 0 {
		e.packedHdr[1] = sys.EncPackedHeaderParameterBuffer{Type: sys.EncPackedHeaderTypeSlice, BitLength: uint32(32 + sliceBits), HasEmulationBytes: 1}
		if err := create(sys.EncPackedHeaderParameterBufferType, int(unsafe.Sizeof(e.packedHdr[1])), unsafe.Pointer(&e.packedHdr[1])); err != nil {
			return nil, err
		}
		if err := create(sys.EncPackedHeaderDataBufferType, len(e.sliceHdr), unsafe.Pointer(&e.sliceHdr[0])); err != nil {
			return nil, err
		}
	}

	if st := sys.BeginPicture(dpy, ctx, surface); st != sys.StatusSuccess {
		return nil, vaError("vaBeginPicture", st)
	}
	if st := sys.RenderPicture(dpy, ctx, &e.bufIDs[0], int32(len(e.bufIDs))); st != sys.StatusSuccess {
		sys.EndPicture(dpy, ctx)
		return nil, vaError("vaRenderPicture", st)
	}
	if st := sys.EndPicture(dpy, ctx); st != sys.StatusSuccess {
		return nil, vaError("vaEndPicture", st)
	}

	synced := false
	if sys.SyncBuffer != nil {
		if st := sys.SyncBuffer(dpy, e.codedBuf, sys.TimeoutInfinite); st == sys.StatusSuccess {
			synced = true
		} else if st != sys.StatusErrorUnimplemented {
			return nil, vaError("vaSyncBuffer", st)
		}
	}
	if !synced {
		if st := sys.SyncSurface(dpy, surface); st != sys.StatusSuccess {
			return nil, vaError("vaSyncSurface", st)
		}
	}
	return e.readCodedBuffer()
}

// readCodedBuffer maps the coded buffer and concatenates its segments.
func (e *encoder) readCodedBuffer() ([]byte, error) {
	dpy := e.dpy.dpy
	var base *byte
	if st := sys.MapBuffer(dpy, e.codedBuf, &base); st != sys.StatusSuccess {
		return nil, vaError("vaMapBuffer(coded)", st)
	}
	defer sys.UnmapBuffer(dpy, e.codedBuf)
	var out []byte
	for seg := (*sys.CodedBufferSegment)(unsafe.Pointer(base)); seg != nil; seg = seg.Next {
		if seg.Status&sys.CodedBufStatusSliceOverflow != 0 {
			return nil, &codec.BackendError{Backend: Name, Op: "encode", Message: "coded buffer overflow"}
		}
		if seg.Buf != nil && seg.Size > 0 {
			out = append(out, unsafe.Slice(seg.Buf, int(seg.Size))...)
		}
	}
	if len(out) == 0 {
		return nil, &codec.BackendError{Backend: Name, Op: "encode", Message: "the driver produced no data for the picture"}
	}
	return out, nil
}

// packetFromStream wraps the coded data as an Annex-B access unit, adding
// the parameter sets in front of keyframes when the driver did not.
func (e *encoder) packetFromStream(data []byte, pts int64) codec.Packet {
	keyframe, hasSPS, hasVPS := false, false, false
	spsType := h264.NALSPS
	if e.hevc != nil {
		spsType = hevc.NALSPS
	}
	for _, nal := range annexb.Split(data) {
		t := annexb.NALUnitType(e.cfg.Codec, nal)
		switch {
		case annexb.IsVCL(e.cfg.Codec, t) && annexb.IsKeyframe(e.cfg.Codec, t):
			keyframe = true
		case t == spsType:
			hasSPS = true
		case e.hevc != nil && t == hevc.NALVPS:
			hasVPS = true
		}
	}
	var prefix []byte
	switch {
	case !keyframe:
	case !hasSPS:
		prefix = e.paramSets
	case e.hevc != nil && !hasVPS:
		// The driver wrote its own SPS and PPS but no VPS; ours describes
		// the same single-layer stream.
		prefix = append([]byte{0, 0, 0, 1}, hevc.WriteVPS(e.hevc.vps)...)
	}
	out := make([]byte, 0, len(prefix)+len(data))
	out = append(append(out, prefix...), data...)
	return codec.Packet{Data: out, PTS: pts, DTS: pts, Keyframe: keyframe}
}

func (e *encoder) fillSequenceParameters() {
	sps := e.sps
	s := &e.seqParam
	*s = sys.EncSequenceParameterBufferH264{}
	s.LevelIDC = sps.LevelIDC
	s.IntraPeriod = uint32(e.gop)
	s.IntraIDRPeriod = uint32(e.gop)
	s.IPPeriod = 1
	s.BitsPerSecond = e.peakBits
	s.MaxNumRefFrames = 1
	s.PictureWidthInMbs = uint16(e.mbW)
	s.PictureHeightInMbs = uint16(e.mbH)
	var seq uint32
	seq |= 1 << sys.EncSeqChromaFormatIDCShift
	seq |= sys.EncSeqFrameMbsOnlyFlag
	seq |= sys.EncSeqDirect8x8InferenceFlag
	seq |= uint32(sps.Log2MaxFrameNum-4) << sys.EncSeqLog2MaxFrameNumMinus4Shift
	seq |= uint32(sps.Log2MaxPicOrderCntLsb-4) << sys.EncSeqLog2MaxPicOrderCntLsbMinus4Shift
	s.SeqFields = seq
	if sps.FrameCropping {
		s.FrameCroppingFlag = 1
		s.FrameCropRightOffset = sps.CropRight
		s.FrameCropBottomOffset = sps.CropBottom
	}
	s.VUIParametersPresentFlag = 1
	var vui uint32 = sys.EncVUIBitstreamRestrictionFlag | sys.EncVUIMotionVectorsOverPicBoundariesFlag
	vui |= 15 << sys.EncVUILog2MaxMvLengthHorizontalShift
	vui |= 15 << sys.EncVUILog2MaxMvLengthVerticalShift
	if sps.VUI.TimingInfoPresent {
		vui |= sys.EncVUITimingInfoPresentFlag | sys.EncVUIFixedFrameRateFlag
		s.NumUnitsInTick = sps.VUI.NumUnitsInTick
		s.TimeScale = sps.VUI.TimeScale
	}
	s.VUIFields = vui
}

func (e *encoder) fillPictureParameters(idr bool, recon, ref uint32, frameNum, poc int) {
	p := &e.picParam
	*p = sys.EncPictureParameterBufferH264{}
	p.CurrPic = sys.PictureH264{PictureID: recon, FrameIdx: uint32(frameNum), TopFieldOrderCnt: int32(poc), BottomFieldOrderCnt: int32(poc)}
	for i := range p.ReferenceFrames {
		p.ReferenceFrames[i] = sys.InvalidPictureH264
	}
	if !idr && e.haveRef {
		p.ReferenceFrames[0] = e.refPicture(ref)
	}
	p.CodedBuf = e.codedBuf
	p.FrameNum = uint16(frameNum)
	p.PicInitQp = uint8(26 + e.pps.PicInitQpMinus26)
	var fields uint32
	if idr {
		fields |= sys.EncPicIdrPicFlag
	}
	fields |= 1 << sys.EncPicReferencePicFlagShift
	if e.pps.EntropyCodingMode {
		fields |= sys.EncPicEntropyCodingModeFlag
	}
	if e.pps.Transform8x8Mode {
		fields |= sys.EncPicTransform8x8ModeFlag
	}
	fields |= sys.EncPicDeblockingFilterControlPresentFlag
	p.PicFields = fields
}

func (e *encoder) refPicture(ref uint32) sys.PictureH264 {
	return sys.PictureH264{PictureID: ref, FrameIdx: uint32(e.refFrameNum), Flags: sys.PictureH264ShortTermReference,
		TopFieldOrderCnt: int32(e.refPOC), BottomFieldOrderCnt: int32(e.refPOC)}
}

// sliceHeader describes the single slice of the picture; it drives both the
// VA slice parameters and the packed slice header.
func (e *encoder) sliceHeader(idr bool, frameNum, poc int) *h264.SliceHeader {
	sh := &h264.SliceHeader{
		FrameNum:       uint32(frameNum),
		PicOrderCntLsb: uint32(poc % (1 << encLog2MaxPOCLsb)),
	}
	if idr {
		sh.NALType, sh.NALRefIdc, sh.IDR = h264.NALSliceIDR, 3, true
		sh.SliceTypeRaw, sh.SliceType = 2, h264.SliceI
		sh.IdrPicID = uint32(e.idrPicID)
	} else {
		sh.NALType, sh.NALRefIdc = h264.NALSlice, 2
		sh.SliceTypeRaw, sh.SliceType = 0, h264.SliceP
		sh.NumRefIdxL0Active = 1
	}
	return sh
}

func (e *encoder) fillSliceParameters(sh *h264.SliceHeader, idr bool, ref uint32, frameNum int) {
	s := &e.sliceParam
	*s = sys.EncSliceParameterBufferH264{}
	s.NumMacroblocks = uint32(e.mbW * e.mbH)
	s.MacroblockInfo = sys.InvalidID
	s.SliceType = uint8(sh.SliceTypeRaw)
	s.IdrPicID = uint16(sh.IdrPicID)
	s.PicOrderCntLsb = uint16(sh.PicOrderCntLsb)
	s.DirectSpatialMvPredFlag = 1
	for i := range s.RefPicList0 {
		s.RefPicList0[i] = sys.InvalidPictureH264
		s.RefPicList1[i] = sys.InvalidPictureH264
	}
	if !idr && e.haveRef {
		s.RefPicList0[0] = e.refPicture(ref)
	}
	s.SliceQpDelta = int8(sh.SliceQpDelta)
	s.CabacInitIdc = uint8(sh.CabacInitIdc)
	s.DisableDeblockingFilterIdc = uint8(sh.DisableDeblockingFilterIdc)
}

func (e *encoder) Receive(ctx context.Context) (codec.Packet, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.Packet{}, codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return codec.Packet{}, err
	}
	if len(e.pending) == 0 {
		if e.flushed {
			return codec.Packet{}, io.EOF
		}
		return codec.Packet{}, codec.ErrAgain
	}
	p := e.pending[0]
	copy(e.pending, e.pending[1:])
	e.pending[len(e.pending)-1] = codec.Packet{}
	e.pending = e.pending[:len(e.pending)-1]
	return p, nil
}

func (e *encoder) Flush(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Every picture is complete when Send returns, so nothing is in flight.
	e.flushed = true
	e.forceNext = true
	e.frameIndex = 0
	return nil
}

func (e *encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	e.pending = nil
	e.teardown()
	e.dpy.close()
	return nil
}
