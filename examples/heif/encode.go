package heif

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"io"

	"github.com/Eyevinn/mp4ff/av1"
	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/bitstream/annexb"
)

// Options control Encode.
type Options struct {
	// Codec is HEVC (default, writes HEIC) or AV1 (writes AVIF, where a
	// hardware AV1 encoder exists; none does on Apple Silicon).
	Codec hwmediacodec.Codec
	// Quality in (0, 1]; 0 uses the encoder's default.
	Quality float64
	// TileSize splits pictures larger than it into a grid of square tiles,
	// the way phone cameras write their photos; 0 writes one picture.
	TileSize int
	// Rotation in degrees counter-clockwise (0, 90, 180, 270) is stored
	// as an irot property: the pixels stay as given, viewers rotate.
	Rotation int
	// Software allows the OS software encoder.
	Software bool
}

type codedItem struct {
	config []byte // hvcC or av1C box payload
	data   []byte // sample: length-prefixed NAL units or an AV1 temporal unit
	width  int
	height int
}

// Encode writes img as a HEIF file.
func Encode(w io.Writer, img image.Image, o Options) error {
	if o.Codec == 0 {
		o.Codec = hwmediacodec.HEVC
	}
	if o.Codec != hwmediacodec.HEVC && o.Codec != hwmediacodec.AV1 {
		return fmt.Errorf("heif: cannot write %s pictures", o.Codec)
	}
	if o.Rotation%90 != 0 {
		return fmt.Errorf("heif: rotation must be a multiple of 90, got %d", o.Rotation)
	}
	rgba := toRGBA(img)
	width, height := rgba.Rect.Dx(), rgba.Rect.Dy()
	if width == 0 || height == 0 {
		return errors.New("heif: empty image")
	}
	// The hardware encoders work on 4:2:0 pictures with even dimensions
	// (VideoToolbox silently rounds an odd request down), so odd pictures
	// are padded by one replicated row or column and a clean-aperture
	// (clap) property tells readers the real size.
	codedW, codedH := width+width%2, height+height%2

	// Tiles (one when the picture fits).
	tileW, tileH, cols, rows := codedW, codedH, 1, 1
	if o.TileSize > 0 && (codedW > o.TileSize || codedH > o.TileSize) {
		tileW, tileH = o.TileSize+o.TileSize%2, o.TileSize+o.TileSize%2
		cols, rows = (codedW+tileW-1)/tileW, (codedH+tileH-1)/tileH
	}
	var tiles []*image.RGBA
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			tiles = append(tiles, tile(rgba, c*tileW, r*tileH, tileW, tileH))
		}
	}
	coded, err := encodeTiles(tiles, o)
	if err != nil {
		return err
	}
	return writeFile(w, coded, cols, rows, width, height, codedW, codedH, o)
}

// toRGBA converts any image into a tightly packed RGBA image.
func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok && r.Rect.Min == (image.Point{}) && r.Stride == 4*r.Rect.Dx() {
		return r
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Rect, img, b.Min, draw.Src)
	return out
}

// tile cuts a w x h tile at (x0, y0), replicating the last row and column
// beyond the picture so that the padding does not cost bits.
func tile(src *image.RGBA, x0, y0, w, h int) *image.RGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	if x0 == 0 && y0 == 0 && w == sw && h == sh {
		return src
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := min(y0+y, sh-1)
		row := src.Pix[sy*src.Stride:]
		for x := 0; x < w; x++ {
			sx := min(x0+x, sw-1)
			copy(out.Pix[y*out.Stride+x*4:][:4], row[sx*4:][:4])
		}
	}
	return out
}

// encodeTiles encodes every tile as one keyframe with a single encoder.
func encodeTiles(tiles []*image.RGBA, o Options) ([]codedItem, error) {
	w, h := tiles[0].Rect.Dx(), tiles[0].Rect.Dy()
	opts := []hwmediacodec.EncoderOption{
		hwmediacodec.WithInputFormat(hwmediacodec.RGBA),
		hwmediacodec.WithKeyframeInterval(1),
		hwmediacodec.WithFrameRate(1),
	}
	if o.Quality > 0 {
		opts = append(opts, hwmediacodec.WithQuality(o.Quality))
	}
	if o.Software {
		opts = append(opts, hwmediacodec.WithSoftwareFallback())
	}
	ctx := context.Background()
	enc, err := hwmediacodec.NewEncoder(ctx, o.Codec, w, h, opts...)
	if err != nil {
		return nil, err
	}
	defer enc.Close()

	var packets []hwmediacodec.Packet
	drain := func() error {
		for {
			p, err := enc.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			packets = append(packets, p)
		}
	}
	for i, t := range tiles {
		f := &hwmediacodec.Frame{
			Width: w, Height: h, Format: hwmediacodec.RGBA,
			Planes: [][]byte{t.Pix}, Strides: []int{t.Stride},
			PTS: int64(i) * int64(hwmediacodec.DefaultTimeScale), ForceKeyframe: true,
		}
		if err := enc.Send(ctx, f); err != nil {
			return nil, err
		}
		if err := drain(); err != nil {
			return nil, err
		}
	}
	if err := enc.Flush(ctx); err != nil {
		return nil, err
	}
	if err := drain(); err != nil {
		return nil, err
	}
	if len(packets) != len(tiles) {
		return nil, fmt.Errorf("heif: encoder produced %d pictures for %d tiles", len(packets), len(tiles))
	}
	out := make([]codedItem, len(packets))
	for i, p := range packets {
		if !p.Keyframe {
			return nil, fmt.Errorf("heif: picture %d is not a keyframe", i)
		}
		var err error
		switch o.Codec {
		case hwmediacodec.HEVC:
			out[i], err = hevcItem(p.Data)
		case hwmediacodec.AV1:
			out[i], err = av1Item(p.Data)
		}
		if err != nil {
			return nil, err
		}
		out[i].width, out[i].height = w, h
	}
	return out, nil
}

// hevcItem splits an Annex-B access unit into the hvcC record (from the
// in-band VPS/SPS/PPS) and the length-prefixed slice data.
func hevcItem(au []byte) (codedItem, error) {
	var vps, sps, pps [][]byte
	var data []byte
	for _, nal := range annexb.Split(au) {
		t := annexb.NALUnitType(hwmediacodec.HEVC, nal)
		switch t {
		case annexb.HEVCNALVPS:
			vps = append(vps, nal)
		case annexb.HEVCNALSPS:
			sps = append(sps, nal)
		case annexb.HEVCNALPPS:
			pps = append(pps, nal)
		case annexb.HEVCNALAUD, annexb.HEVCNALFiller:
		default:
			n := len(nal)
			data = append(data, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
			data = append(data, nal...)
		}
	}
	if len(vps) == 0 || len(sps) == 0 || len(pps) == 0 {
		return codedItem{}, errors.New("heif: the encoded picture carries no VPS/SPS/PPS")
	}
	hvcC, err := mp4.CreateHvcC(vps, sps, pps, true, true, true, true)
	if err != nil {
		return codedItem{}, err
	}
	var buf bytes.Buffer
	if err := hvcC.DecConfRec.Encode(&buf); err != nil {
		return codedItem{}, err
	}
	return codedItem{config: buf.Bytes(), data: data}, nil
}

// av1Item builds the av1C record from the sequence header of a temporal
// unit; the unit itself is the item data.
func av1Item(tu []byte) (codedItem, error) {
	var seqHdr []byte
	for off := 0; off < len(tu); {
		h := tu[off]
		typ := (h >> 3) & 15
		hasExt, hasSize := h&4 != 0, h&2 != 0
		start := off
		off++
		if hasExt {
			off++
		}
		if !hasSize {
			return codedItem{}, errors.New("heif: AV1 OBU without a size field")
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
		if off+size > len(tu) {
			return codedItem{}, errors.New("heif: truncated AV1 OBU")
		}
		if typ == 1 {
			seqHdr = tu[start : off+size]
			sh, err := av1.ParseSequenceHeader(tu[off : off+size])
			if err != nil {
				return codedItem{}, fmt.Errorf("heif: AV1 sequence header: %w", err)
			}
			rec := av1.CodecConfRecFromSequenceHeader(sh, seqHdr)
			var buf bytes.Buffer
			if err := rec.Encode(&buf); err != nil {
				return codedItem{}, err
			}
			return codedItem{config: buf.Bytes(), data: tu}, nil
		}
		off += size
	}
	return codedItem{}, errors.New("heif: the encoded AV1 picture carries no sequence header")
}

// writeFile lays out ftyp, meta and mdat. Item IDs: tiles 1..n, the grid
// (when there is one) n+1.
func writeFile(w io.Writer, coded []codedItem, cols, rows, width, height, codedW, codedH int, o Options) error {
	n := len(coded)
	gridID := uint32(n + 1)
	primary := uint32(1)
	if n > 1 {
		primary = gridID
	}
	configType, brand, compatible := "hvcC", "heic", []string{"mif1", "heic"}
	if o.Codec == hwmediacodec.AV1 {
		configType, brand, compatible = "av1C", "avif", []string{"avif", "mif1", "miaf"}
	}

	// Properties: distinct configs, tile size, picture size, pixel info,
	// colour, rotation. ipma indexes are 1-based positions in ipco.
	var props [][]byte
	index := map[string]int{}
	add := func(key string, b []byte) int {
		if i, ok := index[key]; ok {
			return i
		}
		props = append(props, b)
		index[key] = len(props)
		return len(props)
	}
	ispe := func(w, h int) []byte {
		return payload(func(x *writer) { x.full("ispe", 0, 0, payload(func(y *writer) { y.u32(uint32(w)); y.u32(uint32(h)) })) })
	}
	pixi := add("pixi", payload(func(x *writer) { x.full("pixi", 0, 0, []byte{3, 8, 8, 8}) }))
	// nclx: BT.709 primaries, transfer and matrix, video range, which is
	// what VideoToolbox converts RGB input to and tags the stream with.
	colr := add("colr", payload(func(x *writer) {
		x.box("colr", payload(func(y *writer) { y.str("nclx"); y.u16(1); y.u16(1); y.u16(1); y.u8(0) }))
	}))
	irot := 0
	if o.Rotation%360 != 0 {
		irot = add("irot", payload(func(x *writer) { x.box("irot", []byte{byte((o.Rotation / 90) % 4)}) }))
	}
	// clap: the clean aperture is width x height, offset from the centre
	// of the coded picture so that it starts at the top-left corner.
	clap := 0
	if codedW != width || codedH != height {
		clap = add("clap", payload(func(x *writer) {
			x.box("clap", payload(func(y *writer) {
				y.u32(uint32(width))
				y.u32(1)
				y.u32(uint32(height))
				y.u32(1)
				y.u32(uint32(int32(width - codedW))) // horizOffN / 2
				y.u32(2)
				y.u32(uint32(int32(height - codedH)))
				y.u32(2)
			}))
		}))
	}
	type assoc struct {
		index     int
		essential bool
	}
	assocs := map[uint32][]assoc{}
	for i, c := range coded {
		id := uint32(i + 1)
		cfg := add(configType+string(c.config), payload(func(x *writer) { x.box(configType, c.config) }))
		size := add(fmt.Sprintf("ispe%dx%d", c.width, c.height), ispe(c.width, c.height))
		assocs[id] = []assoc{{cfg, true}, {size, false}, {pixi, false}, {colr, false}}
		if n == 1 {
			// Transformations apply in order: clap, then irot.
			if clap != 0 {
				assocs[id] = append(assocs[id], assoc{clap, true})
			}
			if irot != 0 {
				assocs[id] = append(assocs[id], assoc{irot, true})
			}
		}
	}
	var gridData []byte
	if n > 1 {
		gridData = payload(func(x *writer) {
			x.u8(0)
			x.u8(1) // 32-bit output size
			x.u8(byte(rows - 1))
			x.u8(byte(cols - 1))
			x.u32(uint32(codedW))
			x.u32(uint32(codedH))
		})
		size := add(fmt.Sprintf("ispe%dx%d", codedW, codedH), ispe(codedW, codedH))
		assocs[gridID] = []assoc{{size, false}, {pixi, false}, {colr, false}}
		if clap != 0 {
			assocs[gridID] = append(assocs[gridID], assoc{clap, true})
		}
		if irot != 0 {
			assocs[gridID] = append(assocs[gridID], assoc{irot, true})
		}
	}

	// Item data in mdat: tiles then the grid description.
	var mdat []byte
	var extents [][2]int // offset within mdat, length
	for _, c := range coded {
		extents = append(extents, [2]int{len(mdat), len(c.data)})
		mdat = append(mdat, c.data...)
	}
	if n > 1 {
		extents = append(extents, [2]int{len(mdat), len(gridData)})
		mdat = append(mdat, gridData...)
	}
	items := n
	if n > 1 {
		items = n + 1
	}

	buildMeta := func(mdatStart int) []byte {
		return payload(func(m *writer) {
			m.full("meta", 0, 0, payload(func(x *writer) {
				x.full("hdlr", 0, 0, payload(func(y *writer) {
					y.u32(0)
					y.str("pict")
					y.u32(0)
					y.u32(0)
					y.u32(0)
					y.u8(0)
				}))
				x.full("pitm", 0, 0, payload(func(y *writer) { y.u16(uint16(primary)) }))
				x.full("iinf", 0, 0, payload(func(y *writer) {
					y.u16(uint16(items))
					for id := 1; id <= items; id++ {
						typ, flags := "hvc1", uint32(0)
						if o.Codec == hwmediacodec.AV1 {
							typ = "av01"
						}
						if n > 1 && uint32(id) == gridID {
							typ = "grid"
						} else if n > 1 {
							flags = 1 // tiles are hidden
						}
						y.full("infe", 2, flags, payload(func(z *writer) { z.u16(uint16(id)); z.u16(0); z.str(typ); z.u8(0) }))
					}
				}))
				if n > 1 {
					x.full("iref", 0, 0, payload(func(y *writer) {
						y.box("dimg", payload(func(z *writer) {
							z.u16(uint16(gridID))
							z.u16(uint16(n))
							for id := 1; id <= n; id++ {
								z.u16(uint16(id))
							}
						}))
					}))
				}
				x.box("iprp", payload(func(y *writer) {
					y.box("ipco", payload(func(z *writer) {
						for _, p := range props {
							z.raw(p)
						}
					}))
					y.full("ipma", 0, 0, payload(func(z *writer) {
						z.u32(uint32(items))
						for id := 1; id <= items; id++ {
							a := assocs[uint32(id)]
							z.u16(uint16(id))
							z.u8(uint8(len(a)))
							for _, e := range a {
								v := uint8(e.index)
								if e.essential {
									v |= 0x80
								}
								z.u8(v)
							}
						}
					}))
				}))
				x.full("iloc", 0, 0, payload(func(y *writer) {
					y.u8(4<<4 | 4) // offset and length sizes
					y.u8(0)        // base offset size, reserved
					y.u16(uint16(items))
					for id := 1; id <= items; id++ {
						e := extents[id-1]
						y.u16(uint16(id))
						y.u16(0) // data reference index
						y.u16(1) // extent count
						y.u32(uint32(mdatStart + e[0]))
						y.u32(uint32(e[1]))
					}
				}))
			}))
		})
	}

	ftyp := payload(func(x *writer) {
		x.box("ftyp", payload(func(y *writer) {
			y.str(brand)
			y.u32(0)
			for _, c := range compatible {
				y.str(c)
			}
		}))
	})
	// The meta size does not depend on the offsets it stores, so build it
	// once to learn where mdat's payload starts, then for real.
	metaSize := len(buildMeta(0))
	meta := buildMeta(len(ftyp) + metaSize + 8)
	var out writer
	out.raw(ftyp)
	out.raw(meta)
	out.box("mdat", mdat)
	_, err := w.Write(out.buf)
	return err
}
