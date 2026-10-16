package imageitem

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
)

// hdlr is a pict handler box payload.
func hdlr(y *writer) {
	y.u32(0)
	y.str("pict")
	y.u32(0)
	y.u32(0)
	y.u32(0)
	y.u8(0)
}

// ispeBox is an ispe property for a w x h picture.
func ispeBox(w, h int) []byte {
	return payload(func(x *writer) { x.full("ispe", 0, 0, payload(func(y *writer) { y.u32(uint32(w)); y.u32(uint32(h)) })) })
}

// gridAlias is a HEIC whose grid of cols x rows cells all reference the
// one coded tile (item 1, declared tileW x tileH, with the bytes of tile
// as its data), the way a malicious file gets a huge canvas out of a few
// hundred bytes. The grid (item 2) is the primary item; its output size
// is the tiles laid edge to edge.
func gridAlias(tile []byte, tileW, tileH, cols, rows int) []byte {
	n := cols * rows
	gridData := payload(func(x *writer) {
		x.u8(0)
		x.u8(1) // 32-bit output size
		x.u8(byte(rows - 1))
		x.u8(byte(cols - 1))
		x.u32(uint32(cols * tileW))
		x.u32(uint32(rows * tileH))
	})
	buildMeta := func(mdatStart int) []byte {
		return payload(func(m *writer) {
			m.full("meta", 0, 0, payload(func(x *writer) {
				x.full("hdlr", 0, 0, payload(hdlr))
				x.full("pitm", 0, 0, payload(func(y *writer) { y.u16(2) }))
				x.full("iinf", 0, 0, payload(func(y *writer) {
					y.u16(2)
					y.full("infe", 2, 1, payload(func(z *writer) { z.u16(1); z.u16(0); z.str("hvc1"); z.u8(0) }))
					y.full("infe", 2, 0, payload(func(z *writer) { z.u16(2); z.u16(0); z.str("grid"); z.u8(0) }))
				}))
				x.full("iref", 0, 0, payload(func(y *writer) {
					y.box("dimg", payload(func(z *writer) {
						z.u16(2)
						z.u16(uint16(n))
						for i := 0; i < n; i++ {
							z.u16(1)
						}
					}))
				}))
				x.box("iprp", payload(func(y *writer) {
					y.box("ipco", payload(func(z *writer) {
						z.raw(ispeBox(tileW, tileH))
						z.raw(ispeBox(cols*tileW, rows*tileH))
					}))
					y.full("ipma", 0, 0, payload(func(z *writer) {
						z.u32(2)
						z.u16(1)
						z.u8(1)
						z.u8(1)
						z.u16(2)
						z.u8(1)
						z.u8(2)
					}))
				}))
				x.full("iloc", 0, 0, payload(func(y *writer) {
					y.u8(4<<4 | 4)
					y.u8(0)
					y.u16(2)
					for id, e := range [][2]int{{0, len(tile)}, {len(tile), len(gridData)}} {
						y.u16(uint16(id + 1))
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
		x.box("ftyp", payload(func(y *writer) { y.str("heic"); y.u32(0); y.str("mif1"); y.str("heic") }))
	})
	metaSize := len(buildMeta(0))
	meta := buildMeta(len(ftyp) + metaSize + 8)
	var out writer
	out.raw(ftyp)
	out.raw(meta)
	out.box("mdat", append(append([]byte(nil), tile...), gridData...))
	return out.buf
}

// singleItem is a HEIC with one hvc1 item declared w x h.
func singleItem(w, h int) []byte {
	var out writer
	out.box("ftyp", payload(func(y *writer) { y.str("heic"); y.u32(0); y.str("mif1") }))
	out.full("meta", 0, 0, payload(func(x *writer) {
		x.full("hdlr", 0, 0, payload(hdlr))
		x.full("pitm", 0, 0, payload(func(y *writer) { y.u16(1) }))
		x.full("iinf", 0, 0, payload(func(y *writer) {
			y.u16(1)
			y.full("infe", 2, 0, payload(func(z *writer) { z.u16(1); z.u16(0); z.str("hvc1"); z.u8(0) }))
		}))
		x.box("iprp", payload(func(y *writer) {
			y.box("ipco", payload(func(z *writer) { z.raw(ispeBox(w, h)) }))
			y.full("ipma", 0, 0, payload(func(z *writer) { z.u32(1); z.u16(1); z.u8(1); z.u8(1) }))
		}))
		x.full("iloc", 0, 0, payload(func(y *writer) {
			y.u8(4<<4 | 4)
			y.u8(0)
			y.u16(1)
			y.u16(1)
			y.u16(0)
			y.u16(1)
			y.u32(0)
			y.u32(4)
		}))
	}))
	out.box("mdat", []byte{0, 0, 0, 0})
	return out.buf
}

// TestDecodeRefusesOversizedFiles feeds files that declare more than the
// limits allow. None carries an hvcC, so an attempt to decode would fail
// with that complaint: the error must be ErrTooLarge instead, which
// proves the refusal came before a decoder was opened. DecodeInfo keeps
// describing them.
func TestDecodeRefusesOversizedFiles(t *testing.T) {
	tile := []byte{0, 0, 0, 0}
	for name, tc := range map[string]struct {
		data   []byte
		limits Limits
		w, h   int
		tiles  int
	}{
		// 255 x 256 tiles of 256 x 256 (a dimg box counts 16 bits of
		// references): a 16 GiB canvas and 65,280 decodes in a file of
		// 130 KB.
		"65280 tiles": {gridAlias(tile, 256, 256, 255, 256), Limits{}, 255 * 256, 65536, 65280},
		// The tile count passes; the canvas does not.
		"huge canvas": {gridAlias(tile, 16384, 16384, 64, 64), Limits{}, 64 * 16384, 64 * 16384, 4096},
		// The auditor's 3072 x 3072 example passes the defaults (it is
		// smaller than a modern camera's picture) and fails a lower bound.
		"lowered pixels":    {gridAlias(tile, 256, 256, 12, 12), Limits{MaxPixels: 1 << 20}, 3072, 3072, 144},
		"lowered tiles":     {gridAlias(tile, 256, 256, 12, 12), Limits{MaxTiles: 100}, 3072, 3072, 144},
		"single item":       {singleItem(20000, 20000), Limits{}, 20000, 20000, 1},
		"grid of big tiles": {gridAlias(tile, 16384, 16384, 2, 1), Limits{MaxPixels: 16384 * 16384}, 2 * 16384, 16384, 2},
		"input size":        {singleItem(16, 16), Limits{MaxInputBytes: 64}, 16, 16, 1},
	} {
		info, err := DecodeInfo(tc.data, hwmediacodec.HEVC)
		if err != nil || info.Width != tc.w || info.Height != tc.h || info.Tiles != tc.tiles {
			t.Errorf("%s: DecodeInfo = %+v, %v; want %dx%d with %d tiles", name, info, err, tc.w, tc.h, tc.tiles)
		}
		start := time.Now()
		if _, _, err := DecodeBytes(tc.data, hwmediacodec.HEVC, tc.limits); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: DecodeBytes = %v, want ErrTooLarge", name, err)
		}
		if _, _, err := Decode(bytes.NewReader(tc.data), hwmediacodec.HEVC, tc.limits); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: Decode = %v, want ErrTooLarge", name, err)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s: refusing took %v", name, d)
		}
	}
}

// TestDecodeUnderTheLimitsReachesTheDecoder is the control: the same files
// within their limits get as far as the missing hvcC.
func TestDecodeUnderTheLimitsReachesTheDecoder(t *testing.T) {
	for name, data := range map[string][]byte{
		"grid":   gridAlias([]byte{0, 0, 0, 0}, 256, 256, 12, 12),
		"single": singleItem(16384, 16384),
	} {
		_, _, err := DecodeBytes(data, hwmediacodec.HEVC, Limits{})
		if err == nil || errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "hvcC") {
			t.Errorf("%s: %v, want the missing hvcC", name, err)
		}
	}
}

// ilocFlood is an iloc whose items declare 65,535 extents each with
// offset_size and length_size 0, so that an extent costs no bytes.
func ilocFlood(items int) []byte {
	var out writer
	out.box("ftyp", payload(func(y *writer) { y.str("heic"); y.u32(0); y.str("mif1") }))
	out.full("meta", 0, 0, payload(func(x *writer) {
		x.full("hdlr", 0, 0, payload(hdlr))
		x.full("pitm", 0, 0, payload(func(y *writer) { y.u16(1) }))
		x.full("iloc", 1, 0, payload(func(y *writer) {
			y.u8(0)
			y.u8(0)
			y.u16(uint16(items))
			for id := 1; id <= items; id++ {
				y.u16(uint16(id))
				y.u16(0) // construction method
				y.u16(0) // data reference index
				y.u16(65535)
			}
		}))
	}))
	return out.buf
}

// TestIlocRejectsFreeExtents: 20,000 items of 65,535 zero-byte extents
// took 20 seconds to walk; the extents must now be refused at once.
func TestIlocRejectsFreeExtents(t *testing.T) {
	start := time.Now()
	_, err := DecodeConfig(bytes.NewReader(ilocFlood(20000)), 0, Limits{})
	if err == nil || !strings.Contains(err.Error(), "without a length field") {
		t.Errorf("DecodeConfig = %v, want a refusal of the length-less extents", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("refusing took %v", d)
	}
	// One extent without a length field is what the specification means.
	one := payload(func(w *writer) {
		w.u32(0)
		w.u8(4 << 4)
		w.u8(0)
		w.u16(1)
		w.u16(1)
		w.u16(0)
		w.u16(1)
		w.u32(0)
	})
	f := &file{items: map[uint32]*item{}}
	if err := f.parseIloc(box{typ: "iloc", data: one}); err != nil {
		t.Errorf("single extent without a length: %v", err)
	}
}

// TestIlocCapsTheExtentTotal builds a legitimate-looking table of
// 4-byte extents whose total passes the cap.
func TestIlocCapsTheExtentTotal(t *testing.T) {
	data := payload(func(w *writer) {
		w.u32(0)
		w.u8(4) // no offsets, 4-byte lengths
		w.u8(0)
		w.u16(3)
		for id := 1; id <= 3; id++ {
			w.u16(uint16(id))
			w.u16(0)
			w.u16(65535)
			for k := 0; k < 65535; k++ {
				w.u32(1)
			}
		}
	})
	f := &file{items: map[uint32]*item{}}
	if err := f.parseIloc(box{typ: "iloc", data: data}); err == nil || !strings.Contains(err.Error(), "extents") {
		t.Errorf("%d extents accepted: %v", 3*65535, err)
	}
}

// countingSeeker records how much of the file DecodeConfig and Decode read.
type countingSeeker struct {
	*bytes.Reader
	read int
}

func (c *countingSeeker) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.read += n
	return n, err
}

// noSeek hides the Seek method.
type noSeek struct{ io.Reader }

// TestSeekableSourceReadsHeadersOnly puts a large mdat after the meta box:
// a seekable reader is charged for the headers and the grid descriptor,
// not for the pictures, and an offset reader is read from where it stands.
func TestSeekableSourceReadsHeadersOnly(t *testing.T) {
	tile := bytes.Repeat([]byte{7}, 1<<20)
	data := gridAlias(tile, 256, 256, 2, 2)
	src := &countingSeeker{Reader: bytes.NewReader(data)}
	cfg, err := DecodeConfig(src, hwmediacodec.HEVC, Limits{})
	if err != nil || cfg.Width != 512 || cfg.Height != 512 {
		t.Fatalf("DecodeConfig = %+v, %v", cfg, err)
	}
	if src.read > 2048 {
		t.Errorf("DecodeConfig read %d bytes of a %d-byte file", src.read, len(data))
	}
	// Decode gets as far as the tile's hvcC, reading the tile on the way.
	src = &countingSeeker{Reader: bytes.NewReader(data)}
	if _, _, err := Decode(src, hwmediacodec.HEVC, Limits{}); err == nil || !strings.Contains(err.Error(), "hvcC") {
		t.Fatalf("Decode = %v, want the missing hvcC", err)
	}
	if src.read < len(tile) || src.read > len(tile)+2048 {
		t.Errorf("Decode read %d bytes, want the tile (%d) and the headers", src.read, len(tile))
	}
	// A reader positioned after a prefix decodes the file that follows.
	prefixed := bytes.NewReader(append([]byte("twelve bytes"), data...))
	if _, err := prefixed.Seek(12, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if cfg, err := DecodeConfig(prefixed, hwmediacodec.HEVC, Limits{}); err != nil || cfg.Width != 512 {
		t.Errorf("DecodeConfig after a prefix = %+v, %v", cfg, err)
	}
	// Without Seek the whole input is read, up to the limit.
	if cfg, err := DecodeConfig(noSeek{bytes.NewReader(data)}, hwmediacodec.HEVC, Limits{}); err != nil || cfg.Width != 512 {
		t.Errorf("DecodeConfig of a plain reader = %+v, %v", cfg, err)
	}
	for name, r := range map[string]io.Reader{
		"plain reader": noSeek{bytes.NewReader(data)},
		"seeker":       bytes.NewReader(data),
	} {
		if _, err := DecodeConfig(r, hwmediacodec.HEVC, Limits{MaxInputBytes: 4096}); !errors.Is(err, ErrTooLarge) {
			t.Errorf("DecodeConfig of a %s over MaxInputBytes = %v, want ErrTooLarge", name, err)
		}
	}
}

// TestIpmaWideIndexes writes a grid whose tiles each carry a different
// hvcC, which takes the property count past the 127 that 7-bit ipma
// indexes hold: the file must use the 15-bit form and read back with
// every tile's own configuration.
func TestIpmaWideIndexes(t *testing.T) {
	const n = 130
	coded := make([]codedItem, n)
	for i := range coded {
		coded[i] = codedItem{config: []byte{byte(i), byte(i >> 8), 1}, data: []byte{0, 0, 0, 1, 0}, width: 2, height: 2}
	}
	var buf bytes.Buffer
	if err := writeFile(&buf, coded, n, 1, 2*n, 2, 2*n, 2, Options{Codec: hwmediacodec.HEVC}); err != nil {
		t.Fatal(err)
	}
	info, err := DecodeInfo(buf.Bytes(), hwmediacodec.HEVC)
	if err != nil || info.Tiles != n || info.Width != 2*n {
		t.Fatalf("DecodeInfo = %+v, %v", info, err)
	}
	f, err := parse(bytesSource(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range coded {
		if got, ok := f.items[uint32(i+1)].prop("hvcC"); !ok || !bytes.Equal(got, c.config) {
			t.Errorf("tile %d reads hvcC %x, wrote %x", i+1, got, c.config)
		}
	}
	coded = make([]codedItem, 0x8000)
	for i := range coded {
		coded[i] = codedItem{config: []byte{byte(i), byte(i >> 8), 2}, data: []byte{0}, width: 2, height: 2}
	}
	if err := writeFile(io.Discard, coded, 256, 128, 512, 256, 512, 256, Options{Codec: hwmediacodec.HEVC}); err == nil {
		t.Error("more properties than ipma can index were written")
	}
}
