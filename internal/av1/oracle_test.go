package av1_test

import (
	"bytes"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/ivf"
)

// readIVF returns the temporal units of an IVF file.
func readIVF(t *testing.T, path string) [][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := ivf.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var units [][]byte
	for {
		tu, _, err := r.Next()
		if err == io.EOF {
			return units
		}
		if err != nil {
			t.Fatal(err)
		}
		units = append(units, tu)
	}
}

// field returns the value of a syntax element in a trace unit.
func field(t *testing.T, u testutil.TraceUnit, name string) int64 {
	t.Helper()
	for _, f := range u.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	t.Fatalf("%s: no field %s", u.Kind, name)
	return 0
}

func optField(u testutil.TraceUnit, name string, absent int64) int64 {
	for _, f := range u.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return absent
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// TestHeadersMatchFFmpegTrace checks the OBU framing, every sequence
// header field and the leading frame header fields of SVT-AV1 streams
// (8-bit and 10-bit) against ffmpeg's trace_headers bitstream filter, and
// that the generated streams have the structure the decoder tests rely on:
// hidden frames and show_existing_frame units.
func TestHeadersMatchFFmpegTrace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []string
	}{{"8bit", nil}, {"10bit", []string{"-pix_fmt", "yuv420p10le"}}} {
		t.Run(tc.name, func(t *testing.T) {
			path := testutil.GenerateAV1(t, 320, 240, 30, tc.extra...)
			units := testutil.TraceHeaders(t, path, "ivf")
			var headers, seqs, frames []testutil.TraceUnit
			for _, u := range units {
				switch u.Kind {
				case "OBU header":
					headers = append(headers, u)
				case "Sequence Header":
					seqs = append(seqs, u)
				case "Frame Header", "Redundant Frame Header":
					frames = append(frames, u)
				}
			}

			var obus []av1.OBU
			var seqPayloads [][]byte
			var frameOBUs []av1.OBU
			var seq *av1.SequenceHeader
			hidden, existing := 0, 0
			for _, data := range readIVF(t, path) {
				tu, err := av1.ParseTemporalUnit(data, seq)
				if err != nil {
					t.Fatal(err)
				}
				if tu.Sequence != nil {
					seq = tu.Sequence
					seqPayloads = append(seqPayloads, tu.SequenceHeader.Payload)
				}
				for _, o := range tu.OBUs {
					obus = append(obus, o)
					if o.Type == av1.OBUFrame || o.Type == av1.OBUFrameHeader || o.Type == av1.OBURedundantFrameHeader {
						frameOBUs = append(frameOBUs, o)
						fh, err := av1.ParseFrameHeader(o.Payload, seq.ReducedStillPictureHeader)
						if err != nil {
							t.Fatal(err)
						}
						if fh.ShowExistingFrame {
							existing++
						} else if !fh.ShowFrame {
							hidden++
						}
					}
				}
			}
			if hidden == 0 || existing == 0 {
				t.Fatalf("the stream has %d hidden frames and %d show_existing_frame headers; the decoder tests need both", hidden, existing)
			}

			if len(headers) != len(obus) {
				t.Fatalf("ffmpeg saw %d OBUs, Split found %d", len(headers), len(obus))
			}
			for i, o := range obus {
				h := headers[i]
				if field(t, h, "obu_type") != int64(o.Type) || field(t, h, "obu_extension_flag") != b2i(o.HasExtension) {
					t.Fatalf("OBU %d: type %s ext %v; ffmpeg: %v", i, o.Type, o.HasExtension, h.Fields)
				}
				if field(t, h, "obu_has_size_field") == 1 && field(t, h, "obu_size") != int64(len(o.Payload)) {
					t.Fatalf("OBU %d (%s): payload %d bytes, ffmpeg obu_size %d", i, o.Type, len(o.Payload), field(t, h, "obu_size"))
				}
			}

			if len(seqs) != len(seqPayloads) {
				t.Fatalf("ffmpeg saw %d sequence headers, found %d", len(seqs), len(seqPayloads))
			}
			for i, payload := range seqPayloads {
				sh, err := av1.ParseSequenceHeader(payload)
				if err != nil {
					t.Fatal(err)
				}
				u := seqs[i]
				checks := map[string]int64{
					"seq_profile":                        int64(sh.Profile),
					"still_picture":                      b2i(sh.StillPicture),
					"reduced_still_picture_header":       b2i(sh.ReducedStillPictureHeader),
					"timing_info_present_flag":           b2i(sh.TimingInfoPresent),
					"initial_display_delay_present_flag": b2i(sh.InitialDisplayDelayPresent),
					"operating_points_cnt_minus_1":       int64(sh.OperatingPoints - 1),
					"operating_point_idc[0]":             int64(sh.OperatingPointIdc),
					"seq_level_idx[0]":                   int64(sh.LevelIdx),
					"frame_width_bits_minus_1":           int64(sh.FrameWidthBits - 1),
					"frame_height_bits_minus_1":          int64(sh.FrameHeightBits - 1),
					"max_frame_width_minus_1":            int64(sh.MaxFrameWidth - 1),
					"max_frame_height_minus_1":           int64(sh.MaxFrameHeight - 1),
					"frame_id_numbers_present_flag":      b2i(sh.FrameIDNumbersPresent),
					"use_128x128_superblock":             b2i(sh.Use128x128Superblock),
					"enable_filter_intra":                b2i(sh.EnableFilterIntra),
					"enable_intra_edge_filter":           b2i(sh.EnableIntraEdgeFilter),
					"enable_interintra_compound":         b2i(sh.EnableInterintraCompound),
					"enable_masked_compound":             b2i(sh.EnableMaskedCompound),
					"enable_warped_motion":               b2i(sh.EnableWarpedMotion),
					"enable_dual_filter":                 b2i(sh.EnableDualFilter),
					"enable_order_hint":                  b2i(sh.EnableOrderHint),
					"enable_superres":                    b2i(sh.EnableSuperres),
					"enable_cdef":                        b2i(sh.EnableCDEF),
					"enable_restoration":                 b2i(sh.EnableRestoration),
					"high_bitdepth":                      b2i(sh.BitDepth >= 10),
					"mono_chrome":                        b2i(sh.MonoChrome),
					"color_description_present_flag":     b2i(sh.ColorDescriptionPresent),
					"color_range":                        b2i(sh.FullRange),
					"separate_uv_delta_q":                b2i(sh.SeparateUVDeltaQ),
					"film_grain_params_present":          b2i(sh.FilmGrainParamsPresent),
				}
				for name, want := range checks {
					if got := field(t, u, name); got != want {
						t.Errorf("sequence header %d: %s = %d, ffmpeg says %d", i, name, want, got)
					}
				}
				optional := map[string][2]int64{
					"seq_tier[0]":                      {int64(sh.Tier), 0},
					"initial_display_delay_minus_1[0]": {int64(sh.InitialDisplayDelay - 1), -1},
					"enable_jnt_comp":                  {b2i(sh.EnableJntComp), 0},
					"enable_ref_frame_mvs":             {b2i(sh.EnableRefFrameMVs), 0},
					"order_hint_bits_minus_1":          {int64(sh.OrderHintBits) - 1, -1},
					"chroma_sample_position":           {int64(sh.ChromaSamplePosition), 0},
					"seq_choose_screen_content_tools":  {b2i(sh.SeqForceScreenContentTools == av1.SelectScreenContentTools), 0},
					"seq_choose_integer_mv":            {b2i(sh.SeqForceIntegerMV == av1.SelectIntegerMV), 0},
				}
				for name, v := range optional {
					if got := optField(u, name, v[1]); got != v[0] {
						t.Errorf("sequence header %d: %s = %d, ffmpeg says %d", i, name, v[0], got)
					}
				}
			}

			if len(frames) != len(frameOBUs) {
				t.Fatalf("ffmpeg saw %d frame headers, found %d", len(frames), len(frameOBUs))
			}
			for i, o := range frameOBUs {
				fh, err := av1.ParseFrameHeader(o.Payload, false)
				if err != nil {
					t.Fatal(err)
				}
				u := frames[i]
				if field(t, u, "show_existing_frame") != b2i(fh.ShowExistingFrame) {
					t.Fatalf("frame %d: show_existing_frame %v, ffmpeg disagrees", i, fh.ShowExistingFrame)
				}
				if fh.ShowExistingFrame {
					if field(t, u, "frame_to_show_map_idx") != int64(fh.FrameToShowMapIdx) {
						t.Fatalf("frame %d: frame_to_show_map_idx %d, ffmpeg says %d", i, fh.FrameToShowMapIdx, field(t, u, "frame_to_show_map_idx"))
					}
					continue
				}
				if field(t, u, "frame_type") != int64(fh.FrameType) || field(t, u, "show_frame") != b2i(fh.ShowFrame) {
					t.Fatalf("frame %d: type %s shown %v; ffmpeg: %v", i, fh.FrameType, fh.ShowFrame, u.Fields)
				}
			}
			t.Logf("%d OBUs, %d sequence headers, %d frame headers (%d hidden, %d show_existing_frame) agree with ffmpeg", len(obus), len(seqs), len(frames), hidden, existing)
		})
	}
}

// TestCodecConfigurationRecordMatchesFFmpeg compares the av1C record built
// from a stream's sequence header with the one ffmpeg writes into the MP4
// sample description when it remuxes the same stream.
func TestCodecConfigurationRecordMatchesFFmpeg(t *testing.T) {
	ffmpeg := testutil.RequireFFmpeg(t)
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not found in PATH")
	}
	for _, tc := range []struct {
		name  string
		extra []string
	}{{"8bit", nil}, {"10bit", []string{"-pix_fmt", "yuv420p10le"}}} {
		t.Run(tc.name, func(t *testing.T) {
			path := testutil.GenerateAV1(t, 320, 240, 10, tc.extra...)
			units := readIVF(t, path)
			tu, err := av1.ParseTemporalUnit(units[0], nil)
			if err != nil {
				t.Fatal(err)
			}
			if tu.SequenceHeader == nil {
				t.Fatal("the first temporal unit has no sequence header")
			}
			got := av1.CodecConfigurationRecord(tu.Sequence, tu.SequenceHeader.Payload)

			mp4 := filepath.Join(t.TempDir(), "stream.mp4")
			if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", path, "-c", "copy", mp4).CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg remux: %v\n%s", err, out)
			}
			out, err := exec.Command(ffprobe, "-hide_banner", "-loglevel", "error", "-select_streams", "v:0", "-show_streams", "-show_data", mp4).Output()
			if err != nil {
				t.Fatalf("ffprobe: %v", err)
			}
			want := parseExtradata(t, string(out))
			if !bytes.Equal(got, want) {
				t.Fatalf("av1C\n% x\nffmpeg wrote\n% x", got, want)
			}
		})
	}
}

// parseExtradata extracts the hexdump ffprobe prints after "extradata=".
func parseExtradata(t *testing.T, probe string) []byte {
	t.Helper()
	var hexText strings.Builder
	in := false
	for _, line := range strings.Split(probe, "\n") {
		switch {
		case strings.HasPrefix(line, "extradata="):
			in = true
		case in && strings.HasPrefix(line, "extradata_size="):
			in = false
		case in:
			// "00000000: 8100 0c00 0a0b 0200 0005 21e7 fde2 57c8  ....."
			_, rest, ok := strings.Cut(line, ": ")
			if !ok {
				continue
			}
			if i := strings.Index(rest, "  "); i >= 0 {
				rest = rest[:i]
			}
			hexText.WriteString(strings.ReplaceAll(rest, " ", ""))
		}
	}
	b, err := hex.DecodeString(hexText.String())
	if err != nil || len(b) == 0 {
		t.Fatalf("no extradata in ffprobe output (%v):\n%s", err, probe)
	}
	return b
}
