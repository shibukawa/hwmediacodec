package heif

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"

	"github.com/Eyevinn/mp4ff/av1"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/shibukawa/hwmediacodec"
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

func init() {
	// The major brand follows the box size and "ftyp". Files whose major
	// brand is the generic mif1/msf1 are reported as "heif".
	for _, b := range []string{"heic", "heix", "hevc", "hevx", "heim", "heis", "hevm", "hevs"} {
		image.RegisterFormat("heic", "????ftyp"+b, Decode, DecodeConfig)
	}
	for _, b := range []string{"avif", "avis"} {
		image.RegisterFormat("avif", "????ftyp"+b, Decode, DecodeConfig)
	}
	for _, b := range []string{"mif1", "msf1"} {
		image.RegisterFormat("heif", "????ftyp"+b, Decode, DecodeConfig)
	}
}

// Decode reads a HEIC or AVIF file from r and returns its primary image
// as an *image.RGBA. It is DecodeBytes without options, in the form the
// image package registers.
func Decode(r io.Reader) (image.Image, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	img, _, err := DecodeBytes(data)
	if err != nil {
		return nil, err
	}
	return img, nil
}

// DecodeConfig returns the colour model and the dimensions of the image
// Decode would return (after cropping and rotation) without decoding it,
// so it needs no hardware codec.
func DecodeConfig(r io.Reader) (image.Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return image.Config{}, err
	}
	info, err := DecodeInfo(data)
	if err != nil {
		return image.Config{}, err
	}
	return image.Config{ColorModel: color.RGBAModel, Width: info.Width, Height: info.Height}, nil
}

// DecodeInfo describes the primary image of a HEIC or AVIF file held in
// data from the file structure alone: nothing is decoded.
func DecodeInfo(data []byte) (*Info, error) {
	f, err := parse(data)
	if err != nil {
		return nil, err
	}
	prim, ok := f.items[f.primary]
	if !ok {
		return nil, fmt.Errorf("heif: primary item %d not found", f.primary)
	}
	info := &Info{Tiles: 1}
	coded := prim
	var w, h int
	switch prim.typ {
	case "hvc1", "hev1", "av01":
		if w, h, ok = prim.ispe(); !ok {
			return nil, fmt.Errorf("heif: item %d has no ispe property", prim.id)
		}
	case "grid":
		g, err := f.grid(prim)
		if err != nil {
			return nil, err
		}
		w, h, info.Tiles, coded = g.outW, g.outH, len(g.tiles), g.tiles[0]
	default:
		return nil, fmt.Errorf("heif: primary item is a %q, which this decoder does not handle", prim.typ)
	}
	switch coded.typ {
	case "hvc1", "hev1":
		info.Codec = hwmediacodec.HEVC
	case "av01":
		info.Codec = hwmediacodec.AV1
	default:
		return nil, fmt.Errorf("heif: grid tiles are %q items, which this decoder does not handle", coded.typ)
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
	info.Width, info.Height = w, h
	return info, nil
}

// DecodeBytes decodes the primary image of a HEIC or AVIF file held in
// data. Grid images are decoded tile by tile and stitched; clap, irot and
// imir are applied. The result is opaque RGBA. The options are passed to
// the decoder (hwmediacodec.WithSoftwareFallback, for example).
func DecodeBytes(data []byte, opts ...hwmediacodec.DecoderOption) (*image.RGBA, *Info, error) {
	f, err := parse(data)
	if err != nil {
		return nil, nil, err
	}
	prim, ok := f.items[f.primary]
	if !ok {
		return nil, nil, fmt.Errorf("heif: primary item %d not found", f.primary)
	}
	info := &Info{Tiles: 1}
	var img *image.RGBA
	switch prim.typ {
	case "hvc1", "hev1", "av01":
		img, info.Codec, err = f.decodeCoded([]*item{prim}, nil, opts)
	case "grid":
		img, info.Codec, info.Tiles, err = f.decodeGrid(prim, opts)
	default:
		return nil, nil, fmt.Errorf("heif: primary item is a %q, which this decoder does not handle", prim.typ)
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
		return nil, errors.New("heif: no ftyp box")
	}
	meta, ok := find(top, "meta")
	if !ok {
		return nil, errors.New("heif: no meta box")
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
			return nil, fmt.Errorf("heif: handler is %q, not a picture file", p[4:8])
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
		return nil, errors.New("heif: no items")
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
			return fmt.Errorf("heif: infe version %d is not supported", ev)
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
		return errors.New("heif: iprp without ipco")
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
					return fmt.Errorf("heif: item %d refers to property %d of %d", id, index, len(props))
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
		return nil, fmt.Errorf("heif: item %d uses construction method %d", it.id, it.method)
	}
	var out []byte
	for _, e := range it.extents {
		off, n := e[0], e[1]
		if off > uint64(len(src)) || n > uint64(len(src))-off {
			return nil, fmt.Errorf("heif: item %d extent [%d, %d) is outside the data", it.id, off, off+n)
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
			return nil, 0, fmt.Errorf("heif: item %d has no hvcC", it.id)
		}
		rec, err := hevc.DecodeHEVCDecConfRec(cfg)
		if err != nil {
			return nil, 0, fmt.Errorf("heif: item %d hvcC: %w", it.id, err)
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
				return nil, 0, fmt.Errorf("heif: item %d: NAL unit length %d out of range", it.id, size)
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
				return nil, 0, fmt.Errorf("heif: item %d has no av1C", it.id)
			}
			rec, err := av1.DecodeAV1CodecConfRec(cfg)
			if err != nil {
				return nil, 0, fmt.Errorf("heif: item %d av1C: %w", it.id, err)
			}
			data = append(append([]byte(nil), rec.ConfigOBUs...), data...)
		}
		return data, hwmediacodec.AV1, nil
	}
	return nil, 0, fmt.Errorf("heif: item %d is a %q, not a coded picture", it.id, it.typ)
}

// decodeCoded decodes items of one codec in order and hands each frame to
// place (or returns the single frame when place is nil).
func (f *file) decodeCoded(items []*item, place func(i int, img *image.RGBA) error, opts []hwmediacodec.DecoderOption) (*image.RGBA, hwmediacodec.Codec, error) {
	if len(items) == 0 {
		return nil, 0, errors.New("heif: nothing to decode")
	}
	first, codec, err := f.packet(items[0])
	if err != nil {
		return nil, 0, err
	}
	ctx := context.Background()
	// Decode order: the pictures are all keyframes, and this way each one
	// comes out as soon as it is decoded instead of waiting in the
	// reorder buffer.
	all := append([]hwmediacodec.DecoderOption{hwmediacodec.WithOutputFormat(hwmediacodec.RGBA), hwmediacodec.WithDecodeOrder()}, opts...)
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
			img := frameImage(fr)
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
				return nil, 0, errors.New("heif: tiles use different codecs")
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
				return nil, 0, fmt.Errorf("heif: decode item %d: %w", it.id, err)
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
		return nil, 0, fmt.Errorf("heif: decoded %d pictures for %d items", got, len(items))
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
		return nil, fmt.Errorf("heif: grid version %d", v)
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
		return nil, fmt.Errorf("heif: grid of %dx%d references %d tiles", g.cols, g.rows, len(grid.dimg))
	}
	g.tiles = make([]*item, len(grid.dimg))
	for i, id := range grid.dimg {
		t, ok := f.items[id]
		if !ok {
			return nil, fmt.Errorf("heif: grid tile %d missing", id)
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
				return fmt.Errorf("heif: %dx%d tiles of %dx%d do not cover %dx%d", cols, rows, tileW, tileH, outW, outH)
			}
			canvas = image.NewRGBA(image.Rect(0, 0, tileW*cols, tileH*rows))
		}
		if img.Rect.Dx() != tileW || img.Rect.Dy() != tileH {
			return fmt.Errorf("heif: tile %d is %dx%d, others are %dx%d", i, img.Rect.Dx(), img.Rect.Dy(), tileW, tileH)
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
		return image.Rectangle{}, fmt.Errorf("heif: clap: %w", r.err)
	}
	if v[1] == 0 || v[3] == 0 || v[5] == 0 || v[7] == 0 {
		return image.Rectangle{}, errors.New("heif: clap with a zero denominator")
	}
	pw, ph := int64(width), int64(height)
	// Integer arithmetic on doubled coordinates: left = (pw - w)/2 + off.
	w, h := v[0]/v[1], v[2]/v[3]
	left := ((pw-w)*v[5] + 2*v[4]) / (2 * v[5]) // (pw-w)/2 + horizOffN/horizOffD
	top := ((ph-h)*v[7] + 2*v[6]) / (2 * v[7])
	if w <= 0 || h <= 0 || left < 0 || top < 0 || left+w > pw || top+h > ph {
		return image.Rectangle{}, fmt.Errorf("heif: clap %dx%d at (%d, %d) does not fit %dx%d", w, h, left, top, pw, ph)
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
