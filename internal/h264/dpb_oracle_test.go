package h264_test

import (
	"bytes"
	"io"
	"os"
	"sort"
	"testing"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

type countingSurfaces struct{ allocated, released int }

func (c *countingSurfaces) Allocate() (any, error) { c.allocated++; return c.allocated, nil }
func (c *countingSurfaces) Release(*h264.Picture)  { c.released++ }

// picture is what we record for every access unit of a stream.
type picture struct {
	idr       bool
	ref       bool
	frameNum  int
	topPOC    int
	bottomPOC int
	poc       int
	slices    int
	state     testutil.RefList // DPB after the picture (reference pictures only)
}

// walkStream drives the DPB over every access unit of an Annex-B stream.
func walkStream(t *testing.T, data []byte) []picture {
	t.Helper()
	ps := h264.NewParameterSets()
	dpb := h264.NewDPB(&countingSurfaces{})
	r := annexb.NewReader(bytes.NewReader(data), codec.H264)
	var out []picture
	for {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var first []byte
		slices := 0
		for _, nal := range annexb.Split(au) {
			_, typ, _ := h264.NALHeader(nal)
			switch typ {
			case h264.NALSPS:
				if _, err := ps.AddSPS(nal); err != nil {
					t.Fatal(err)
				}
			case h264.NALPPS:
				if err := ps.AddPPS(nal); err != nil {
					t.Fatal(err)
				}
			case h264.NALSlice, h264.NALSliceIDR:
				slices++
				if first == nil {
					first = nal
				}
			}
		}
		if first == nil {
			continue
		}
		sh, sps, _, err := h264.ParseSliceHeader(first, ps)
		if err != nil {
			t.Fatal(err)
		}
		cur, err := dpb.Start(sps, sh, len(out))
		if err != nil {
			t.Fatalf("picture %d: Start: %v", len(out), err)
		}
		// Every slice of the picture must yield lists of the requested size.
		for _, nal := range annexb.Split(au) {
			_, typ, _ := h264.NALHeader(nal)
			if typ != h264.NALSlice && typ != h264.NALSliceIDR {
				continue
			}
			h, _, _, err := h264.ParseSliceHeader(nal, ps)
			if err != nil {
				t.Fatal(err)
			}
			l0, l1 := dpb.RefPicLists(h)
			if len(l0) != int(h.NumRefIdxL0Active) || len(l1) != int(h.NumRefIdxL1Active) {
				t.Fatalf("picture %d: list sizes %d/%d, want %d/%d", len(out), len(l0), len(l1), h.NumRefIdxL0Active, h.NumRefIdxL1Active)
			}
			for i, p := range l0 {
				if p == nil {
					t.Errorf("picture %d (%s, frame_num %d): RefPicList0[%d] has no reference picture", len(out), h.SliceType, h.FrameNum, i)
				}
			}
			for i, p := range l1 {
				if p == nil {
					t.Errorf("picture %d (%s, frame_num %d): RefPicList1[%d] has no reference picture", len(out), h.SliceType, h.FrameNum, i)
				}
			}
		}
		if err := dpb.Finish(sh); err != nil {
			t.Fatal(err)
		}
		p := picture{idr: sh.IDR, ref: sh.NALRefIdc != 0, frameNum: int(sh.FrameNum), topPOC: cur.TopPOC, bottomPOC: cur.BottomPOC, poc: cur.POC(), slices: slices}
		for _, ref := range dpb.Refs() {
			e := testutil.RefEntry{FrameNum: ref.FrameNum, POC: ref.POC()}
			if ref.LongTerm {
				e.Index = ref.LongTermFrameIdx
				p.state.Long = append(p.state.Long, e)
			} else {
				p.state.Short = append(p.state.Short, e)
			}
		}
		out = append(out, p)
	}
	return out
}

func sortRefs(l []testutil.RefEntry) {
	sort.Slice(l, func(i, j int) bool {
		if l[i].FrameNum != l[j].FrameNum {
			return l[i].FrameNum < l[j].FrameNum
		}
		return l[i].POC < l[j].POC
	})
}

// TestDPBMatchesFFmpeg compares the reference picture set after every
// reference picture with what ffmpeg's decoder keeps (-debug mmco). POC
// values are compared up to the constant offset ffmpeg adds internally.
func TestDPBMatchesFFmpeg(t *testing.T) {
	for _, s := range oracleStreams {
		t.Run(s.name, func(t *testing.T) {
			path := testutil.GenerateH264(t, s.w, s.h, s.n, s.args...)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := testutil.DPBTrace(t, path)
			pics := walkStream(t, data)
			got := []testutil.RefList{{}}
			for _, p := range pics {
				if !p.ref {
					continue
				}
				// ffmpeg empties its lists when an IDR slice starts and logs
				// that intermediate state before marking the IDR picture.
				if p.idr && len(got[len(got)-1].Short)+len(got[len(got)-1].Long) > 0 {
					got = append(got, testutil.RefList{})
				}
				got = append(got, p.state)
			}
			if len(got) != len(want) {
				t.Fatalf("we have %d DPB states, ffmpeg logged %d", len(got), len(want))
			}
			offset, haveOffset := 0, false
			for i := range want {
				g, w := got[i], want[i]
				sortRefs(g.Short)
				sortRefs(w.Short)
				if len(g.Short) != len(w.Short) || len(g.Long) != len(w.Long) {
					t.Fatalf("state %d: %d short/%d long refs, ffmpeg has %d/%d\nours: %+v\nffmpeg: %+v", i, len(g.Short), len(g.Long), len(w.Short), len(w.Long), g, w)
				}
				for j := range g.Short {
					if g.Short[j].FrameNum != w.Short[j].FrameNum {
						t.Fatalf("state %d: short refs %+v, ffmpeg %+v", i, g.Short, w.Short)
					}
					d := w.Short[j].POC - g.Short[j].POC
					if !haveOffset {
						offset, haveOffset = d, true
					}
					if d != offset {
						t.Fatalf("state %d: frame_num %d POC %d, ffmpeg %d (offset %d elsewhere)", i, g.Short[j].FrameNum, g.Short[j].POC, w.Short[j].POC, offset)
					}
				}
				sort.Slice(g.Long, func(a, b int) bool { return g.Long[a].Index < g.Long[b].Index })
				sort.Slice(w.Long, func(a, b int) bool { return w.Long[a].Index < w.Long[b].Index })
				for j := range g.Long {
					if g.Long[j].Index != w.Long[j].Index || g.Long[j].FrameNum != w.Long[j].FrameNum {
						t.Fatalf("state %d: long refs %+v, ffmpeg %+v", i, g.Long, w.Long)
					}
				}
			}
			t.Logf("%d pictures, %d DPB states match (POC offset %d)", len(pics), len(got), offset)
		})
	}
}

// TestPOCMatchesFFmpeg compares the picture order count of every picture
// with the value ffmpeg's decoder derives (-debug pict), up to the constant
// offset ffmpeg adds internally. Equal POCs mean equal display order.
func TestPOCMatchesFFmpeg(t *testing.T) {
	for _, s := range oracleStreams {
		t.Run(s.name, func(t *testing.T) {
			path := testutil.GenerateH264(t, s.w, s.h, s.n, s.args...)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := testutil.POCTrace(t, path)
			pics := walkStream(t, data)
			if len(want) == 0 {
				t.Fatal("ffmpeg logged no slices")
			}
			offset, haveOffset := 0, false
			k := 0
			for i, p := range pics {
				if k+p.slices > len(want) {
					t.Fatalf("picture %d: ffmpeg logged only %d slices", i, len(want))
				}
				for j := 0; j < p.slices; j++ {
					w := want[k+j]
					if w.FrameNum != p.frameNum {
						t.Fatalf("picture %d slice %d: frame_num %d, ffmpeg %d", i, j, p.frameNum, w.FrameNum)
					}
					d := w.TopPOC - p.topPOC
					if !haveOffset {
						offset, haveOffset = d, true
					}
					if d != offset || w.BottomPOC-p.bottomPOC != offset {
						t.Fatalf("picture %d: POC %d/%d, ffmpeg %d/%d (offset %d elsewhere)", i, p.topPOC, p.bottomPOC, w.TopPOC, w.BottomPOC, offset)
					}
				}
				k += p.slices
			}
			if k != len(want) {
				t.Fatalf("ffmpeg logged %d slices, we saw %d", len(want), k)
			}
			t.Logf("%d pictures, %d slices match (POC offset %d)", len(pics), k, offset)
		})
	}
}
