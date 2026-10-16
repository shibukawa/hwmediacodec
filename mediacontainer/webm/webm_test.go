package webm_test

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/mediatest"
	"github.com/shibukawa/hwmediacodec/mediacontainer/ivf"
	"github.com/shibukawa/hwmediacodec/mediacontainer/webm"
)

// These tests need ffmpeg (with libsvtav1 and libopus) but no hardware:
// they check the container layer against ffprobe's view of the same
// files. The ones that run the codecs are in codec_test.go.

// generate writes a WebM file with ffmpeg: a test pattern in AV1 and,
// with audio, a tone in Opus. args are further output options.
func generate(t *testing.T, seconds float64, audio bool, args ...string) string {
	t.Helper()
	mediatest.RequireFFmpeg(t)
	for _, enc := range []string{"libsvtav1", "libopus"} {
		if !mediatest.HasEncoder(t, enc) {
			t.Skipf("ffmpeg has no %s encoder", enc)
		}
	}
	path := filepath.Join(t.TempDir(), "in.webm")
	cmd := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30"}
	if audio {
		cmd = append(cmd, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-c:a", "libopus", "-b:a", "64k")
	}
	cmd = append(cmd, "-t", fmt.Sprint(seconds), "-c:v", "libsvtav1", "-preset", "10", "-g", "25",
		"-svtav1-params", "film-grain=0")
	cmd = append(cmd, args...)
	cmd = append(cmd, path)
	if out, err := exec.Command("ffmpeg", cmd...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	return path
}

// probePacket is a packet as ffprobe sees it, in its stream's time base.
type probePacket struct {
	PTS      int64
	Duration int64
	Size     int
	Key      bool
	Hash     string
	Discard  int
}

func probePackets(t *testing.T, path, stream string) []probePacket {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", stream, "-show_packets",
		"-show_data_hash", "MD5", "-show_entries", "packet=pts,duration,size,flags,data_hash:packet_side_data=discard_padding",
		"-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe packets %s: %v", path, err)
	}
	var rep struct {
		Packets []struct {
			PTS      int64  `json:"pts"`
			Duration int64  `json:"duration"`
			Size     string `json:"size"`
			Flags    string `json:"flags"`
			Hash     string `json:"data_hash"`
			Side     []struct {
				Discard int `json:"discard_padding"`
			} `json:"side_data_list"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatal(err)
	}
	var ps []probePacket
	for _, p := range rep.Packets {
		size, _ := strconv.Atoi(p.Size)
		pp := probePacket{PTS: p.PTS, Duration: p.Duration, Size: size, Key: len(p.Flags) > 0 && p.Flags[0] == 'K', Hash: p.Hash}
		for _, s := range p.Side {
			pp.Discard += s.Discard
		}
		ps = append(ps, pp)
	}
	return ps
}

func md5Hash(b []byte) string {
	h := md5.Sum(b)
	return "MD5:" + hex.EncodeToString(h[:])
}

func readAll(t *testing.T, src hwmediacodec.PacketReader) []hwmediacodec.Packet {
	t.Helper()
	var out []hwmediacodec.Packet
	for {
		p, err := src.ReadPacket()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
}

func TestDemuxMatchesFFprobe(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"default", nil},
		// A live stream: unknown sizes, no Cues, no duration.
		{"live", []string{"-live", "1"}},
		// Many small Clusters.
		{"clusters", []string{"-cluster_time_limit", "100"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := generate(t, 2, true, c.args...)
			d, err := webm.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			checkAgainstProbe(t, d, path, true)
		})
	}
}

// checkAgainstProbe compares every packet of d with ffprobe's view of
// the file at path.
func checkAgainstProbe(t *testing.T, d *webm.Demuxer, path string, audio bool) {
	t.Helper()
	v := d.Video()
	if v == nil {
		t.Fatal("no video track")
	}
	if v.Codec != hwmediacodec.AV1 || v.Width != 320 || v.Height != 240 {
		t.Fatalf("video is %s %dx%d, want AV1 320x240", v.Codec, v.Width, v.Height)
	}
	src := v.PacketSource()
	if src.TimeScale() != 1000 {
		t.Fatalf("video time scale %d, want 1000", src.TimeScale())
	}
	ref := probePackets(t, path, "v:0")
	got := readAll(t, src)
	if len(got) != len(ref) {
		t.Fatalf("%d video packets, ffprobe sees %d", len(got), len(ref))
	}
	for i, p := range got {
		r := ref[i]
		if p.PTS != r.PTS || p.Keyframe != r.Key {
			t.Fatalf("video packet %d: pts %d key %v, ffprobe pts %d key %v", i, p.PTS, p.Keyframe, r.PTS, r.Key)
		}
		// A keyframe may have gained the sequence header of the track.
		if !p.Keyframe && md5Hash(p.Data) != r.Hash {
			t.Fatalf("video packet %d differs from ffprobe's", i)
		}
	}
	if !audio {
		return
	}
	a := d.Audio()
	if a == nil {
		t.Fatal("no audio track")
	}
	f := a.Format()
	if f.Codec != hwmediacodec.Opus || f.SampleRate != 48000 || f.Channels != 1 || f.Delay != 312 {
		t.Fatalf("audio format %+v, want mono Opus with 312 samples of delay", f)
	}
	aref := probePackets(t, path, "a:0")
	agot := readAll(t, a.PacketSource())
	if len(agot) != len(aref) {
		t.Fatalf("%d audio packets, ffprobe sees %d", len(agot), len(aref))
	}
	for i, p := range agot {
		r := aref[i]
		// ffprobe rounds the delay to milliseconds; we count samples.
		if math.Abs(float64(p.PTS)/48-float64(r.PTS)) > 1 || md5Hash(p.Data) != r.Hash {
			t.Fatalf("audio packet %d: pts %d samples, ffprobe %d ms", i, p.PTS, r.PTS)
		}
		if want := int64(960 - r.Discard); p.Duration != want {
			t.Fatalf("audio packet %d lasts %d samples, want %d", i, p.Duration, want)
		}
	}
	if agot[0].PTS != -312 {
		t.Errorf("first audio packet at %d, want -312 (the codec delay)", agot[0].PTS)
	}
	if l := d.Duration(); l < 1900*time.Millisecond || l > 2100*time.Millisecond {
		t.Errorf("duration %v, want about 2s", l)
	}
}

// TestDemuxTruncated opens a file cut off in the middle, as a recording
// that was interrupted leaves it, and reads the frames that are whole.
func TestDemuxTruncated(t *testing.T) {
	path := generate(t, 2, true, "-live", "1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	full, err := webm.NewDemuxer(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	cut := data[:len(data)*2/3]
	d, err := webm.NewDemuxer(bytes.NewReader(cut))
	if err != nil {
		t.Fatal(err)
	}
	n := d.Video().FrameCount()
	if n == 0 || n >= full.Video().FrameCount() {
		t.Fatalf("%d of %d frames in the cut file", n, full.Video().FrameCount())
	}
	for _, src := range []hwmediacodec.PacketReader{d.Video().PacketSource(), d.Audio().PacketSource()} {
		readAll(t, src) // every indexed frame lies in the file
	}
}

func TestSeek(t *testing.T) {
	path := generate(t, 3, true)
	d, err := webm.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	src := d.Video().PacketSource()
	for _, c := range []struct{ at, want time.Duration }{
		{0, 0},
		{500 * time.Millisecond, 0},
		{900 * time.Millisecond, 833 * time.Millisecond}, // keyframes every 25 frames at 30 fps
		{2 * time.Second, 1667 * time.Millisecond},
		{time.Hour, 2500 * time.Millisecond},
	} {
		got, err := src.SeekKeyframe(c.at)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("SeekKeyframe(%v) = %v, want %v", c.at, got, c.want)
		}
		p, err := src.ReadPacket()
		if err != nil || !p.Keyframe {
			t.Fatalf("after seeking to %v: keyframe %v, %v", c.at, p.Keyframe, err)
		}
	}
	asrc := d.Audio().PacketSource()
	got, err := asrc.SeekKeyframe(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got > 920*time.Millisecond || got < 880*time.Millisecond {
		t.Errorf("audio seek to 1s lands at %v, want 80 ms of pre-roll before it", got)
	}
}

// TestRemux copies an ffmpeg file through the Muxer: ffprobe must see the
// same packets, and ffmpeg must decode the copy without complaint.
func TestRemux(t *testing.T) {
	path := generate(t, 2, true)
	d, err := webm.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	out := filepath.Join(t.TempDir(), "out.webm")
	m, err := webm.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	vw, err := m.AddVideoTrack(hwmediacodec.AV1, 1000)
	if err != nil {
		t.Fatal(err)
	}
	aw, err := m.AddAudioTrack(d.Audio().Format(), 48000)
	if err != nil {
		t.Fatal(err)
	}
	// Video packets in milliseconds, audio packets in samples: written
	// one after the other, the Muxer interleaves them.
	// The packets as read are what an encoder gives: on the
	// presentation's time line, the first audio packet before 0.
	audio := readAll(t, d.Audio().PacketSource())
	video := readAll(t, d.Video().PacketSource())
	for len(video) > 0 || len(audio) > 0 {
		if len(audio) == 0 || (len(video) > 0 && video[0].PTS*48 <= audio[0].PTS) {
			if err := vw.WritePacket(video[0]); err != nil {
				t.Fatal(err)
			}
			video = video[1:]
			continue
		}
		if err := aw.WritePacket(audio[0]); err != nil {
			t.Fatal(err)
		}
		audio = audio[1:]
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	mediatest.CheckDecodes(t, out)

	for _, stream := range []string{"v:0", "a:0"} {
		want, got := probePackets(t, path, stream), probePackets(t, out, stream)
		if len(got) != len(want) {
			t.Fatalf("%s: %d packets, want %d", stream, len(got), len(want))
		}
		for i := range got {
			g, w := got[i], want[i]
			if g.PTS != w.PTS || g.Key != w.Key || g.Discard != w.Discard {
				t.Fatalf("%s packet %d: %+v, want %+v", stream, i, g, w)
			}
			if stream == "a:0" && g.Hash != w.Hash {
				t.Fatalf("audio packet %d differs", i)
			}
		}
	}
	if !slices.Equal(mediatest.FrameMD5(t, out, ""), mediatest.FrameMD5(t, path, "")) {
		t.Error("the copy decodes to other pictures")
	}
	ws, gs := mediatest.AudioStream(t, path), mediatest.AudioStream(t, out)
	if ws.Start() != gs.Start() {
		t.Errorf("audio starts at %v, want %v", gs.Start(), ws.Start())
	}
	// The copy has Cues and a duration, and opens in this package too.
	d2, err := webm.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if l := d2.Duration(); l < 1900*time.Millisecond || l > 2100*time.Millisecond {
		t.Errorf("duration %v, want about 2s", l)
	}
	checkAgainstProbe(t, d2, out, true)
}

// TestFromIVF writes the temporal units of an IVF file (with temporal
// delimiters, as encoders produce them) into WebM: ffmpeg decodes both to
// the same pictures.
func TestFromIVF(t *testing.T) {
	mediatest.RequireFFmpeg(t)
	if !mediatest.HasEncoder(t, "libsvtav1") {
		t.Skip("ffmpeg has no libsvtav1 encoder")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "in.ivf")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=256x144:rate=25", "-frames:v", "60",
		"-c:v", "libsvtav1", "-preset", "10", "-g", "20", src).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := ivf.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	h := r.Header()
	out := filepath.Join(dir, "out.webm")
	m, err := webm.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	vw, err := m.AddVideoTrack(hwmediacodec.AV1, h.TimebaseDen/h.TimebaseNum)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		data, pts, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := vw.WritePacket(hwmediacodec.Packet{Data: data, PTS: int64(pts), DTS: int64(pts), Keyframe: i%20 == 0}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	mediatest.CheckDecodes(t, out)
	if got, want := mediatest.FrameMD5(t, out, ""), mediatest.FrameMD5(t, src, "ivf"); !slices.Equal(got, want) {
		t.Fatalf("WebM decodes to %d frames that differ from the %d of the IVF file", len(got), len(want))
	}
	s := mediatest.VideoStream(t, out)
	if s.Width != 256 || s.Height != 144 {
		t.Errorf("ffprobe sees %dx%d", s.Width, s.Height)
	}
}

func TestWriterErrors(t *testing.T) {
	var buf memFile
	m, err := webm.NewMuxer(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddVideoTrack(hwmediacodec.H264, 1000); err == nil {
		t.Error("an H.264 track was accepted")
	}
	if _, err := m.AddAudioTrack(hwmediacodec.AudioFormat{Codec: hwmediacodec.AAC, SampleRate: 48000, Channels: 2}, 48000); err == nil {
		t.Error("an AAC track was accepted")
	}
	vw, err := m.AddVideoTrack(hwmediacodec.AV1, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// A temporal delimiter alone: no sequence header to configure from.
	if err := vw.WritePacket(hwmediacodec.Packet{Data: []byte{0x12, 0x00}, Keyframe: true}); err == nil {
		t.Error("a packet without a sequence header was accepted first")
	}
}

// memFile is a file in memory: the io.WriteSeeker a Muxer writes to.
type memFile struct {
	data []byte
	pos  int
}

func (f *memFile) Write(p []byte) (int, error) {
	if end := f.pos + len(p); end > len(f.data) {
		f.data = append(f.data, make([]byte, end-len(f.data))...)
	}
	copy(f.data[f.pos:], p)
	f.pos += len(p)
	return len(p), nil
}

func (f *memFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		offset += int64(f.pos)
	case io.SeekEnd:
		offset += int64(len(f.data))
	}
	f.pos = int(offset)
	return offset, nil
}
