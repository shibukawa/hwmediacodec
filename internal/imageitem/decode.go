package imageitem

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"sync"

	"github.com/Eyevinn/mp4ff/av1"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	hevcps "github.com/shibukawa/hwmediacodec/internal/hevc"
)

// Info describes a decoded file.
type Info struct {
	Codec    hwmediacodec.Codec
	Width    int // of the displayed picture, after rotation
	Height   int
	Tiles    int // 1 for a single coded picture, more for a grid
	Rotation int // degrees counter-clockwise applied from irot
	Mirror   bool
}

type property struct {
	typ       string
	data      []byte
	essential bool
}

type item struct {
	id      uint32
	typ     string
	hidden  bool
	method  uint8 // iloc construction method: 0 file offsets, 1 idat offsets
	extents [][2]uint64
	props   []property
	dimg    []uint32 // derived-image sources (grid tiles), in order
}

type file struct {
	data    []byte
	idat    []byte
	primary uint32
	items   map[uint32]*item
	order   []uint32
}

// name is how a codec's files are called in messages.
func name(c hwmediacodec.Codec) string {
	if c == hwmediacodec.AV1 {
		return "AVIF (AV1)"
	}
	return "HEIC (HEVC)"
}

// wrongCodec is the error for a file whose pictures are coded with the
// codec of the other package.
func wrongCodec(got, want hwmediacodec.Codec) error {
	pkg := "image/heif"
	if got == hwmediacodec.AV1 {
		pkg = "image/avif"
	}
	return fmt.Errorf("the file is %s, not %s; use github.com/shibukawa/hwmediacodec/%s", name(got), name(want), pkg)
}

var (
	registerMu sync.Mutex
	registered = map[hwmediacodec.Codec]bool{}
	genericSet bool
)

// Register is called by the public packages from init: it notes that
// codec c is linked into the program and registers the generic brands
// (mif1, msf1), which do not say how the pictures are coded, with the
// image package once. Files with these brands are decoded when the
// package for their codec is linked.
func Register(c hwmediacodec.Codec) {
	registerMu.Lock()
	defer registerMu.Unlock()
	registered[c] = true
	if genericSet {
		return
	}
	genericSet = true
	for _, b := range []string{"mif1", "msf1"} {
		image.RegisterFormat("heif", "????ftyp"+b, decodeGeneric, decodeConfigGeneric)
	}
}

func linked(c hwmediacodec.Codec) error {
	registerMu.Lock()
	defer registerMu.Unlock()
	if registered[c] {
		return nil
	}
	pkg := "image/heif"
	if c == hwmediacodec.AV1 {
		pkg = "image/avif"
	}
	return fmt.Errorf("the file is %s; import github.com/shibukawa/hwmediacodec/%s to decode it", name(c), pkg)
}

func decodeGeneric(r io.Reader) (image.Image, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	info, err := DecodeInfo(data, 0)
	if err != nil {
		return nil, fmt.Errorf("heif: %w", err)
	}
	if err := linked(info.Codec); err != nil {
		return nil, fmt.Errorf("heif: %w", err)
	}
	img, _, err := DecodeBytes(data, info.Codec)
	if err != nil {
		return nil, fmt.Errorf("heif: %w", err)
	}
	return img, nil
}

func decodeConfigGeneric(r io.Reader) (image.Config, error) {
	cfg, err := DecodeConfig(r, 0)
	if err != nil {
		return cfg, fmt.Errorf("heif: %w", err)
	}
	return cfg, nil
}

// Decode reads a file from r and returns its primary image; see
// DecodeBytes for only.
func Decode(r io.Reader, only hwmediacodec.Codec) (image.Image, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	img, _, err := DecodeBytes(data, only)
	if err != nil {
		return nil, err
	}
	return img, nil
}

// DecodeConfig returns the colour model and the dimensions of the image
// Decode would return, without decoding it.
func DecodeConfig(r io.Reader, only hwmediacodec.Codec) (image.Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return image.Config{}, err
	}
	info, err := DecodeInfo(data, only)
	if err != nil {
		return image.Config{}, err
	}
	return image.Config{ColorModel: color.RGBAModel, Width: info.Width, Height: info.Height}, nil
}

// DecodeInfo describes the primary image of a file held in data from the
// file structure alone: nothing is decoded. A non-zero only rejects files
// coded with another codec.
func DecodeInfo(data []byte, only hwmediacodec.Codec) (*Info, error) {
	f, err := parse(data)
	if err != nil {
		return nil, err
	}
	prim, ok := f.items[f.primary]
	if !ok {
		return nil, fmt.Errorf("primary item %d not found", f.primary)
	}
	info := &Info{Tiles: 1}
	coded := prim
	var w, h int
	switch prim.typ {
	case "hvc1", "hev1", "av01":
		if w, h, ok = prim.ispe(); !ok {
			return nil, fmt.Errorf("item %d has no ispe property", prim.id)
		}
	case "grid":
		g, err := f.grid(prim)
		if err != nil {
			return nil, err
		}
		w, h, info.Tiles, coded = g.outW, g.outH, len(g.tiles), g.tiles[0]
	default:
		return nil, fmt.Errorf("primary item is a %q, which this decoder does not handle", prim.typ)
	}
	switch coded.typ {
	case "hvc1", "hev1":
		info.Codec = hwmediacodec.HEVC
	case "av01":
		info.Codec = hwmediacodec.AV1
	default:
		return nil, fmt.Errorf("grid tiles are %q items, which this decoder does not handle", coded.typ)
	}
	for _, p := range prim.props {
		switch p.typ {
		case "clap":
			rect, err := clapRect(w, h, p.data)
			if err != nil {
				return nil, err
			}
			w, h = rect.Dx(), rect.Dy()
		case "irot":
			if len(p.data) > 0 {
				angle := int(p.data[0]&3) * 90
				if angle%180 != 0 {
					w, h = h, w
				}
				info.Rotation = (info.Rotation + angle) % 360
			}
		case "imir":
			if len(p.data) > 0 {
				info.Mirror = !info.Mirror
			}
		}
	}
	if only != 0 && info.Codec != only {
		return nil, wrongCodec(info.Codec, only)
	}
	info.Width, info.Height = w, h
	return info, nil
}

// DecodeBytes decodes the primary image of a file held in data. Grid
// images are decoded tile by tile and stitched; clap, irot and imir are
// applied. The result is opaque RGBA. A non-zero only rejects files coded
// with another codec before a decoder is opened. The options are passed
// to the decoder.
func DecodeBytes(data []byte, only hwmediacodec.Codec, opts ...hwmediacodec.DecoderOption) (*image.RGBA, *Info, error) {
	if only != 0 {
		if _, err := DecodeInfo(data, only); err != nil {
			return nil, nil, err
		}
	}
	f, err := parse(data)
	if err != nil {
		return nil, nil, err
	}
	prim, ok := f.items[f.primary]
	if !ok {
		return nil, nil, fmt.Errorf("primary item %d not found", f.primary)
	}
	info := &Info{Tiles: 1}
	var img *image.RGBA
	switch prim.typ {
	case "hvc1", "hev1", "av01":
		img, info.Codec, err = f.decodeCoded([]*item{prim}, nil, opts)
	case "grid":
		img, info.Codec, info.Tiles, err = f.decodeGrid(prim, opts)
	default:
		return nil, nil, fmt.Errorf("primary item is a %q, which this decoder does not handle", prim.typ)
	}
	if err != nil {
		return nil, nil, err
	}
	// Transformative properties apply in their ipma order.
	for _, p := range prim.props {
		switch p.typ {
		case "clap":
			img, err = cleanAperture(img, p.data)
			if err != nil {
				return nil, nil, err
			}
		case "irot":
			if len(p.data) > 0 {
				angle := int(p.data[0]&3) * 90
				img = rotate(img, angle)
				info.Rotation = (info.Rotation + angle) % 360
			}
		case "imir":
			if len(p.data) > 0 {
				img = mirror(img, p.data[0]&1 == 1)
				info.Mirror = !info.Mirror
			}
		}
	}
	info.Width, info.Height = img.Rect.Dx(), img.Rect.Dy()
	return img, info, nil
}

// parse reads the meta box into items and properties.
func parse(data []byte) (*file, error) {
	top, err := parseBoxes(data, 0)
	if err != nil {
		return nil, err
	}
	ftyp, ok := find(top, "ftyp")
	if !ok || len(ftyp.data) < 8 {
		return nil, errors.New("no ftyp box")
	}
	meta, ok := find(top, "meta")
	if !ok {
		return nil, errors.New("no meta box")
	}
	_, _, metaPayload, err := fullBox(meta)
	if err != nil {
		return nil, err
	}
	children, err := parseBoxes(metaPayload, meta.start+4)
	if err != nil {
		return nil, err
	}
	f := &file{data: data, items: map[uint32]*item{}}
	if hdlr, ok := find(children, "hdlr"); ok {
		if _, _, p, err := fullBox(hdlr); err == nil && len(p) >= 8 && string(p[4:8]) != "pict" {
			return nil, fmt.Errorf("handler is %q, not a picture file", p[4:8])
		}
	}
	if b, ok := find(children, "pitm"); ok {
		v, _, p, err := fullBox(b)
		if err != nil {
			return nil, err
		}
		r := &reader{b: p}
		if v == 0 {
			f.primary = uint32(r.u16())
		} else {
			f.primary = r.u32()
		}
	}
	if b, ok := find(children, "idat"); ok {
		f.idat = b.data
	}
	if b, ok := find(children, "iinf"); ok {
		if err := f.parseIinf(b); err != nil {
			return nil, err
		}
	}
	if b, ok := find(children, "iloc"); ok {
		if err := f.parseIloc(b); err != nil {
			return nil, err
		}
	}
	if b, ok := find(children, "iref"); ok {
		if err := f.parseIref(b); err != nil {
			return nil, err
		}
	}
	if b, ok := find(children, "iprp"); ok {
		if err := f.parseIprp(b); err != nil {
			return nil, err
		}
	}
	if len(f.items) == 0 {
		return nil, errors.New("no items")
	}
	return f, nil
}

func (f *file) item(id uint32) *item {
	it, ok := f.items[id]
	if !ok {
		it = &item{id: id}
		f.items[id] = it
		f.order = append(f.order, id)
	}
	return it
}

func (f *file) parseIinf(b box) error {
	v, _, p, err := fullBox(b)
	if err != nil {
		return err
	}
	r := &reader{b: p}
	var count int
	if v == 0 {
		count = int(r.u16())
	} else {
		count = int(r.u32())
	}
	if r.err != nil {
		return r.err
	}
	entries, err := parseBoxes(p[r.off:], 0)
	if err != nil {
		return err
	}
	for i, e := range entries {
		if e.typ != "infe" || i >= count {
			continue
		}
		ev, flags, ep, err := fullBox(e)
		if err != nil {
			return err
		}
		er := &reader{b: ep}
		var id uint32
		switch ev {
		case 2:
			id = uint32(er.u16())
		case 3:
			id = er.u32()
		default:
			return fmt.Errorf("infe version %d is not supported", ev)
		}
		er.u16() // protection index
		typ := string(er.bytes(4))
		if er.err != nil {
			return er.err
		}
		it := f.item(id)
		it.typ = typ
		it.hidden = flags&1 == 1
	}
	return nil
}

func (f *file) parseIloc(b box) error {
	v, _, p, err := fullBox(b)
	if err != nil {
		return err
	}
	r := &reader{b: p}
	sizes := r.u8()
	offsetSize, lengthSize := int(sizes>>4), int(sizes&15)
	sizes = r.u8()
	baseOffsetSize := int(sizes >> 4)
	indexSize := 0
	if v == 1 || v == 2 {
		indexSize = int(sizes & 15)
	}
	var count int
	if v < 2 {
		count = int(r.u16())
	} else {
		count = int(r.u32())
	}
	for i := 0; i < count && r.err == nil; i++ {
		var id uint32
		if v < 2 {
			id = uint32(r.u16())
		} else {
			id = r.u32()
		}
		it := f.item(id)
		if v == 1 || v == 2 {
			it.method = uint8(r.u16() & 15)
		}
		r.u16() // data reference index
		base := r.uint(baseOffsetSize)
		n := int(r.u16())
		for k := 0; k < n && r.err == nil; k++ {
			if indexSize > 0 {
				r.uint(indexSize)
			}
			off := r.uint(offsetSize)
			length := r.uint(lengthSize)
			it.extents = append(it.extents, [2]uint64{base + off, length})
		}
	}
	return r.err
}

func (f *file) parseIref(b box) error {
	v, _, p, err := fullBox(b)
	if err != nil {
		return err
	}
	refs, err := parseBoxes(p, 0)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if ref.typ != "dimg" {
			continue
		}
		r := &reader{b: ref.data}
		read := func() uint32 {
			if v == 0 {
				return uint32(r.u16())
			}
			return r.u32()
		}
		from := read()
		n := int(read())
		it := f.item(from)
		for i := 0; i < n && r.err == nil; i++ {
			it.dimg = append(it.dimg, read())
		}
		if r.err != nil {
			return r.err
		}
	}
	return nil
}

func (f *file) parseIprp(b box) error {
	children, err := parseBoxes(b.data, 0)
	if err != nil {
		return err
	}
	ipco, ok := find(children, "ipco")
	if !ok {
		return errors.New("iprp without ipco")
	}
	props, err := parseBoxes(ipco.data, 0)
	if err != nil {
		return err
	}
	for _, c := range children {
		if c.typ != "ipma" {
			continue
		}
		v, flags, p, err := fullBox(c)
		if err != nil {
			return err
		}
		r := &reader{b: p}
		count := int(r.u32())
		for i := 0; i < count && r.err == nil; i++ {
			var id uint32
			if v < 1 {
				id = uint32(r.u16())
			} else {
				id = r.u32()
			}
			it := f.item(id)
			n := int(r.u8())
			for k := 0; k < n && r.err == nil; k++ {
				var essential bool
				var index int
				if flags&1 == 1 {
					x := r.u16()
					essential, index = x&0x8000 != 0, int(x&0x7fff)
				} else {
					x := r.u8()
					essential, index = x&0x80 != 0, int(x&0x7f)
				}
				if index == 0 {
					continue
				}
				if index > len(props) {
					return fmt.Errorf("item %d refers to property %d of %d", id, index, len(props))
				}
				pb := props[index-1]
				it.props = append(it.props, property{typ: pb.typ, data: pb.data, essential: essential})
			}
		}
		if r.err != nil {
			return r.err
		}
	}
	return nil
}

// payload concatenates the extents of an item.
func (f *file) payload(it *item) ([]byte, error) {
	src := f.data
	if it.method == 1 {
		src = f.idat
	} else if it.method != 0 {
		return nil, fmt.Errorf("item %d uses construction method %d", it.id, it.method)
	}
	var out []byte
	for _, e := range it.extents {
		off, n := e[0], e[1]
		if off > uint64(len(src)) || n > uint64(len(src))-off {
			return nil, fmt.Errorf("item %d extent [%d, %d) is outside the data", it.id, off, off+n)
		}
		if len(it.extents) == 1 {
			return src[off : off+n], nil
		}
		out = append(out, src[off:off+n]...)
	}
	return out, nil
}

func (it *item) prop(typ string) ([]byte, bool) {
	for _, p := range it.props {
		if p.typ == typ {
			return p.data, true
		}
	}
	return nil, false
}

// ispe returns the declared size of an item.
func (it *item) ispe() (w, h int, ok bool) {
	p, ok := it.prop("ispe")
	if !ok || len(p) < 12 {
		return 0, 0, false
	}
	r := &reader{b: p[4:]}
	return int(r.u32()), int(r.u32()), true
}

// packet turns a coded item into the packet the decoder wants: Annex-B
// with the parameter sets from hvcC in front for HEVC, the temporal unit
// (with the sequence header from av1C when the item lacks one) for AV1.
func (f *file) packet(it *item) ([]byte, hwmediacodec.Codec, error) {
	data, err := f.payload(it)
	if err != nil {
		return nil, 0, err
	}
	switch it.typ {
	case "hvc1", "hev1":
		cfg, ok := it.prop("hvcC")
		if !ok {
			return nil, 0, fmt.Errorf("item %d has no hvcC", it.id)
		}
		rec, err := hevc.DecodeHEVCDecConfRec(cfg)
		if err != nil {
			return nil, 0, fmt.Errorf("item %d hvcC: %w", it.id, err)
		}
		out := make([]byte, 0, len(data)+256)
		for _, arr := range rec.NaluArrays {
			for _, nal := range arr.Nalus {
				out = append(out, 0, 0, 0, 1)
				out = append(out, nal...)
			}
		}
		n := int(rec.LengthSizeMinusOne) + 1
		for len(data) >= n {
			size := 0
			for i := 0; i < n; i++ {
				size = size<<8 | int(data[i])
			}
			data = data[n:]
			if size <= 0 || size > len(data) {
				return nil, 0, fmt.Errorf("item %d: NAL unit length %d out of range", it.id, size)
			}
			out = append(out, 0, 0, 0, 1)
			out = append(out, data[:size]...)
			data = data[size:]
		}
		return out, hwmediacodec.HEVC, nil
	case "av01":
		if !hasSequenceHeader(data) {
			cfg, ok := it.prop("av1C")
			if !ok {
				return nil, 0, fmt.Errorf("item %d has no av1C", it.id)
			}
			rec, err := av1.DecodeAV1CodecConfRec(cfg)
			if err != nil {
				return nil, 0, fmt.Errorf("item %d av1C: %w", it.id, err)
			}
			data = append(append([]byte(nil), rec.ConfigOBUs...), data...)
		}
		return data, hwmediacodec.AV1, nil
	}
	return nil, 0, fmt.Errorf("item %d is a %q, not a coded picture", it.id, it.typ)
}

// decodeCoded decodes items of one codec in order and hands each frame to
// place (or returns the single frame when place is nil).
func (f *file) decodeCoded(items []*item, place func(i int, img *image.RGBA) error, opts []hwmediacodec.DecoderOption) (*image.RGBA, hwmediacodec.Codec, error) {
	if len(items) == 0 {
		return nil, 0, errors.New("nothing to decode")
	}
	first, codec, err := f.packet(items[0])
	if err != nil {
		return nil, 0, err
	}
	ctx := context.Background()
	// Decode order: the pictures are all keyframes, and this way each one
	// comes out as soon as it is decoded instead of waiting in the
	// reorder buffer.
	// The decoder converts to RGB with the colours the bitstream declares.
	// When the bitstream declares none and the item does, the conversion
	// is done here from the decoder's NV12 instead.
	format := hwmediacodec.RGBA
	colours, convert := f.unsignalledColours(items[0])
	if convert {
		format = hwmediacodec.NV12
	}
	all := append([]hwmediacodec.DecoderOption{hwmediacodec.WithOutputFormat(format), hwmediacodec.WithDecodeOrder()}, opts...)
	dec, err := hwmediacodec.NewDecoder(ctx, codec, all...)
	if err != nil {
		return nil, 0, err
	}
	defer dec.Close()

	var single *image.RGBA
	got := 0
	drain := func() error {
		for {
			fr, err := dec.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			var img *image.RGBA
			if convert {
				img = nv12Image(fr, colours)
			} else {
				img = frameImage(fr)
			}
			fr.Release()
			if place == nil {
				single = img
			} else if err := place(got, img); err != nil {
				return err
			}
			got++
		}
	}
	for i, it := range items {
		pkt := first
		if i > 0 {
			var c hwmediacodec.Codec
			if pkt, c, err = f.packet(it); err != nil {
				return nil, 0, err
			} else if c != codec {
				return nil, 0, errors.New("tiles use different codecs")
			}
		}
		for {
			err := dec.Send(ctx, hwmediacodec.Packet{Data: pkt, PTS: int64(i) * 90000, Keyframe: true})
			if errors.Is(err, hwmediacodec.ErrAgain) {
				if err := drain(); err != nil {
					return nil, 0, err
				}
				continue
			}
			if err != nil {
				return nil, 0, fmt.Errorf("decode item %d: %w", it.id, err)
			}
			break
		}
		if err := drain(); err != nil {
			return nil, 0, err
		}
	}
	if err := dec.Flush(ctx); err != nil {
		return nil, 0, err
	}
	if err := drain(); err != nil {
		return nil, 0, err
	}
	if got != len(items) {
		return nil, 0, fmt.Errorf("decoded %d pictures for %d items", got, len(items))
	}
	return single, codec, nil
}

// gridLayout is the descriptor of a grid item: the tiles in row-major
// order and the size of the picture they are cropped to.
type gridLayout struct {
	rows, cols int
	outW, outH int
	tiles      []*item
}

func (f *file) grid(grid *item) (*gridLayout, error) {
	data, err := f.payload(grid)
	if err != nil {
		return nil, err
	}
	r := &reader{b: data}
	if v := r.u8(); v != 0 {
		return nil, fmt.Errorf("grid version %d", v)
	}
	flags := r.u8()
	g := &gridLayout{}
	g.rows, g.cols = int(r.u8())+1, int(r.u8())+1
	if flags&1 == 0 {
		g.outW, g.outH = int(r.u16()), int(r.u16())
	} else {
		g.outW, g.outH = int(r.u32()), int(r.u32())
	}
	if r.err != nil {
		return nil, r.err
	}
	if len(grid.dimg) != g.rows*g.cols {
		return nil, fmt.Errorf("grid of %dx%d references %d tiles", g.cols, g.rows, len(grid.dimg))
	}
	g.tiles = make([]*item, len(grid.dimg))
	for i, id := range grid.dimg {
		t, ok := f.items[id]
		if !ok {
			return nil, fmt.Errorf("grid tile %d missing", id)
		}
		g.tiles[i] = t
	}
	return g, nil
}

// decodeGrid decodes a grid derived image: tiles in row-major order,
// stitched and cropped to the grid's output size.
func (f *file) decodeGrid(grid *item, opts []hwmediacodec.DecoderOption) (*image.RGBA, hwmediacodec.Codec, int, error) {
	g, err := f.grid(grid)
	if err != nil {
		return nil, 0, 0, err
	}
	rows, cols, outW, outH, tiles := g.rows, g.cols, g.outW, g.outH, g.tiles
	var canvas *image.RGBA
	var tileW, tileH int
	place := func(i int, img *image.RGBA) error {
		if canvas == nil {
			tileW, tileH = img.Rect.Dx(), img.Rect.Dy()
			if tileW*cols < outW || tileH*rows < outH {
				return fmt.Errorf("%dx%d tiles of %dx%d do not cover %dx%d", cols, rows, tileW, tileH, outW, outH)
			}
			canvas = image.NewRGBA(image.Rect(0, 0, tileW*cols, tileH*rows))
		}
		if img.Rect.Dx() != tileW || img.Rect.Dy() != tileH {
			return fmt.Errorf("tile %d is %dx%d, others are %dx%d", i, img.Rect.Dx(), img.Rect.Dy(), tileW, tileH)
		}
		x0, y0 := (i%cols)*tileW, (i/cols)*tileH
		for y := 0; y < tileH; y++ {
			copy(canvas.Pix[(y0+y)*canvas.Stride+x0*4:], img.Pix[y*img.Stride:y*img.Stride+tileW*4])
		}
		return nil
	}
	_, codec, err := f.decodeCoded(tiles, place, opts)
	if err != nil {
		return nil, 0, 0, err
	}
	out := canvas.SubImage(image.Rect(0, 0, outW, outH)).(*image.RGBA)
	return crop(out), codec, len(tiles), nil
}

// cleanAperture crops img to a clap property: a width x height window
// whose centre is offset from the picture centre by (horizOff, vertOff),
// all as fractions.
func cleanAperture(img *image.RGBA, data []byte) (*image.RGBA, error) {
	rect, err := clapRect(img.Rect.Dx(), img.Rect.Dy(), data)
	if err != nil {
		return nil, err
	}
	return crop(img.SubImage(rect.Add(img.Rect.Min)).(*image.RGBA)), nil
}

// clapRect returns the window a clap property selects in a picture of
// width x height.
func clapRect(width, height int, data []byte) (image.Rectangle, error) {
	r := &reader{b: data}
	var v [8]int64
	for i := range v {
		v[i] = int64(int32(r.u32()))
	}
	if r.err != nil {
		return image.Rectangle{}, fmt.Errorf("clap: %w", r.err)
	}
	if v[1] == 0 || v[3] == 0 || v[5] == 0 || v[7] == 0 {
		return image.Rectangle{}, errors.New("clap with a zero denominator")
	}
	pw, ph := int64(width), int64(height)
	// Integer arithmetic on doubled coordinates: left = (pw - w)/2 + off.
	w, h := v[0]/v[1], v[2]/v[3]
	left := ((pw-w)*v[5] + 2*v[4]) / (2 * v[5]) // (pw-w)/2 + horizOffN/horizOffD
	top := ((ph-h)*v[7] + 2*v[6]) / (2 * v[7])
	if w <= 0 || h <= 0 || left < 0 || top < 0 || left+w > pw || top+h > ph {
		return image.Rectangle{}, fmt.Errorf("clap %dx%d at (%d, %d) does not fit %dx%d", w, h, left, top, pw, ph)
	}
	return image.Rect(int(left), int(top), int(left+w), int(top+h)), nil
}

// frameImage copies a decoded RGBA frame into an image.
func frameImage(fr *hwmediacodec.Frame) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, fr.Width, fr.Height))
	src, stride := fr.Planes[0], fr.Strides[0]
	for y := 0; y < fr.Height; y++ {
		copy(img.Pix[y*img.Stride:(y+1)*img.Stride], src[y*stride:y*stride+fr.Width*4])
	}
	return img
}

// nclx is the colour description of a colr property of type nclx
// (ISO/IEC 23091-2 code points).
type nclx struct {
	primaries, transfer, matrix uint16
	fullRange                   bool
}

// nclx returns the item's colr property of type nclx. An item may carry a
// second colr with an ICC profile, which is skipped.
func (it *item) nclx() (nclx, bool) {
	for _, p := range it.props {
		if p.typ != "colr" || len(p.data) < 11 || string(p.data[:4]) != "nclx" {
			continue
		}
		r := &reader{b: p.data[4:]}
		c := nclx{primaries: r.u16(), transfer: r.u16(), matrix: r.u16()}
		c.fullRange = r.u8()&0x80 != 0
		return c, true
	}
	return nclx{}, false
}

// unsignalledColours reports the colours to convert an HEVC item with when
// its bitstream does not describe them (no video_signal_type in the SPS,
// as some software encoders write) while its colr property does. A decoder
// left alone with such a stream assumes video range, which is wrong for
// the full-range pictures still-image encoders prefer. AV1 always carries
// its colour configuration in the sequence header.
func (f *file) unsignalledColours(it *item) (nclx, bool) {
	if it.typ != "hvc1" && it.typ != "hev1" {
		return nclx{}, false
	}
	c, ok := it.nclx()
	if !ok {
		return nclx{}, false
	}
	cfg, ok := it.prop("hvcC")
	if !ok {
		return nclx{}, false
	}
	rec, err := hevc.DecodeHEVCDecConfRec(cfg)
	if err != nil {
		return nclx{}, false
	}
	for _, arr := range rec.NaluArrays {
		for _, nal := range arr.Nalus {
			if annexb.NALUnitType(hwmediacodec.HEVC, nal) != annexb.HEVCNALSPS {
				continue
			}
			sps, err := hevcps.ParseSPS(nal)
			if err != nil {
				return nclx{}, false
			}
			return c, !(sps.VUIPresent && sps.VUI.VideoSignalTypePresent)
		}
	}
	return nclx{}, false
}

// nv12Image converts a decoded NV12 frame to RGB with the given matrix
// and range. Chroma is taken from the nearest sample.
func nv12Image(fr *hwmediacodec.Frame, c nclx) *image.RGBA {
	// BT.601, which is also what an unspecified matrix means for stills.
	kr, kb := 0.299, 0.114
	switch c.matrix {
	case 1:
		kr, kb = 0.2126, 0.0722
	case 9:
		kr, kb = 0.2627, 0.0593
	}
	kg := 1 - kr - kb
	yOff, yScale, cScale := 0.0, 1.0, 1.0
	if !c.fullRange {
		yOff, yScale, cScale = 16, 255.0/219, 255.0/224
	}
	const one = 1 << 16
	fix := func(v float64) int32 { return int32(v*one + 0.5) }
	yMul := fix(yScale)
	crR, cbB := fix(2*(1-kr)*cScale), fix(2*(1-kb)*cScale)
	cbG, crG := fix(2*kb*(1-kb)/kg*cScale), fix(2*kr*(1-kr)/kg*cScale)
	clamp := func(v int32) uint8 {
		v = (v + one/2) >> 16
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return uint8(v)
	}
	img := image.NewRGBA(image.Rect(0, 0, fr.Width, fr.Height))
	luma, chroma := fr.Planes[0], fr.Planes[1]
	for y := 0; y < fr.Height; y++ {
		lrow := luma[y*fr.Strides[0]:]
		crow := chroma[(y/2)*fr.Strides[1]:]
		out := img.Pix[y*img.Stride:]
		for x := 0; x < fr.Width; x++ {
			yy := (int32(lrow[x]) - int32(yOff)) * yMul
			cb, cr := int32(crow[x&^1])-128, int32(crow[x|1])-128
			out[4*x] = clamp(yy + crR*cr)
			out[4*x+1] = clamp(yy - cbG*cb - crG*cr)
			out[4*x+2] = clamp(yy + cbB*cb)
			out[4*x+3] = 255
		}
	}
	return img
}

// hasSequenceHeader reports whether an AV1 temporal unit contains a
// sequence header OBU (type 1).
func hasSequenceHeader(tu []byte) bool {
	for off := 0; off < len(tu); {
		h := tu[off]
		typ := (h >> 3) & 15
		hasExt, hasSize := h&4 != 0, h&2 != 0
		off++
		if hasExt {
			off++
		}
		if !hasSize {
			return typ == 1
		}
		size, n := 0, 0
		for n < 8 && off+n < len(tu) {
			b := tu[off+n]
			size |= int(b&0x7f) << (7 * n)
			n++
			if b&0x80 == 0 {
				break
			}
		}
		off += n
		if typ == 1 {
			return true
		}
		off += size
	}
	return false
}
