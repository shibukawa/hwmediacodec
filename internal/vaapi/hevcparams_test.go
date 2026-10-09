package vaapi

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

// Capability words as drivers report them.
const (
	// AMD VCN: 64x64 coding tree blocks, 8x8 minimum coding blocks, 4x4 to
	// 32x32 transforms, no transform hierarchy.
	amdBlockSizes = 3<<sys.HEVCBlockLog2MaxCodingTreeBlockSizeMinus3Shift |
		3<<sys.HEVCBlockLog2MinCodingTreeBlockSizeMinus3Shift |
		3<<sys.HEVCBlockLog2MaxLumaTransformBlockSizeMinus2Shift
	// AMP, SAO, strong intra smoothing, constrained intra prediction,
	// cu_qp_delta and deblocking control offered, none forced.
	amdFeatures = 1<<sys.HEVCFeatureAmpShift | 1<<sys.HEVCFeatureSaoShift |
		1<<sys.HEVCFeatureStrongIntraSmoothingShift | 1<<sys.HEVCFeatureConstrainedIntraPredShift |
		1<<sys.HEVCFeatureCuQpDeltaShift | 1<<sys.HEVCFeatureDeblockingFilterDisableShift
)

func TestHEVCToolsFor(t *testing.T) {
	guess := hevcToolsFor(0, false, 0, false, true)
	want := hevcTools{ctbLog2: 5, minCbLog2: 4, minTbLog2: 2, maxTbLog2: 5, depthInter: 3, depthIntra: 3, amp: true, cuQpDelta: true}
	if guess != want {
		t.Errorf("without driver attributes: %+v, want %+v", guess, want)
	}
	amd := hevcToolsFor(amdBlockSizes, true, amdFeatures, true, false)
	want = hevcTools{ctbLog2: 6, minCbLog2: 3, minTbLog2: 2, maxTbLog2: 5, amp: true, sao: true}
	if amd != want {
		t.Errorf("AMD attributes, constant QP: %+v, want %+v", amd, want)
	}
	if !hevcToolsFor(amdBlockSizes, true, amdFeatures, true, true).cuQpDelta {
		t.Error("rate control needs cu_qp_delta")
	}
	// Tools a driver always uses must be signalled.
	forced := hevcToolsFor(amdBlockSizes, true, 2<<sys.HEVCFeatureStrongIntraSmoothingShift|2<<sys.HEVCFeatureSignDataHidingShift|
		2<<sys.HEVCFeatureTransformSkipShift|2<<sys.HEVCFeatureCuQpDeltaShift, true, false)
	if !forced.strongIntraSmoothing || !forced.signDataHiding || !forced.transformSkip || !forced.cuQpDelta || forced.amp || forced.sao {
		t.Errorf("forced tools: %+v", forced)
	}
	// Nonsense from a driver must not produce an invalid stream.
	odd := hevcToolsFor(0, true, 0, true, false) // 8x8 coding tree blocks, 4x4 transforms only
	if odd.ctbLog2 != 4 || odd.minCbLog2 != 3 || odd.minTbLog2 != 2 || odd.maxTbLog2 != 2 || odd.amp {
		t.Errorf("clamped structure: %+v", odd)
	}
}

func TestHEVCLevel(t *testing.T) {
	for _, tc := range []struct {
		w, h     int
		fps      uint32
		kbps     int
		want     uint8
		describe string
	}{
		{320, 240, 30, 1000, 60, "QVGA: level 2"},
		{1280, 720, 30, 4000, 93, "720p30: level 3.1"},
		{1920, 1080, 30, 8000, 120, "1080p30: level 4"},
		{1920, 1080, 60, 8000, 123, "1080p60: level 4.1"},
		{1920, 1080, 30, 15000, 123, "1080p30 at 15 Mbit/s: level 4.1"},
		{3840, 2160, 30, 20000, 150, "2160p30: level 5"},
		{3840, 2160, 60, 20000, 153, "2160p60: level 5.1"},
		{7680, 4320, 60, 80000, 183, "4320p60: level 6.1"},
		{4096, 64, 30, 500, 120, "very wide: the side limit (level 4), not the area (level 2.1), decides"},
		{1920, 1080, 0, 0, 120, "unknown frame rate and bitrate"},
	} {
		den := uint32(1)
		if tc.fps == 0 {
			den = 0
		}
		if got := hevcLevelIDC(tc.w, tc.h, tc.fps, den, tc.kbps); got != tc.want {
			t.Errorf("%s: level_idc %d, want %d", tc.describe, got, tc.want)
		}
	}
}

// encoderStream assembles what the encoder puts on the wire: the parameter
// sets and, for an IDR picture and the P pictures after it, the packed
// slice headers followed by fake slice data.
func encoderStream(vps *hevc.VPS, sps *hevc.SPS, pps *hevc.PPS, pictures int) []byte {
	var buf bytes.Buffer
	add := func(nal []byte) {
		buf.Write([]byte{0, 0, 0, 1})
		buf.Write(nal)
	}
	add(hevc.WriteVPS(vps))
	add(hevc.WriteSPS(sps))
	add(hevc.WritePPS(pps))
	for poc := 0; poc < pictures; poc++ {
		sh := hevcSliceHeader(sps, pps, poc == 0, poc)
		add(append(hevc.WriteSliceHeader(sh, sps, pps), 0xAB, 0xCD, 0xEF, 0x80))
	}
	return buf.Bytes()
}

// TestHEVCEncoderHeaders checks the parameter sets and slice headers the
// encoder writes: they parse back to what was built, describe the requested
// picture, and drive the DPB the way the encoder's reference structure
// intends (every P picture references the picture before it).
func TestHEVCEncoderHeaders(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		tools         hevcTools
		codedW        int
		codedH        int
	}{
		{"amd-1080p", 1920, 1080, hevcToolsFor(amdBlockSizes, true, amdFeatures, true, true), 1920, 1080},
		{"amd-odd-size", 322, 242, hevcToolsFor(amdBlockSizes, true, amdFeatures, true, false), 328, 248},
		{"guessed-1080p", 1920, 1080, hevcToolsFor(0, false, 0, false, true), 1920, 1088},
		{"guessed-qvga", 320, 240, hevcToolsFor(0, false, 0, false, false), 320, 240},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vps, sps, pps := hevcParameterSets(tc.width, tc.height, 30000, 1001, 4000, 30, tc.tools)
			if sps.Width != tc.codedW || sps.Height != tc.codedH {
				t.Errorf("coded size %dx%d, want %dx%d", sps.Width, sps.Height, tc.codedW, tc.codedH)
			}
			if x, y, w, h := sps.Crop(); x != 0 || y != 0 || w != tc.width || h != tc.height {
				t.Errorf("conformance window %d,%d %dx%d, want 0,0 %dx%d", x, y, w, h, tc.width, tc.height)
			}
			if pps.InitQpMinus26 != 4 || pps.CuQpDeltaEnabled != tc.tools.cuQpDelta {
				t.Errorf("PPS init_qp_minus26 %d, cu_qp_delta %v", pps.InitQpMinus26, pps.CuQpDeltaEnabled)
			}

			const pictures = 5000 // beyond the wrap of the 12-bit picture order count
			stream := encoderStream(vps, sps, pps, pictures)
			ps := hevc.NewParameterSets()
			dpb := hevc.NewDPB()
			released := 0
			dpb.Release = func(*hevc.Picture) { released++ }
			poc := 0
			for i, nal := range annexb.Split(stream) {
				switch typ := hevc.Type(nal); {
				case typ == hevc.NALSPS:
					got, err := ps.AddSPS(nal)
					if err != nil {
						t.Fatalf("SPS: %v", err)
					}
					if !reflect.DeepEqual(got, sps) {
						t.Errorf("SPS round trip differs:\n got %+v\nwant %+v", *got, *sps)
					}
				case typ == hevc.NALPPS:
					got, err := ps.AddPPS(nal)
					if err != nil {
						t.Fatalf("PPS: %v", err)
					}
					if !reflect.DeepEqual(got, pps) {
						t.Errorf("PPS round trip differs:\n got %+v\nwant %+v", *got, *pps)
					}
				case hevc.IsSlice(typ):
					sh, s, _, err := hevc.ParseSliceHeader(nal, ps, nil)
					if err != nil {
						t.Fatalf("NAL %d: slice header: %v", i, err)
					}
					if sh.DataByteOffset() != len(nal)-4 {
						t.Fatalf("picture %d: slice data at byte %d of a %d byte header", poc, sh.DataByteOffset(), len(nal)-4)
					}
					cur, err := dpb.Start(s, sh, poc)
					if err != nil {
						t.Fatalf("picture %d: %v", poc, err)
					}
					l0, l1 := dpb.RefPicLists(sh)
					refs := dpb.Refs()
					switch {
					case int(cur.POC) != poc:
						t.Fatalf("picture %d: POC %d", poc, cur.POC)
					case poc == 0 && (typ != hevc.NALIDRWRADL || len(refs) != 0 || l0 != nil):
						t.Fatalf("picture 0: type %d with %d references", typ, len(refs))
					case poc > 0 && (typ != hevc.NALTrailR || sh.SliceType != hevc.SliceP || len(refs) != 1 || refs[0].Missing ||
						len(l0) != 1 || int(l0[0].POC) != poc-1 || l1 != nil):
						t.Fatalf("picture %d: type %d, %d references, list %v", poc, typ, len(refs), l0)
					}
					dpb.Finish()
					poc++
				}
			}
			if poc != pictures {
				t.Fatalf("walked %d pictures, wrote %d", poc, pictures)
			}
			// Picture n leaves the DPB when picture n+2 starts, so the last
			// two are still held.
			if released != pictures-2 {
				t.Errorf("%d pictures released while %d were decoded: the stream keeps more than one reference", released, pictures)
			}
		})
	}
}

// TestHEVCEncoderHeadersFFmpeg lets ffmpeg read the encoder's parameter
// sets and slice headers: its bitstream reader must accept every unit and
// see the stream as the profile, level, size and frame rate that were
// asked for.
func TestHEVCEncoderHeadersFFmpeg(t *testing.T) {
	testutil.RequireFFmpeg(t)
	vps, sps, pps := hevcParameterSets(322, 242, 30000, 1001, 1500, 28, hevcToolsFor(amdBlockSizes, true, amdFeatures, true, true))
	stream := encoderStream(vps, sps, pps, 4)
	path := filepath.Join(t.TempDir(), "encoder.hevc")
	if err := os.WriteFile(path, stream, 0o644); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	var poc []int64
	sliceEnd := map[int]int{} // slice index -> last bit of the header
	for _, u := range testutil.TraceHeaders(t, path, "hevc") {
		kinds[u.Kind]++
		if u.Kind != "Slice Segment Header" {
			continue
		}
		end := 0
		for _, f := range u.Fields {
			end = max(end, f.Pos+f.Bits)
			if f.Name == "slice_pic_order_cnt_lsb" {
				poc = append(poc, f.Value)
			}
		}
		sliceEnd[kinds[u.Kind]-1] = end
	}
	if kinds["Video Parameter Set"] != 1 || kinds["Sequence Parameter Set"] != 1 || kinds["Picture Parameter Set"] != 1 || kinds["Slice Segment Header"] != 4 {
		t.Fatalf("ffmpeg read %v", kinds)
	}
	if !reflect.DeepEqual(poc, []int64{1, 2, 3}) {
		t.Errorf("slice_pic_order_cnt_lsb of the P pictures = %v", poc)
	}
	for i := 0; i < 4; i++ {
		hdr := hevc.WriteSliceHeader(hevcSliceHeader(sps, pps, i == 0, i), sps, pps)
		if sliceEnd[i] != 8*len(hdr) {
			t.Errorf("slice %d: ffmpeg's header ends at bit %d, the packed header has %d bits", i, sliceEnd[i], 8*len(hdr))
		}
	}
	if got := testutil.ProbeStreamField(t, path, codec.HEVC, "profile"); got != "Main" {
		t.Errorf("ffprobe profile %q", got)
	}
	if got := testutil.ProbeStreamField(t, path, codec.HEVC, "level"); got != "60" {
		t.Errorf("ffprobe level %q, want 60 (level 2)", got)
	}
	w := testutil.ProbeStreamField(t, path, codec.HEVC, "width")
	h := testutil.ProbeStreamField(t, path, codec.HEVC, "height")
	if w != "322" || h != "242" {
		t.Errorf("ffprobe size %sx%s, want 322x242", w, h)
	}
	if got := testutil.ProbeStreamField(t, path, codec.HEVC, "r_frame_rate"); got != "30000/1001" {
		t.Errorf("ffprobe frame rate %q", got)
	}
}
