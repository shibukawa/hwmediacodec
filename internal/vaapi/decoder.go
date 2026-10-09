//go:build linux

package vaapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

// outputSlack is the number of decoded pictures that may wait for Receive
// beyond the reference pictures and the picture being decoded before Send
// reports ErrAgain.
const outputSlack = 4

// surface is one VA surface of the decode pool and its ownership state. A
// surface is free when it is neither held by the DPB nor waiting for
// Receive.
type surface struct {
	id            uint32
	inDPB         bool
	pendingOutput bool
	dummy         bool // shared stand-in for non-existing (frame_num gap) frames

	pts                 int64
	order               codec.Order
	cropX, cropY        int
	width, height       int
	codedW, codedH      int
	deriveTried, derive bool
}

// surfaceID implements vaSurface.
func (s *surface) surfaceID() uint32 { return s.id }

// sequence is the VA configuration, context and surface pool created for
// one SPS (profile, size, reference count).
type sequence struct {
	profile  int32
	config   uint32
	context  uint32
	width    int // coded size
	height   int
	maxRefs  int
	surfaces []*surface
	dummy    *surface
	ids      []uint32
	// image is a cached NV12 VAImage for the vaGetImage path.
	image    sys.Image
	hasImage bool
}

type decoder struct {
	mu   sync.Mutex
	cfg  codec.DecoderConfig
	dpy  *display
	ps   *h264.ParameterSets
	dpb  *h264.DPB
	seq  *sequence
	pool sync.Pool

	waitKeyframe bool
	flushed      bool
	closed       bool
	deriveWorks  int // 0 unknown, 1 vaDeriveImage returns NV12, -1 use vaGetImage

	pending []*surface // decoded pictures in decode order

	// Scratch buffers reused across pictures.
	picParam sys.PictureParameterBufferH264
	iqMatrix sys.IQMatrixBufferH264
	sliceBuf []sys.SliceParameterBufferH264
	bufIDs   []uint32

	// hevc holds the HEVC bitstream state; it is nil for H.264 decoders.
	hevc *hevcDecoder
}

func newDecoder(cfg codec.DecoderConfig, dpy *display) *decoder {
	d := &decoder{cfg: cfg, dpy: dpy, ps: h264.NewParameterSets(), waitKeyframe: true}
	d.dpb = h264.NewDPB(d)
	if cfg.Codec == codec.HEVC {
		d.hevc = newHEVCDecoder(d)
	}
	return d
}

// Allocate implements h264.Surfaces for non-existing frames. They are never
// output and a conforming stream never predicts from them, so they all share
// one surface.
func (d *decoder) Allocate() (any, error) {
	if d.seq == nil {
		return nil, errors.New("vaapi: no sequence")
	}
	return d.seq.dummy, nil
}

// Release implements h264.Surfaces.
func (d *decoder) Release(p *h264.Picture) {
	if s, ok := p.Handle.(*surface); ok && !s.dummy {
		s.inDPB = false
	}
}

func (d *decoder) freeSurface() *surface {
	if d.seq == nil {
		return nil
	}
	for _, s := range d.seq.surfaces {
		if !s.inDPB && !s.pendingOutput {
			return s
		}
	}
	return nil
}

func (d *decoder) Send(ctx context.Context, p codec.Packet) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.flushed = false

	nals := annexb.Split(p.Data)
	if len(nals) == 0 {
		return codec.ErrInvalidData
	}
	if d.hevc != nil {
		return d.sendHEVC(p, nals)
	}
	var slices [][]byte
	for _, nal := range nals {
		_, typ, ok := h264.NALHeader(nal)
		if !ok {
			continue
		}
		switch typ {
		case h264.NALSPS:
			if _, err := d.ps.AddSPS(nal); err != nil {
				return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
			}
		case h264.NALPPS:
			if err := d.ps.AddPPS(nal); err != nil {
				return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
			}
		case h264.NALSlice, h264.NALSliceIDR:
			slices = append(slices, nal)
		case h264.NALSliceDPA, h264.NALSliceDPB, h264.NALSliceDPC:
			return unsupported(d.cfg.Codec, "data partitioned slices (Extended profile) are not supported")
		}
	}
	if len(slices) == 0 {
		return nil
	}

	// Parse every slice header up front: it validates the access unit
	// before any state changes, and redundant coded pictures (optional
	// duplicates of the primary slices) are dropped here.
	var headers []*h264.SliceHeader
	var sps *h264.SPS
	var pps *h264.PPS
	kept := slices[:0]
	for i, nal := range slices {
		sh, s, p, err := h264.ParseSliceHeader(nal, d.ps)
		if err != nil {
			if errors.Is(err, h264.ErrMissingSPS) || errors.Is(err, h264.ErrMissingPPS) {
				// Parameter sets have not arrived yet; skip until they do.
				return nil
			}
			return fmt.Errorf("%w: slice %d: %v", codec.ErrInvalidData, i, err)
		}
		if sh.RedundantPicCnt > 0 {
			continue
		}
		if len(headers) > 0 && (sh.FrameNum != headers[0].FrameNum || sh.PPSID != headers[0].PPSID || sh.IDR != headers[0].IDR) {
			return fmt.Errorf("%w: slice %d belongs to a different picture", codec.ErrInvalidData, i)
		}
		headers = append(headers, sh)
		kept = append(kept, nal)
		sps, pps = s, p
	}
	slices = kept
	if len(headers) == 0 {
		return nil
	}
	sh := headers[0]
	if d.waitKeyframe {
		if !sh.IDR {
			return nil
		}
	}
	if err := checkStream(sps, pps, sh); err != nil {
		return err
	}
	if d.seq == nil || d.seq.needsReset(sps) {
		if !sh.IDR {
			// A new sequence must start at an IDR picture.
			d.waitKeyframe = true
			return nil
		}
		if len(d.pending) > 0 {
			// Frames of the old sequence are still waiting for Receive and
			// hold its surfaces; the caller must drain them first.
			return codec.ErrAgain
		}
		d.dpb.Reset()
		candidates := append([]int32{profileFor(sps, -1)}, h264Profiles...)
		if err := d.setupSequence(candidates, sps.CodedWidth(), sps.CodedHeight(), refCount(sps),
			fmt.Sprintf("profile_idc %d", sps.ProfileIDC)); err != nil {
			return err
		}
	}
	s := d.freeSurface()
	if s == nil {
		return codec.ErrAgain
	}
	d.waitKeyframe = false

	cur, err := d.dpb.Start(sps, sh, s)
	if err != nil {
		switch {
		case errors.Is(err, h264.ErrFieldCoding):
			return unsupported(d.cfg.Codec, "interlaced field pictures are not supported")
		case errors.Is(err, h264.ErrNoKeyframe):
			d.waitKeyframe = true
			return nil
		}
		return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
	}
	s.inDPB = cur.Reference
	s.pendingOutput = true
	s.pts = p.PTS
	s.order = codec.PacketOrder(p)
	s.cropX, s.cropY, s.width, s.height = sps.Crop()
	s.codedW, s.codedH = sps.CodedWidth(), sps.CodedHeight()

	decodeErr := d.decodePicture(sps, pps, headers, cur, slices)
	if err := d.dpb.Finish(sh); err != nil && decodeErr == nil {
		decodeErr = err
	}
	if decodeErr != nil {
		s.pendingOutput = false
		return decodeErr
	}
	d.pending = append(d.pending, s)
	return nil
}

// checkStream rejects stream features the backend does not handle.
func checkStream(sps *h264.SPS, pps *h264.PPS, sh *h264.SliceHeader) error {
	switch {
	case sps.ChromaFormatIDC != 1 || sps.SeparateColourPlane:
		return unsupported(codec.H264, fmt.Sprintf("chroma_format_idc %d is not supported; only 4:2:0", sps.ChromaFormatIDC))
	case sps.BitDepthLuma != 8 || sps.BitDepthChroma != 8:
		return unsupported(codec.H264, fmt.Sprintf("%d-bit video is not supported; only 8-bit", sps.BitDepthLuma))
	case sh.FieldPic:
		return unsupported(codec.H264, "interlaced field pictures are not supported")
	case pps.NumSliceGroups > 1:
		return unsupported(codec.H264, "flexible macroblock ordering (slice groups) is not supported")
	}
	return nil
}

func (s *sequence) needsReset(sps *h264.SPS) bool {
	return s.width != sps.CodedWidth() || s.height != sps.CodedHeight() ||
		s.maxRefs < refCount(sps) || s.profile != profileFor(sps, s.profile)
}

func refCount(sps *h264.SPS) int {
	n := int(sps.MaxNumRefFrames)
	if n < 1 {
		n = 1
	}
	if n > 16 {
		n = 16
	}
	return n
}

// profileFor picks the VA profile for an SPS; current is returned when it
// still fits so that a compatible SPS change does not force a reset.
func profileFor(sps *h264.SPS, current int32) int32 {
	var want []int32
	switch sps.ProfileIDC {
	case 66:
		want = []int32{sys.ProfileH264ConstrainedBaseline, sys.ProfileH264Main, sys.ProfileH264High}
	case 77:
		want = []int32{sys.ProfileH264Main, sys.ProfileH264High}
	default:
		want = []int32{sys.ProfileH264High}
	}
	for _, p := range want {
		if p == current {
			return current
		}
	}
	return want[0]
}

// setupSequence creates the VA configuration, the surface pool and the
// context for a coded size. candidates lists the VA profiles that can decode
// the stream in order of preference; maxRefs is the number of reference
// pictures the stream keeps besides the one being decoded.
func (d *decoder) setupSequence(candidates []int32, width, height, maxRefs int, stream string) error {
	d.teardownSequence()

	var profile int32 = -1
	for _, p := range candidates {
		if d.dpy.decodeProfiles[p] {
			profile = p
			break
		}
	}
	if profile < 0 {
		return unsupported(d.cfg.Codec, stream+" has no VA-API decode profile on this driver")
	}
	attr := sys.ConfigAttrib{Type: sys.ConfigAttribRTFormat}
	if st := sys.GetConfigAttributes(d.dpy.dpy, profile, sys.EntrypointVLD, &attr, 1); st != sys.StatusSuccess {
		return vaError("vaGetConfigAttributes", st)
	}
	if attr.Value == sys.AttribNotSupported || attr.Value&sys.RTFormatYUV420 == 0 {
		return unsupported(d.cfg.Codec, "the VA-API driver does not decode to 4:2:0 8-bit surfaces")
	}
	seq := &sequence{profile: profile, width: width, height: height, maxRefs: maxRefs}
	attr.Value = sys.RTFormatYUV420
	if st := sys.CreateConfig(d.dpy.dpy, profile, sys.EntrypointVLD, &attr, 1, &seq.config); st != sys.StatusSuccess {
		if st == sys.StatusErrorUnsupportedProfile || st == sys.StatusErrorUnsupportedEntrypoint || st == sys.StatusErrorUnsupportedRTFormat {
			return unsupported(d.cfg.Codec, "vaCreateConfig: "+statusMessage(st))
		}
		return vaError("vaCreateConfig", st)
	}
	n := seq.maxRefs + 1 + outputSlack + 1 // + dummy surface for gap frames
	seq.ids = make([]uint32, n)
	pixfmt := sys.IntegerAttrib(sys.SurfaceAttribPixelFormat, int32(sys.FourccNV12))
	if st := sys.CreateSurfaces(d.dpy.dpy, sys.RTFormatYUV420, uint32(seq.width), uint32(seq.height), &seq.ids[0], uint32(n), &pixfmt, 1); st != sys.StatusSuccess {
		sys.DestroyConfig(d.dpy.dpy, seq.config)
		if st == sys.StatusErrorResolutionNotSupported {
			return unsupported(d.cfg.Codec, fmt.Sprintf("%dx%d: %s", seq.width, seq.height, statusMessage(st)))
		}
		return vaError("vaCreateSurfaces", st)
	}
	for _, id := range seq.ids[:n-1] {
		seq.surfaces = append(seq.surfaces, &surface{id: id})
	}
	seq.dummy = &surface{id: seq.ids[n-1], dummy: true}
	if st := sys.CreateContext(d.dpy.dpy, seq.config, int32(seq.width), int32(seq.height), sys.Progressive, &seq.ids[0], int32(n), &seq.context); st != sys.StatusSuccess {
		sys.DestroySurfaces(d.dpy.dpy, &seq.ids[0], int32(n))
		sys.DestroyConfig(d.dpy.dpy, seq.config)
		return vaError("vaCreateContext", st)
	}
	d.seq = seq
	return nil
}

func (d *decoder) teardownSequence() {
	seq := d.seq
	if seq == nil {
		return
	}
	d.seq = nil
	dpy := d.dpy.dpy
	if seq.hasImage {
		sys.DestroyImage(dpy, seq.image.ImageID)
	}
	if seq.context != 0 {
		sys.DestroyContext(dpy, seq.context)
	}
	if len(seq.ids) > 0 {
		sys.DestroySurfaces(dpy, &seq.ids[0], int32(len(seq.ids)))
	}
	if seq.config != 0 {
		sys.DestroyConfig(dpy, seq.config)
	}
}

// decodePicture submits one picture (all its slices) to the driver.
func (d *decoder) decodePicture(sps *h264.SPS, pps *h264.PPS, headers []*h264.SliceHeader, cur *h264.Picture, slices [][]byte) error {
	first := headers[0]
	d.bufIDs = d.bufIDs[:0]
	defer d.destroyBuffers()
	create := d.createBuffer

	fillPictureParameters(&d.picParam, sps, pps, first, cur, d.dpb.Refs())
	if err := create(sys.PictureParameterBufferType, int(unsafe.Sizeof(d.picParam)), unsafe.Pointer(&d.picParam)); err != nil {
		return err
	}
	fillIQMatrix(&d.iqMatrix, &pps.ScalingLists)
	if err := create(sys.IQMatrixBufferType, int(unsafe.Sizeof(d.iqMatrix)), unsafe.Pointer(&d.iqMatrix)); err != nil {
		return err
	}

	if cap(d.sliceBuf) < len(slices) {
		d.sliceBuf = make([]sys.SliceParameterBufferH264, len(slices))
	}
	d.sliceBuf = d.sliceBuf[:len(slices)]
	for i, nal := range slices {
		sh := headers[i]
		l0, l1 := d.dpb.RefPicLists(sh)
		sp := &d.sliceBuf[i]
		fillSliceParameters(sp, sh, len(nal), l0, l1)
		if err := create(sys.SliceParameterBufferType, int(unsafe.Sizeof(*sp)), unsafe.Pointer(sp)); err != nil {
			return err
		}
		if err := create(sys.SliceDataBufferType, len(nal), unsafe.Pointer(&nal[0])); err != nil {
			return err
		}
	}

	return d.renderBuffers(cur.Handle.(*surface))
}

// createBuffer creates one VA buffer for the picture being assembled; the
// buffers are destroyed by destroyBuffers.
func (d *decoder) createBuffer(typ int32, size int, data unsafe.Pointer) error {
	var id uint32
	if st := sys.CreateBuffer(d.dpy.dpy, d.seq.context, typ, uint32(size), 1, data, &id); st != sys.StatusSuccess {
		return vaError("vaCreateBuffer", st)
	}
	d.bufIDs = append(d.bufIDs, id)
	return nil
}

func (d *decoder) destroyBuffers() {
	for _, id := range d.bufIDs {
		sys.DestroyBuffer(d.dpy.dpy, id)
	}
	d.bufIDs = d.bufIDs[:0]
}

// renderBuffers submits the buffers created for one picture to the driver,
// decoding into s.
func (d *decoder) renderBuffers(s *surface) error {
	dpy, ctx := d.dpy.dpy, d.seq.context
	if st := sys.BeginPicture(dpy, ctx, s.id); st != sys.StatusSuccess {
		return vaError("vaBeginPicture", st)
	}
	if st := sys.RenderPicture(dpy, ctx, &d.bufIDs[0], int32(len(d.bufIDs))); st != sys.StatusSuccess {
		sys.EndPicture(dpy, ctx)
		return vaError("vaRenderPicture", st)
	}
	if st := sys.EndPicture(dpy, ctx); st != sys.StatusSuccess {
		return vaError("vaEndPicture", st)
	}
	return nil
}

func (d *decoder) Receive(ctx context.Context) (*codec.Frame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(d.pending) == 0 {
		if d.flushed {
			return nil, io.EOF
		}
		return nil, codec.ErrAgain
	}
	s := d.pending[0]
	copy(d.pending, d.pending[1:])
	d.pending = d.pending[:len(d.pending)-1]
	frame, err := d.copySurface(s)
	s.pendingOutput = false
	return frame, err
}

// copySurface waits for the picture and copies it into an NV12 frame in
// CPU memory, cropping to the display size.
func (d *decoder) copySurface(s *surface) (*codec.Frame, error) {
	dpy := d.dpy.dpy
	if st := sys.SyncSurface(dpy, s.id); st != sys.StatusSuccess {
		return nil, vaError("vaSyncSurface", st)
	}
	var img sys.Image
	derived := false
	if d.deriveWorks >= 0 {
		if st := sys.DeriveImage(dpy, s.id, &img); st == sys.StatusSuccess {
			if img.Format.Fourcc == sys.FourccNV12 && img.NumPlanes == 2 {
				derived = true
				d.deriveWorks = 1
			} else {
				sys.DestroyImage(dpy, img.ImageID)
				d.deriveWorks = -1
			}
		} else {
			d.deriveWorks = -1
		}
	}
	if !derived {
		seq := d.seq
		if seq == nil {
			return nil, errors.New("vaapi: sequence torn down with frames pending")
		}
		if !seq.hasImage || int(seq.image.Width) != s.codedW || int(seq.image.Height) != s.codedH {
			if seq.hasImage {
				sys.DestroyImage(dpy, seq.image.ImageID)
				seq.hasImage = false
			}
			format := sys.ImageFormat{Fourcc: sys.FourccNV12, ByteOrder: sys.LSBFirst, BitsPerPixel: 12}
			if st := sys.CreateImage(dpy, &format, int32(s.codedW), int32(s.codedH), &seq.image); st != sys.StatusSuccess {
				return nil, vaError("vaCreateImage", st)
			}
			seq.hasImage = true
		}
		if st := sys.GetImage(dpy, s.id, 0, 0, uint32(s.codedW), uint32(s.codedH), seq.image.ImageID); st != sys.StatusSuccess {
			return nil, vaError("vaGetImage", st)
		}
		img = seq.image
	}
	if derived {
		defer sys.DestroyImage(dpy, img.ImageID)
	}

	var base *byte
	if st := sys.MapBuffer(dpy, img.Buf, &base); st != sys.StatusSuccess {
		return nil, vaError("vaMapBuffer", st)
	}
	defer sys.UnmapBuffer(dpy, img.Buf)
	if base == nil || img.NumPlanes < 2 {
		return nil, &codec.BackendError{Backend: Name, Op: "vaMapBuffer", Message: "mapped image has no planes"}
	}
	src := unsafe.Slice(base, int(img.DataSize))

	width, height := s.width, s.height
	rowBytes := [2]int{width, (width + 1) / 2 * 2}
	rows := [2]int{height, (height + 1) / 2}
	total := rowBytes[0]*rows[0] + rowBytes[1]*rows[1]
	buf := d.getBuffer(total)
	planes := make([][]byte, 2)
	strides := make([]int, 2)
	off := 0
	for i := 0; i < 2; i++ {
		pitch := int(img.Pitches[i])
		start := int(img.Offsets[i])
		x, y := s.cropX, s.cropY
		if i == 1 {
			y /= 2
		}
		dst := buf[off : off+rowBytes[i]*rows[i]]
		for r := 0; r < rows[i]; r++ {
			so := start + (y+r)*pitch + x
			if so+rowBytes[i] > len(src) {
				d.putBuffer(buf)
				return nil, &codec.BackendError{Backend: Name, Op: "copy frame", Message: "image smaller than the picture"}
			}
			copy(dst[r*rowBytes[i]:(r+1)*rowBytes[i]], src[so:so+rowBytes[i]])
		}
		planes[i] = dst
		strides[i] = rowBytes[i]
		off += rowBytes[i] * rows[i]
	}
	f := &codec.Frame{Width: width, Height: height, Format: codec.NV12, Planes: planes, Strides: strides, PTS: s.pts}
	codec.SetFrameOrder(f, s.order)
	codec.SetRelease(f, func() { d.putBuffer(buf) })
	return f, nil
}

func (d *decoder) getBuffer(n int) []byte {
	if b, ok := d.pool.Get().(*[]byte); ok && cap(*b) >= n {
		return (*b)[:n]
	}
	return make([]byte, n)
}

func (d *decoder) putBuffer(b []byte) {
	d.pool.Put(&b)
}

func (d *decoder) Flush(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Decoding is synchronous per picture, so nothing is in flight; frames
	// already decoded stay queued for Receive. Drop the references so that
	// decoding restarts at the next IDR (HEVC: IRAP) picture.
	d.dpb.Reset()
	if d.hevc != nil {
		d.hevc.dpb.Reset()
	}
	d.flushed = true
	d.waitKeyframe = true
	return nil
}

func (d *decoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.dpb.Reset()
	if d.hevc != nil {
		d.hevc.dpb.Reset()
	}
	d.pending = nil
	d.teardownSequence()
	d.dpy.close()
	return nil
}
