package hevc_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// picture is what we record for every access unit of a stream.
type picture struct {
	nalType int
	poc     int
	l0, l1  []int // POCs of RefPicList0 and RefPicList1 of the first slice
	refs    int   // reference pictures held while the picture is decoded
	missing int   // stand-ins for references the stream never delivered
	skipped bool
}

// walkStream drives the DPB over every access unit of an Annex-B stream,
// starting at access unit start.
func walkStream(t *testing.T, data []byte, start int) (pics []picture, released int) {
	t.Helper()
	ps := hevc.NewParameterSets()
	dpb := hevc.NewDPB()
	dpb.Release = func(*hevc.Picture) { released++ }
	r := annexb.NewReader(bytes.NewReader(data), codec.HEVC)
	for index := 0; ; index++ {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var slices [][]byte
		for _, nal := range annexb.Split(au) {
			typ := hevc.Type(nal)
			switch {
			case typ == hevc.NALSPS:
				if _, err := ps.AddSPS(nal); err != nil {
					t.Fatal(err)
				}
			case typ == hevc.NALPPS:
				if _, err := ps.AddPPS(nal); err != nil {
					t.Fatal(err)
				}
			case hevc.IsSlice(typ):
				slices = append(slices, nal)
			}
		}
		if len(slices) == 0 || index < start {
			continue
		}
		var prev *hevc.SliceHeader
		var cur *hevc.Picture
		var rec picture
		for i, nal := range slices {
			sh, sps, _, err := hevc.ParseSliceHeader(nal, ps, prev)
			if err != nil {
				t.Fatalf("access unit %d slice %d: %v", index, i, err)
			}
			prev = sh
			if i == 0 {
				if !sh.FirstSliceSegmentInPic {
					t.Fatalf("access unit %d does not start a picture", index)
				}
				cur, err = dpb.Start(sps, sh, index)
				if errors.Is(err, hevc.ErrSkipped) || errors.Is(err, hevc.ErrNoKeyframe) {
					rec = picture{nalType: sh.NALType, skipped: true}
					break
				}
				if err != nil {
					t.Fatalf("access unit %d: Start: %v", index, err)
				}
				rec = picture{nalType: sh.NALType, poc: int(cur.POC)}
				for _, p := range dpb.Refs() {
					rec.refs++
					if p.Missing {
						rec.missing++
					}
				}
				if rec.refs > sps.MaxDecPicBuffering()-1 {
					t.Errorf("access unit %d: %d reference pictures exceed sps_max_dec_pic_buffering_minus1 = %d",
						index, rec.refs, sps.MaxDecPicBuffering()-1)
				}
			}
			l0, l1 := dpb.RefPicLists(sh)
			if len(l0) != int(sh.NumRefIdxL0Active) || len(l1) != int(sh.NumRefIdxL1Active) {
				t.Errorf("access unit %d slice %d: lists of %d and %d entries, header says %d and %d",
					index, i, len(l0), len(l1), sh.NumRefIdxL0Active, sh.NumRefIdxL1Active)
			}
			pocs := func(l []*hevc.Picture) []int {
				var out []int
				for _, p := range l {
					if p == nil {
						t.Errorf("access unit %d slice %d: empty reference list entry", index, i)
						continue
					}
					out = append(out, int(p.POC))
				}
				return out
			}
			if i == 0 {
				rec.l0, rec.l1 = pocs(l0), pocs(l1)
			} else if got0, got1 := pocs(l0), pocs(l1); !reflect.DeepEqual(got0, rec.l0) || !reflect.DeepEqual(got1, rec.l1) {
				// x265 gives every slice of a picture the same lists.
				t.Errorf("access unit %d slice %d: lists %v %v differ from the first slice's %v %v", index, i, got0, got1, rec.l0, rec.l1)
			}
		}
		if !rec.skipped {
			dpb.Finish()
		}
		pics = append(pics, rec)
	}
	dpb.Reset()
	return pics, released
}

var x265Configs = []struct {
	name string
	args []string
}{
	{"default", []string{"--keyint", "12", "--min-keyint", "12"}},
	{"bpyramid", []string{"--bframes", "4", "--b-pyramid", "--ref", "5", "--keyint", "20", "--min-keyint", "20", "--no-open-gop"}},
	{"no-bpyramid", []string{"--bframes", "5", "--no-b-pyramid", "--ref", "3", "--keyint", "16", "--min-keyint", "16", "--no-open-gop"}},
	{"lowdelay", []string{"--bframes", "0", "--ref", "6", "--keyint", "30", "--weightp"}},
	{"open-gop", []string{"--bframes", "3", "--b-pyramid", "--ref", "4", "--keyint", "10", "--min-keyint", "10", "--open-gop"}},
	{"slices", []string{"--bframes", "3", "--ref", "4", "--keyint", "15", "--min-keyint", "15", "--slices", "3", "--weightb"}},
	{"temporal-layers", []string{"--bframes", "3", "--b-pyramid", "--temporal-layers", "3", "--keyint", "15", "--min-keyint", "15"}},
}

// TestRefPicListsMatchX265 checks the picture order count and both
// reference picture lists of every picture against the lists the x265
// encoder reports it used. The lists follow from the reference picture set
// and the list construction process, so this covers 8.3.1, 8.3.2 and 8.3.4.
func TestRefPicListsMatchX265(t *testing.T) {
	for _, c := range x265Configs {
		t.Run(c.name, func(t *testing.T) {
			path, frames := testutil.GenerateX265(t, 320, 240, 48, c.args...)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			pics, released := walkStream(t, data, 0)
			if len(pics) != len(frames) {
				t.Fatalf("walked %d pictures, x265 encoded %d", len(pics), len(frames))
			}
			inter, maxRefs := 0, 0
			for i, p := range pics {
				f := frames[i]
				if p.skipped {
					t.Fatalf("picture %d was skipped", i)
				}
				if p.missing != 0 {
					t.Errorf("picture %d: %d missing references", i, p.missing)
				}
				if p.poc != f.POC {
					t.Errorf("picture %d: POC %d, x265 says %d", i, p.poc, f.POC)
				}
				if !reflect.DeepEqual(p.l0, f.List0) || !reflect.DeepEqual(p.l1, f.List1) {
					t.Errorf("picture %d (%s, POC %d): lists %v %v, x265 used %v %v", i, f.Type, f.POC, p.l0, p.l1, f.List0, f.List1)
				}
				if len(f.List0) > 0 {
					inter++
				}
				maxRefs = max(maxRefs, p.refs)
			}
			if inter == 0 {
				t.Fatal("no inter pictures compared")
			}
			if released != len(pics) {
				t.Errorf("%d pictures decoded, %d released", len(pics), released)
			}
			t.Logf("%d pictures, %d with references, up to %d reference pictures held", len(pics), inter, maxRefs)
		})
	}
}

// TestSPSReferencePictureSets uses x265's two-pass mode, which moves the
// reference picture sets into the SPS, to check sets selected by
// short_term_ref_pic_set_idx.
func TestSPSReferencePictureSets(t *testing.T) {
	stats := filepath.Join(t.TempDir(), "x265.stats")
	common := []string{"--bframes", "3", "--b-pyramid", "--ref", "4", "--keyint", "12", "--min-keyint", "12",
		"--bitrate", "300", "--stats", stats, "--multi-pass-opt-rps"}
	testutil.GenerateX265(t, 320, 240, 36, append([]string{"--pass", "1"}, common...)...)
	path, frames := testutil.GenerateX265(t, 320, 240, 36, append([]string{"--pass", "2"}, common...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	units := parseStream(t, data)
	fromSPS := 0
	for _, u := range units {
		if u.kind == "Slice Segment Header" {
			if v := u.values["short_term_ref_pic_set_sps_flag"]; len(v) == 1 && v[0] == 1 {
				fromSPS++
			}
		}
	}
	if fromSPS == 0 {
		t.Skip("this x265 build kept the reference picture sets in the slice headers")
	}
	if slices, _ := compareWithTrace(t, path, units); slices == 0 {
		t.Fatal("no slice segments compared")
	}
	pics, _ := walkStream(t, data, 0)
	if len(pics) != len(frames) {
		t.Fatalf("walked %d pictures, x265 encoded %d", len(pics), len(frames))
	}
	for i, p := range pics {
		f := frames[i]
		if p.poc != f.POC || !reflect.DeepEqual(p.l0, f.List0) || !reflect.DeepEqual(p.l1, f.List1) || p.missing != 0 {
			t.Errorf("picture %d: POC %d lists %v %v missing %d, x265 says POC %d lists %v %v", i, p.poc, p.l0, p.l1, p.missing, f.POC, f.List0, f.List1)
		}
	}
	t.Logf("%d slice segments take their reference picture set from the SPS", fromSPS)
}

// TestRandomAccessSkipsRASL starts decoding at a CRA picture in the middle
// of an open-GOP stream: the RASL pictures that follow it refer to pictures
// before the CRA and must be skipped, and nothing else may miss a reference.
func TestRandomAccessSkipsRASL(t *testing.T) {
	path := testutil.GenerateHEVC(t, 320, 240, 40, "bframes=3:b-pyramid=1:keyint=10:min-keyint=10:open-gop=1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := walkStream(t, data, 0)
	start := -1
	for i, p := range all {
		if p.skipped || p.missing != 0 {
			t.Fatalf("picture %d: skipped=%v missing=%d when decoding from the start", i, p.skipped, p.missing)
		}
		if i > 0 && p.nalType == hevc.NALCRA && start < 0 {
			start = i
		}
	}
	if start < 0 {
		t.Skip("the stream has no CRA picture after its start")
	}
	pics, _ := walkStream(t, data, start)
	if pics[0].nalType != hevc.NALCRA {
		t.Fatalf("decoding starts at NAL type %d", pics[0].nalType)
	}
	rasl, skipped := 0, 0
	for i, p := range pics {
		isRASL := hevc.IsRASL(p.nalType)
		if isRASL {
			rasl++
		}
		if p.skipped {
			skipped++
			if !isRASL {
				t.Errorf("picture %d (type %d) was skipped", i, p.nalType)
			}
		} else if p.missing != 0 {
			// Only the RASL pictures of the first CRA are undecodable;
			// everything else finds its references.
			t.Errorf("picture %d (type %d, POC %d) misses %d references", i, p.nalType, p.poc, p.missing)
		}
	}
	if rasl == 0 {
		t.Skip("the stream has no RASL pictures")
	}
	if skipped == 0 {
		t.Error("no RASL picture was skipped after the random access point")
	}
	// POCs after the random access point keep their distance to the CRA.
	want := all[start:]
	for i, p := range pics {
		if p.skipped {
			continue
		}
		if p.poc-pics[0].poc != want[i].poc-want[0].poc {
			t.Errorf("picture %d: POC distance to the CRA is %d, was %d when decoding from the start", i, p.poc-pics[0].poc, want[i].poc-want[0].poc)
		}
	}
	t.Logf("started at picture %d; %d RASL pictures, %d skipped", start, rasl, skipped)
}
