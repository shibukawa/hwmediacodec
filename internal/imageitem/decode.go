package imageitem

import (
	"context"
	"encoding/binary"
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

// Limits bound what a file can make the decoder do. A HEIF file declares
// the size of its pictures and of a grid's canvas itself, and iloc extents
// may alias, so a few hundred bytes can describe a 16 GiB canvas stitched
// from 65,536 decodes of the same tile. Decode refuses such a file with an
// error wrapping ErrTooLarge before it allocates the canvas or opens a
// decoder; DecodeInfo and DecodeConfig keep reporting the declared size,
// as image/png's DecodeConfig does. The defaults are those of libavif and
// libheif. A zero field means the default.
type Limits struct {
	// MaxPixels bounds the picture a single coded item declares (ispe)
	// and the canvas of a grid (its output size and each tile).
	MaxPixels int
	// MaxTiles bounds the number of tiles a grid is stitched from, which
	// is the number of pictures decoded.
	MaxTiles int
	// MaxInputBytes bounds the file size. A reader that cannot seek is
	// read into memory up to this size and refused beyond it.
	MaxInputBytes int64
}

// DefaultLimits are the values a zero Limits field stands for.
var DefaultLimits = Limits{MaxPixels: 16384 * 16384, MaxTiles: 4096, MaxInputBytes: 256 << 20}

// ErrTooLarge is wrapped by the error Decode returns for a file that
// exceeds its Limits; test for it with errors.Is.
var ErrTooLarge = errors.New("image exceeds the decode limits")

// withDefaults fills the zero fields.
func (l Limits) withDefaults() Limits {
	if l.MaxPixels <= 0 {
		l.MaxPixels = DefaultLimits.MaxPixels
	}
	if l.MaxTiles <= 0 {
		l.MaxTiles = DefaultLimits.MaxTiles
	}
	if l.MaxInputBytes <= 0 {
		l.MaxInputBytes = DefaultLimits.MaxInputBytes
	}
	return l
}

// checkPixels is the MaxPixels test for a what of w x h.
func (l Limits) checkPixels(what string, w, h int) error {
	if w < 0 || h < 0 || uint64(w)*uint64(h) > uint64(l.MaxPixels) {
		return fmt.Errorf("%w: %s is %dx%d, the limit is %d pixels", ErrTooLarge, what, w, h, l.MaxPixels)
	}
	return nil
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

// source is where the item data lives: the whole file in memory, or a
// seekable reader from which the extents are read when they are needed,
// so that a file on disk costs only its headers until a picture is
// decoded.
type source interface {
	// slice returns the n bytes at off, which the caller has checked
	// against size; the in-memory source returns a view, the reader
	// allocates.
	slice(off, n uint64) ([]byte, error)
	size() uint64
}

type bytesSource []byte

func (b bytesSource) slice(off, n uint64) ([]byte, error) { return b[off : off+n], nil }
func (b bytesSource) size() uint64                        { return uint64(len(b)) }

// seekSource reads from base in r, which is where r stood when decoding
// began, as an io.Reader would have.
type seekSource struct {
	r    io.ReadSeeker
	base int64
	n    uint64
}

func (s *seekSource) size() uint64 { return s.n }

func (s *seekSource) slice(off, n uint64) ([]byte, error) {
	if _, err := s.r.Seek(s.base+int64(off), io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(s.r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// readSource makes a source of r. A reader that can seek is used in
// place: its size comes from the end, and nothing is read until a box or
// an extent is wanted. Anything else is read into memory up to the limit
// and refused beyond it, so that a stream cannot make the decoder hold an
// unbounded buffer. os.Stdin is an *os.File whose Seek fails on a pipe,
// which falls back to reading as well.
func readSource(r io.Reader, l Limits) (source, error) {
	if rs, ok := r.(io.ReadSeeker); ok {
		if cur, err := rs.Seek(0, io.SeekCurrent); err == nil {
			end, err := rs.Seek(0, io.SeekEnd)
			if err == nil && end >= cur {
				if end-cur > l.MaxInputBytes {
					return nil, fmt.Errorf("%w: the file is %d bytes, the limit is %d", ErrTooLarge, end-cur, l.MaxInputBytes)
				}
				return &seekSource{r: rs, base: cur, n: uint64(end - cur)}, nil
			}
			if _, err := rs.Seek(cur, io.SeekStart); err != nil {
				return nil, err
			}
		}
	}
	data, err := io.ReadAll(io.LimitReader(r, l.MaxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > l.MaxInputBytes {
		return nil, fmt.Errorf("%w: the input exceeds %d bytes", ErrTooLarge, l.MaxInputBytes)
	}
	return bytesSource(data), nil
}

type file struct {
	src     source
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
	registered = map[hwmediacodec.Codec]func() Limits{}
	genericSet bool
)

// Register is called by the public packages from init: it notes that
// codec c is linked into the program, with the function that reads the
// package's current limits, and registers the generic brands (mif1,
// msf1), which do not say how the pictures are coded, with the image
// package once. Files with these brands are decoded when the package for
// their codec is linked, under that package's limits.
func Register(c hwmediacodec.Codec, limits func() Limits) {
	registerMu.Lock()
	defer registerMu.Unlock()
	registered[c] = limits
	if genericSet {
		return
	}
	genericSet = true
	for _, b := range []string{"mif1", "msf1"} {
		image.RegisterFormat("heif", "????ftyp"+b, decodeGeneric, decodeConfigGeneric)
	}
}

// linked returns the limits of the package for codec c, or the error
// telling which package to import when none is linked.
func linked(c hwmediacodec.Codec) (Limits, error) {
	registerMu.Lock()
	defer registerMu.Unlock()
	if limits, ok := registered[c]; ok {
		return limits().withDefaults(), nil
	}
	pkg := "image/heif"
	if c == hwmediacodec.AV1 {
		pkg = "image/avif"
	}
	return Limits{}, fmt.Errorf("the file is %s; import github.com/shibukawa/hwmediacodec/%s to decode it", name(c), pkg)
}

// genericLimits are the limits for reading a file whose codec is not yet
// known: the most permissive input size among the linked packages, which
// each package's own limits replace once the codec is known.
func genericLimits() Limits {
	registerMu.Lock()
	defer registerMu.Unlock()
	l := DefaultLimits
	for _, limits := range registered {
		if n := limits().withDefaults().MaxInputBytes; n > l.MaxInputBytes {
			l.MaxInputBytes = n
		}
	}
	return l
}

func decodeGeneric(r io.Reader) (image.Image, error) {
	img, _, err := Decode(r, 0, genericLimits())
	if err != nil {
		return nil, fmt.Errorf("heif: %w", err)
	}
	return img, nil
}

func decodeConfigGeneric(r io.Reader) (image.Config, error) {
	cfg, err := DecodeConfig(r, 0, genericLimits())
	if err != nil {
		return cfg, fmt.Errorf("heif: %w", err)
	}
	return cfg, nil
}

// Decode reads a file from r and returns its primary image; see
// DecodeBytes for only, l and opts. When r can seek (an *os.File, a
// bytes.Reader) only the headers and the items of the primary image are
// read; otherwise r is read into memory up to l.MaxInputBytes.
func Decode(r io.Reader, only hwmediacodec.Codec, l Limits, opts ...hwmediacodec.DecoderOption) (*image.RGBA, *Info, error) {
	l = l.withDefaults()
	src, err := readSource(r, l)
	if err != nil {
		return nil, nil, err
	}
	return decode(src, only, l, opts)
}

// DecodeConfig returns the colour model and the dimensions of the image
// Decode would return, without decoding it. When r can seek, only the
// headers are read.
func DecodeConfig(r io.Reader, only hwmediacodec.Codec, l Limits) (image.Config, error) {
	src, err := readSource(r, l.withDefaults())
	if err != nil {
		return image.Config{}, err
	}
	f, err := parse(src)
	if err != nil {
		return image.Config{}, err
	}
	d, err := f.describe(only)
	if err != nil {
		return image.Config{}, err
	}
	return image.Config{ColorModel: color.RGBAModel, Width: d.info.Width, Height: d.info.Height}, nil
}

// DecodeInfo describes the primary image of a file held in data from the
// file structure alone: nothing is decoded and no limit applies, so the
// size of a file Decode refuses is still reported. A non-zero only
// rejects files coded with another codec.
func DecodeInfo(data []byte, only hwmediacodec.Codec) (*Info, error) {
	f, err := parse(bytesSource(data))
	if err != nil {
		return nil, err
	}
	d, err := f.describe(only)
	if err != nil {
		return nil, err
	}
	return d.info, nil
}

// DecodeBytes decodes the primary image of a file held in data. Grid
// images are decoded tile by tile and stitched; clap, irot and imir are
// applied. The result is opaque RGBA. A non-zero only rejects files coded
// with another codec before a decoder is opened, and so does a file
// beyond l (see Limits). The options are passed to the decoder.
func DecodeBytes(data []byte, only hwmediacodec.Codec, l Limits, opts ...hwmediacodec.DecoderOption) (*image.RGBA, *Info, error) {
	l = l.withDefaults()
	if int64(len(data)) > l.MaxInputBytes {
		return nil, nil, fmt.Errorf("%w: the file is %d bytes, the limit is %d", ErrTooLarge, len(data), l.MaxInputBytes)
	}
	return decode(bytesSource(data), only, l, opts)
}

// decode parses, checks the codec and the limits, then decodes.
func decode(src source, only hwmediacodec.Codec, l Limits, opts []hwmediacodec.DecoderOption) (*image.RGBA, *Info, error) {
	f, err := parse(src)
	if err != nil {
		return nil, nil, err
	}
	d, err := f.describe(only)
	if err != nil {
		return nil, nil, err
	}
	// The generic brands reach here with only == 0 and the limits of
	// whichever package is linked; a package's own call always finds
	// itself registered.
	if only == 0 {
		if l, err = linked(d.info.Codec); err != nil {
			return nil, nil, err
		}
	}
	if err := d.checkLimits(l); err != nil {
		return nil, nil, err
	}
	info := &Info{Codec: d.info.Codec, Tiles: d.info.Tiles}
	var img *image.RGBA
	if d.grid != nil {
		img, err = f.decodeGrid(d.grid, opts)
	} else {
		img, _, err = f.decodeCoded([]*item{d.prim}, nil, opts)
	}
	if err != nil {
		return nil, nil, err
	}
	// Transformative properties apply in their ipma order.
	for _, p := range d.prim.props {
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

// description is what the file structure says about the primary image:
// the Info DecodeInfo reports, the primary item and, for a grid, its
// layout.
type description struct {
	info *Info
	prim *item
	grid *gridLayout // nil for a single coded picture
}

// describe reads the primary image's structure. A non-zero only rejects
// another codec.
func (f *file) describe(only hwmediacodec.Codec) (*description, error) {
	prim, ok := f.items[f.primary]
	if !ok {
		return nil, fmt.Errorf("primary item %d not found", f.primary)
	}
	d := &description{info: &Info{Tiles: 1}, prim: prim}
	info := d.info
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
		d.grid = g
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
	return d, nil
}

// checkLimits refuses a primary image beyond l before anything is
// allocated or decoded. The sizes come from the declared properties: a
// grid's canvas and tile count from its descriptor, each coded picture
// from its ispe, which the specification requires every coded item to
// carry.
func (d *description) checkLimits(l Limits) error {
	coded := []*item{d.prim}
	if g := d.grid; g != nil {
		if len(g.tiles) > l.MaxTiles {
			return fmt.Errorf("%w: the grid has %d tiles, the limit is %d", ErrTooLarge, len(g.tiles), l.MaxTiles)
		}
		if err := l.checkPixels("the grid", g.outW, g.outH); err != nil {
			return err
		}
		coded = g.tiles
	}
	for _, it := range coded {
		w, h, ok := it.ispe()
		if !ok {
			return fmt.Errorf("item %d has no ispe property", it.id)
		}
		if err := l.checkPixels(fmt.Sprintf("item %d", it.id), w, h); err != nil {
			return err
		}
	}
	return nil
}

// topLevel walks the top-level boxes of src and returns the ftyp and meta
// payloads. Nothing else is read: mdat, which holds the pictures and is
// most of the file, is skipped by its size, so a seekable source costs
// only the headers until an item is wanted.
func topLevel(src source) (ftyp, meta box, err error) {
	var haveFtyp, haveMeta bool
	size := src.size()
	for off := uint64(0); off < size; {
		if size-off < 8 {
			return box{}, box{}, errors.New("truncated box header")
		}
		hdr, err := src.slice(off, 8)
		if err != nil {
			return box{}, box{}, err
		}
		size64 := uint64(binary.BigEndian.Uint32(hdr))
		typ := string(hdr[4:8])
		hdrLen := uint64(8)
		switch size64 {
		case 0:
			size64 = size - off
		case 1:
			if size-off < 16 {
				return box{}, box{}, errors.New("truncated large box header")
			}
			large, err := src.slice(off+8, 8)
			if err != nil {
				return box{}, box{}, err
			}
			size64 = binary.BigEndian.Uint64(large)
			hdrLen = 16
		}
		if size64 < hdrLen || size64 > size-off {
			return box{}, box{}, fmt.Errorf("box %q has size %d beyond the data", typ, size64)
		}
		want := (typ == "ftyp" && !haveFtyp) || (typ == "meta" && !haveMeta)
		if want {
			data, err := src.slice(off+hdrLen, size64-hdrLen)
			if err != nil {
				return box{}, box{}, err
			}
			b := box{typ: typ, data: data, start: int(off + hdrLen)}
			if typ == "ftyp" {
				ftyp, haveFtyp = b, true
			} else {
				meta, haveMeta = b, true
			}
		}
		off += size64
	}
	if !haveFtyp || len(ftyp.data) < 8 {
		return box{}, box{}, errors.New("no ftyp box")
	}
	if !haveMeta {
		return box{}, box{}, errors.New("no meta box")
	}
	return ftyp, meta, nil
}

// parse reads the meta box into items and properties.
func parse(src source) (*file, error) {
	_, meta, err := topLevel(src)
	if err != nil {
		return nil, err
	}
	_, _, metaPayload, err := fullBox(meta)
	if err != nil {
		return nil, err
	}
	children, err := parseBoxes(metaPayload, meta.start+4)
	if err != nil {
		return nil, err
	}
	f := &file{src: src, items: map[uint32]*item{}}
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
		if r.err != nil {
			return nil, r.err
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

// maxExtents bounds the iloc extents of one file: a grid of the most tiles
// the encoder writes (65,534) with one extent each, plus metadata items,
// fits with room to spare.
const maxExtents = 1 << 17

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
	extents := 0
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
		// A length_size of 0 means the extent runs to the end of the
		// data, which only makes sense for a single extent. With
		// offset_size 0 as well an extent costs no bytes in the box, so
		// 65,535 of them per item would otherwise be parsed for free:
		// requiring a length field for multiple extents makes every
		// extent cost at least one byte of the file, and the total cap
		// bounds the memory the extent table can take from a large file.
		if lengthSize == 0 && n > 1 {
			return fmt.Errorf("item %d has %d extents without a length field", id, n)
		}
		if extents += n; extents > maxExtents {
			return fmt.Errorf("the iloc box has more than %d extents", maxExtents)
		}
		for k := 0; k < n && r.err == nil; k++ {
			if indexSize > 0 {
				r.uint(indexSize)
			}
			off := r.uint(offsetSize)
			length := r.uint(lengthSize)
			if r.err != nil {
				return r.err
			}
			if off > ^uint64(0)-base {
				return fmt.Errorf("item %d extent offset overflows", id)
			}
			if length != 0 {
				it.extents = append(it.extents, [2]uint64{base + off, length})
			}
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
		n := int(r.u16()) // reference_count is 16 bits in both versions
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
	src := f.src
	if it.method == 1 {
		src = bytesSource(f.idat)
	} else if it.method != 0 {
		return nil, fmt.Errorf("item %d uses construction method %d", it.id, it.method)
	}
	size := src.size()
	var total uint64
	for _, e := range it.extents {
		off, n := e[0], e[1]
		if off > size || n > size-off {
			return nil, fmt.Errorf("item %d extent [%d, %d) is outside the data", it.id, off, off+n)
		}
		if len(it.extents) == 1 {
			return src.slice(off, n)
		}
		// Extents may overlap, so their sum is not bounded by the input
		// length. Bound it before allocating to avoid amplification by a
		// small malicious iloc table that repeats the same large range.
		if n > size-total {
			return nil, fmt.Errorf("item %d extents exceed the data size", it.id)
		}
		total += n
	}
	out := make([]byte, 0, int(total))
	for _, e := range it.extents {
		b, err := src.slice(e[0], e[1])
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
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
				out = annexb.AppendUnit(out, nal)
			}
		}
		if out, err = annexb.FromLengthPrefixed(out, data, int(rec.LengthSizeMinusOne)+1); err != nil {
			return nil, 0, fmt.Errorf("item %d: %w", it.id, err)
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

// itemReader hands the coded items to the decoder as packets, one
// keyframe each.
type itemReader struct {
	f     *file
	items []*item
	codec hwmediacodec.Codec
	first []byte // the first item's packet, built by load to learn the codec
	next  int
}

func (r *itemReader) load() error {
	var err error
	r.first, r.codec, err = r.f.packet(r.items[0])
	return err
}

func (r *itemReader) Codec() hwmediacodec.Codec { return r.codec }
func (r *itemReader) TimeScale() int32          { return 1 }

func (r *itemReader) ReadPacket() (hwmediacodec.Packet, error) {
	if r.next >= len(r.items) {
		return hwmediacodec.Packet{}, io.EOF
	}
	data := r.first
	if r.next > 0 {
		var c hwmediacodec.Codec
		var err error
		if data, c, err = r.f.packet(r.items[r.next]); err != nil {
			return hwmediacodec.Packet{}, err
		} else if c != r.codec {
			return hwmediacodec.Packet{}, errors.New("tiles use different codecs")
		}
	}
	r.next++
	return hwmediacodec.Packet{Data: data, PTS: int64(r.next - 1), Keyframe: true}, nil
}

// decodeCoded decodes items of one codec in order and hands each frame to
// place (or returns the single frame when place is nil).
func (f *file) decodeCoded(items []*item, place func(i int, img *image.RGBA) error, opts []hwmediacodec.DecoderOption) (*image.RGBA, hwmediacodec.Codec, error) {
	if len(items) == 0 {
		return nil, 0, errors.New("nothing to decode")
	}
	src := &itemReader{f: f, items: items}
	if err := src.load(); err != nil {
		return nil, 0, err
	}
	codec := src.codec
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
	all := append([]hwmediacodec.DecoderOption{
		hwmediacodec.WithOutputFormat(format), hwmediacodec.WithDecodeOrder(), hwmediacodec.WithTimeScale(src.TimeScale()),
	}, opts...)
	dec, err := hwmediacodec.NewDecoder(ctx, codec, all...)
	if err != nil {
		return nil, 0, err
	}
	defer dec.Close()

	var single *image.RGBA
	got := 0
	frames := hwmediacodec.NewDecodeReader(dec, src)
	for {
		fr, err := frames.ReadFrame(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("decode picture %d of %d: %w", got+1, len(items), err)
		}
		var img *image.RGBA
		if convert {
			img = nv12Image(fr, colours)
		} else {
			img, err = fr.RGBAImage()
		}
		fr.Release()
		if err != nil {
			return nil, 0, err
		}
		if place == nil {
			single = img
		} else if err := place(got, img); err != nil {
			return nil, 0, err
		}
		got++
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
	// Only unrepresentable sizes are rejected here, so that DecodeInfo
	// still describes a file Decode will refuse under its Limits.
	if g.outW <= 0 || g.outH <= 0 || uint64(g.outW) > uint64(^uint(0)>>1)/4/uint64(g.outH) {
		return nil, errors.New("grid dimensions are invalid or too large")
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
// stitched and cropped to the grid's output size. The caller has checked
// the layout against its Limits.
func (f *file) decodeGrid(g *gridLayout, opts []hwmediacodec.DecoderOption) (*image.RGBA, error) {
	rows, cols, outW, outH, tiles := g.rows, g.cols, g.outW, g.outH, g.tiles
	var canvas *image.RGBA
	var tileW, tileH int
	place := func(i int, img *image.RGBA) error {
		if i >= len(tiles) {
			return errors.New("decoder returned more pictures than grid tiles")
		}
		if canvas == nil {
			tileW, tileH = img.Rect.Dx(), img.Rect.Dy()
			if tileW <= 0 || tileH <= 0 || tileW < (outW+cols-1)/cols || tileH < (outH+rows-1)/rows {
				return fmt.Errorf("%dx%d tiles of %dx%d do not cover %dx%d", cols, rows, tileW, tileH, outW, outH)
			}
			canvas = image.NewRGBA(image.Rect(0, 0, outW, outH))
		}
		if img.Rect.Dx() != tileW || img.Rect.Dy() != tileH {
			return fmt.Errorf("tile %d is %dx%d, others are %dx%d", i, img.Rect.Dx(), img.Rect.Dy(), tileW, tileH)
		}
		x0, y0 := (i%cols)*tileW, (i/cols)*tileH
		copyW, copyH := min(tileW, outW-x0), min(tileH, outH-y0)
		for y := 0; y < copyH && copyW > 0; y++ {
			copy(canvas.Pix[(y0+y)*canvas.Stride+x0*4:][:copyW*4], img.Pix[y*img.Stride:y*img.Stride+copyW*4])
		}
		return nil
	}
	if _, _, err := f.decodeCoded(tiles, place, opts); err != nil {
		return nil, err
	}
	return canvas, nil
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
			if off == len(tu) {
				return false
			}
			off++
		}
		if !hasSize {
			return typ == 1
		}
		size, n := uint64(0), 0
		terminated := false
		for n < 8 && off+n < len(tu) {
			b := tu[off+n]
			size |= uint64(b&0x7f) << (7 * n)
			n++
			if b&0x80 == 0 {
				terminated = true
				break
			}
		}
		off += n
		if !terminated || size > uint64(len(tu)-off) {
			return false
		}
		if typ == 1 {
			return true
		}
		off += int(size)
	}
	return false
}
