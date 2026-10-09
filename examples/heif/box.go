// Package heif reads and writes HEIF still images (HEIC with HEVC, AVIF
// with AV1) with the hardware codecs. A HEIF file is an ISOBMFF "meta"
// box describing items (coded pictures, a "grid" that tiles them, their
// properties: decoder configuration, size, rotation, colour) and an
// "mdat" holding the coded data. The codec work is one keyframe per item;
// everything else here is the box plumbing, written out by hand because it
// is small and because the point of the sample is to show where the codec
// library fits.
package heif

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// box is one ISOBMFF box: its type, its payload (without the header) and
// the absolute file offset of that payload.
type box struct {
	typ   string
	data  []byte
	start int
}

// parseBoxes splits data (which starts at absolute offset base) into boxes.
func parseBoxes(data []byte, base int) ([]box, error) {
	var out []box
	for off := 0; off < len(data); {
		if off+8 > len(data) {
			return nil, errors.New("heif: truncated box header")
		}
		size := int(binary.BigEndian.Uint32(data[off:]))
		typ := string(data[off+4 : off+8])
		hdr := 8
		switch size {
		case 0:
			size = len(data) - off
		case 1:
			if off+16 > len(data) {
				return nil, errors.New("heif: truncated large box header")
			}
			size = int(binary.BigEndian.Uint64(data[off+8:]))
			hdr = 16
		}
		if size < hdr || off+size > len(data) {
			return nil, fmt.Errorf("heif: box %q has size %d beyond the data", typ, size)
		}
		out = append(out, box{typ: typ, data: data[off+hdr : off+size], start: base + off + hdr})
		off += size
	}
	return out, nil
}

// find returns the first box of the given type.
func find(boxes []box, typ string) (box, bool) {
	for _, b := range boxes {
		if b.typ == typ {
			return b, true
		}
	}
	return box{}, false
}

// fullBox splits a FullBox payload into version, flags and the rest.
func fullBox(b box) (version byte, flags uint32, payload []byte, err error) {
	if len(b.data) < 4 {
		return 0, 0, nil, fmt.Errorf("heif: box %q too short for a full box", b.typ)
	}
	return b.data[0], binary.BigEndian.Uint32(b.data[:4]) & 0xffffff, b.data[4:], nil
}

// reader is a bounds-checked big-endian cursor; the first error sticks.
type reader struct {
	b   []byte
	off int
	err error
}

func (r *reader) need(n int) bool {
	if r.err != nil {
		return false
	}
	if r.off+n > len(r.b) {
		r.err = errors.New("heif: box payload truncated")
		return false
	}
	return true
}

func (r *reader) u8() uint8 {
	if !r.need(1) {
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}

func (r *reader) u16() uint16 {
	if !r.need(2) {
		return 0
	}
	v := binary.BigEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

func (r *reader) u32() uint32 {
	if !r.need(4) {
		return 0
	}
	v := binary.BigEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *reader) u64() uint64 {
	if !r.need(8) {
		return 0
	}
	v := binary.BigEndian.Uint64(r.b[r.off:])
	r.off += 8
	return v
}

// uint reads an integer of size bytes (0, 4 or 8, as iloc sizes are).
func (r *reader) uint(size int) uint64 {
	switch size {
	case 0:
		return 0
	case 4:
		return uint64(r.u32())
	case 8:
		return r.u64()
	}
	r.err = fmt.Errorf("heif: unsupported field size %d", size)
	return 0
}

func (r *reader) bytes(n int) []byte {
	if !r.need(n) {
		return nil
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v
}

func (r *reader) cstring() string {
	if r.err != nil {
		return ""
	}
	for i := r.off; i < len(r.b); i++ {
		if r.b[i] == 0 {
			s := string(r.b[r.off:i])
			r.off = i + 1
			return s
		}
	}
	r.err = errors.New("heif: unterminated string")
	return ""
}

// writer builds boxes.
type writer struct {
	buf []byte
}

func (w *writer) u8(v uint8)   { w.buf = append(w.buf, v) }
func (w *writer) u16(v uint16) { w.buf = binary.BigEndian.AppendUint16(w.buf, v) }
func (w *writer) u32(v uint32) { w.buf = binary.BigEndian.AppendUint32(w.buf, v) }
func (w *writer) raw(b []byte) { w.buf = append(w.buf, b...) }
func (w *writer) str(s string) { w.buf = append(w.buf, s...) }

// box appends a box of the given type around payload.
func (w *writer) box(typ string, payload []byte) {
	w.u32(uint32(8 + len(payload)))
	w.str(typ)
	w.raw(payload)
}

// full appends a FullBox.
func (w *writer) full(typ string, version byte, flags uint32, payload []byte) {
	w.u32(uint32(12 + len(payload)))
	w.str(typ)
	w.u32(uint32(version)<<24 | flags&0xffffff)
	w.raw(payload)
}

// payload runs f on a fresh writer and returns what it wrote.
func payload(f func(w *writer)) []byte {
	var w writer
	f(&w)
	return w.buf
}
