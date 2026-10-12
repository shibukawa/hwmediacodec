package webcodecs

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/mp4ff/avc"
	mp4hevc "github.com/Eyevinn/mp4ff/hevc"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// splitSets separates the parameter sets of a stream's first access unit
// from its other NAL units.
func splitSets(t *testing.T, c codec.Codec, stream []byte) (sets [3][][]byte, rest [][]byte) {
	t.Helper()
	au, err := annexb.NewReader(bytes.NewReader(stream), c).Next()
	if err != nil {
		t.Fatal(err)
	}
	for _, nal := range annexb.Split(au) {
		typ := annexb.NALUnitType(c, nal)
		if kind, _, ok := annexb.ParameterSetID(c, typ, nal); ok {
			sets[kind] = append(sets[kind], nal)
		} else {
			rest = append(rest, nal)
		}
	}
	if len(sets[annexb.ParamSPS]) == 0 || len(sets[annexb.ParamPPS]) == 0 || len(rest) == 0 {
		t.Fatal("the first access unit lacks parameter sets or a picture")
	}
	return sets, rest
}

// TestAddDescription feeds the decoder configuration records mp4ff builds
// from an ffmpeg stream's parameter sets (what an encoder that ignores the
// request for Annex-B output hands over) and checks that the parameter
// sets come back and that a length-prefixed sample turns into the access
// unit it was made from.
func TestAddDescription(t *testing.T) {
	for _, c := range []codec.Codec{codec.H264, codec.HEVC} {
		t.Run(c.String(), func(t *testing.T) {
			s := testutil.GenerateStream(t, c, 320, 240, 2)
			sets, rest := splitSets(t, c, testutil.ReadFile(t, s.Path))

			var rec bytes.Buffer
			if c == codec.H264 {
				r, err := avc.CreateAVCDecConfRec(sets[annexb.ParamSPS], sets[annexb.ParamPPS], true)
				if err != nil {
					t.Fatal(err)
				}
				if err := r.Encode(&rec); err != nil {
					t.Fatal(err)
				}
			} else {
				r, err := mp4hevc.CreateHEVCDecConfRec(sets[annexb.ParamVPS], sets[annexb.ParamSPS], sets[annexb.ParamPPS], true, true, true, true)
				if err != nil {
					t.Fatal(err)
				}
				if err := r.Encode(&rec); err != nil {
					t.Fatal(err)
				}
			}

			ps := newParamSets(c)
			lengthSize, err := ps.addDescription(rec.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if lengthSize != 4 {
				t.Fatalf("length size %d, want 4", lengthSize)
			}
			var want []byte
			for kind := range sets {
				for _, nal := range sets[kind] {
					want = annexb.AppendUnit(want, nal)
				}
			}
			if got := ps.annexB(); !bytes.Equal(got, want) {
				t.Fatalf("parameter sets differ:\n got %x\nwant %x", got, want)
			}

			// The sample of the record's stream: the picture, length-prefixed.
			var sample, picture []byte
			for _, nal := range rest {
				sample = annexb.AppendLengthPrefixed(sample, nal)
				picture = annexb.AppendUnit(picture, nal)
			}
			converted, err := annexb.FromLengthPrefixed(nil, sample, lengthSize)
			if err != nil || !bytes.Equal(converted, picture) {
				t.Fatalf("length-prefixed sample did not convert back: %v", err)
			}
			info := ps.inspect(converted)
			if !info.vcl || !info.keyframe || ps.covers(info.kinds) {
				t.Fatalf("sample classified as %+v", info)
			}
			// An Annex-B chunk does not pass for a length-prefixed one.
			if _, err := annexb.FromLengthPrefixed(nil, picture, lengthSize); err == nil {
				t.Error("Annex-B bytes parsed as length-prefixed NAL units")
			}

			limit := rec.Len()
			if c == codec.H264 {
				// A High profile record ends with the chroma format, the
				// bit depths and an SPS extension count, which are not
				// read.
				limit -= 4
			}
			for n := 0; n < limit; n++ {
				if _, err := newParamSets(c).addDescription(rec.Bytes()[:n]); err == nil {
					t.Fatalf("record truncated to %d bytes accepted", n)
				}
			}
		})
	}
}

func TestParamSetsCoverAndOrder(t *testing.T) {
	ps := newParamSets(codec.H264)
	// SPS ids 1 and 0, PPS id 0: sps_id and pps_id are the first ue(v).
	sps1 := []byte{0x67, 0x64, 0x00, 0x1f, 0x40} // ue 1 = 010
	sps0 := []byte{0x67, 0x64, 0x00, 0x1f, 0x80} // ue 0 = 1
	pps0 := []byte{0x68, 0xC0}                   // pps 0, sps 0
	var au []byte
	for _, nal := range [][]byte{sps1, sps0, pps0, {0x65, 0x88}} {
		au = annexb.AppendUnit(au, nal)
	}
	info := ps.inspect(au)
	if info.nals != 4 || !info.vcl || !info.keyframe || !ps.covers(info.kinds) {
		t.Fatalf("classified as %+v", info)
	}
	if !bytes.Equal(ps.lastSPS, sps0) {
		t.Errorf("last SPS %x", ps.lastSPS)
	}
	var want []byte
	for _, nal := range [][]byte{sps0, sps1, pps0} {
		want = annexb.AppendUnit(want, nal)
	}
	if got := ps.annexB(); !bytes.Equal(got, want) {
		t.Errorf("stored sets %x, want %x (ascending ids, SPS before PPS)", got, want)
	}
	if info := ps.inspect(annexb.AppendUnit(nil, []byte{0x41, 0x9a})); info.keyframe || !info.vcl || ps.covers(info.kinds) {
		t.Errorf("P slice classified as %+v", info)
	}
	if info := ps.inspect([]byte("not a bitstream")); info.nals != 0 {
		t.Errorf("garbage has %d NAL units", info.nals)
	}
}
