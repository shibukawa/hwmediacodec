package imageitem

import (
	"encoding/binary"
	"math"
	"reflect"
	"testing"
)

func TestParseBoxesRejectsOverflow(t *testing.T) {
	for _, size := range []uint64{math.MaxInt64, math.MaxInt64 + 8, math.MaxUint64} {
		data := make([]byte, 24)
		binary.BigEndian.PutUint32(data, 8)
		copy(data[4:], "free")
		binary.BigEndian.PutUint32(data[8:], 1)
		copy(data[12:], "free")
		binary.BigEndian.PutUint64(data[16:], size)
		if _, err := parseBoxes(data, 0); err == nil {
			t.Errorf("box size %d accepted", size)
		}
	}
	r := &reader{b: make([]byte, 4), off: 1}
	if r.bytes(math.MaxInt) != nil || r.err == nil {
		t.Fatal("overflowing cursor read accepted")
	}
}

func TestIlocRejectsOffsetOverflow(t *testing.T) {
	data := payload(func(w *writer) {
		w.u32(0) // full box
		w.u8(0x88)
		w.u8(0x80)
		w.u16(1)
		w.u16(1) // item ID
		w.u16(0) // data reference
		w.raw(binary.BigEndian.AppendUint64(nil, math.MaxUint64))
		w.u16(1)
		w.raw(binary.BigEndian.AppendUint64(nil, 1))
		w.raw(binary.BigEndian.AppendUint64(nil, 1))
	})
	f := &file{items: map[uint32]*item{}}
	if err := f.parseIloc(box{typ: "iloc", data: data}); err == nil {
		t.Fatal("wrapping base offset accepted")
	}
}

func TestIrefVersionOneUses16BitCount(t *testing.T) {
	data := payload(func(w *writer) {
		w.u32(1 << 24)
		w.box("dimg", payload(func(w *writer) {
			w.u32(70000)
			w.u16(2)
			w.u32(70001)
			w.u32(70002)
		}))
	})
	f := &file{items: map[uint32]*item{}}
	if err := f.parseIref(box{typ: "iref", data: data}); err != nil {
		t.Fatal(err)
	}
	if got := f.items[70000].dimg; !reflect.DeepEqual(got, []uint32{70001, 70002}) {
		t.Fatalf("references = %v", got)
	}
}

func TestPayloadRejectsExtentAmplification(t *testing.T) {
	f := &file{src: bytesSource(make([]byte, 8))}
	if _, err := f.payload(&item{extents: [][2]uint64{{0, 8}, {0, 8}}}); err == nil {
		t.Fatal("repeated extents amplified the payload")
	}
}

func TestGridRejectsUnrepresentableDimensions(t *testing.T) {
	data := []byte{0, 1, 0, 0}
	data = binary.BigEndian.AppendUint32(data, math.MaxUint32)
	data = binary.BigEndian.AppendUint32(data, math.MaxUint32)
	f := &file{src: bytesSource(data), items: map[uint32]*item{2: {id: 2, typ: "hvc1"}}}
	if _, err := f.grid(&item{extents: [][2]uint64{{0, uint64(len(data))}}, dimg: []uint32{2}}); err == nil {
		t.Fatal("overflowing grid accepted")
	}
}

func TestMalformedAV1OBUSizes(t *testing.T) {
	for _, data := range [][]byte{
		{0x0e},       // sequence header with a missing extension
		{0x0a, 0x80}, // incomplete length
		{0x0a, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		{0x0a, 0x20}, // payload length larger than the temporal unit
	} {
		if hasSequenceHeader(data) {
			t.Errorf("malformed sequence header accepted: %x", data)
		}
		if _, err := av1Item(data); err == nil {
			t.Errorf("malformed AV1 temporal unit accepted: %x", data)
		}
	}
}
