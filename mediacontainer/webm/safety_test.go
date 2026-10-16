package webm

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec/internal/opus"
)

// buildFile assembles a WebM file with one Opus track (number 1, 20 ms
// default duration) and the given Cluster payloads.
func buildFile(clusters ...[]byte) []byte {
	var ebml []byte
	ebml = appendString(ebml, idDocType, "webm")
	b := appendElement(nil, idEBML, ebml)
	var seg []byte
	seg = appendElement(seg, idInfo, appendUint(nil, idTimestScale, tickNs))
	entry := appendUint(nil, idTrackNumber, 1)
	entry = appendUint(entry, idTrackType, trackAudio)
	entry = appendString(entry, idCodecID, "A_OPUS")
	entry = appendElement(entry, idCodecPrivate, opus.Head{Channels: 2, PreSkip: 312, InputSampleRate: 48000}.Marshal())
	entry = appendUint(entry, idDefaultDuration, uint64(20*time.Millisecond))
	seg = appendElement(seg, idTracks, appendElement(nil, idTrackEntry, entry))
	for _, c := range clusters {
		seg = appendElement(seg, idCluster, c)
	}
	b = appendID(b, idSegment)
	b = append(b, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
	return append(b, seg...)
}

// opusPacket is a 20 ms CELT packet of n bytes whose payload bytes are v.
func opusPacket(n int, v byte) []byte {
	p := bytes.Repeat([]byte{v}, n)
	p[0] = 0xFC
	return p
}

func simpleBlock(ts int16, flags byte, lace []byte, frames ...[]byte) []byte {
	b := []byte{0x81, byte(uint16(ts) >> 8), byte(ts), flags}
	b = append(b, lace...)
	for _, f := range frames {
		b = append(b, f...)
	}
	return appendElement(nil, idSimpleBlock, b)
}

func TestLacing(t *testing.T) {
	a, b, c := opusPacket(300, 1), opusPacket(10, 2), opusPacket(70, 3)
	var cluster []byte
	cluster = appendUint(cluster, idTimestamp, 1000)
	// Xiph: sizes 300 = 255+45 and 10, the last implied.
	cluster = append(cluster, simpleBlock(0, 0x82, []byte{2, 255, 45, 10}, a, b, c)...)
	// EBML: 300, then 10 as a signed difference of -290, the last implied.
	ebml := appendSizeLen([]byte{2}, 300, 2)
	ebml = append(ebml, appendSizeLen(nil, uint64(-290+(1<<13-1)), 2)...)
	cluster = append(cluster, simpleBlock(60, 0x84, ebml, a, b, c)...)
	// Fixed: three frames of 70 bytes.
	cluster = append(cluster, simpleBlock(120, 0x86, []byte{2}, c, c, c)...)
	d, err := NewDemuxer(bytes.NewReader(buildFile(cluster)))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{a, b, c, a, b, c, c, c, c}
	src := d.Audio().PacketSource()
	for i, w := range want {
		p, err := src.ReadPacket()
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		if !bytes.Equal(p.Data, w) {
			t.Fatalf("packet %d has %d bytes, want %d", i, len(p.Data), len(w))
		}
		// 20 ms apart from the cluster's 1 s, less the 312-sample delay.
		if pts := int64(48000 + 960*i - 312); p.PTS != pts {
			t.Errorf("packet %d at %d, want %d", i, p.PTS, pts)
		}
	}
	if _, err := src.ReadPacket(); err != io.EOF {
		t.Fatalf("after the last packet: %v", err)
	}
}

func TestHostileFiles(t *testing.T) {
	var cluster []byte
	cluster = appendUint(cluster, idTimestamp, 0)
	for _, c := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"not EBML", []byte("RIFF....WAVE")},
		{"Xiph laces past the block", buildFile(append(cluster, simpleBlock(0, 0x82, []byte{1, 255, 255, 10}, opusPacket(20, 1))...))},
		{"fixed laces that do not divide", buildFile(append(cluster, simpleBlock(0, 0x86, []byte{2}, opusPacket(20, 1))...))},
		{"truncated lace header", buildFile(append(cluster, simpleBlock(0, 0x82, nil)...))},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewDemuxer(bytes.NewReader(c.data)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestMaxFrames(t *testing.T) {
	var cluster []byte
	cluster = appendUint(cluster, idTimestamp, 0)
	for i := range 10 {
		cluster = append(cluster, simpleBlock(int16(i*20), 0x80, nil, opusPacket(5, 1))...)
	}
	old := MaxFrames
	defer func() { MaxFrames = old }()
	MaxFrames = 5
	if _, err := NewDemuxer(bytes.NewReader(buildFile(cluster))); err == nil {
		t.Fatal("10 frames accepted with MaxFrames 5")
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error)    { return len(p) / 2, nil }
func (shortWriter) Seek(int64, int) (int64, error) { return 0, nil }

func TestMuxerRejectsShortWrites(t *testing.T) {
	if _, err := NewMuxer(shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("NewMuxer = %v", err)
	}
}

func FuzzDemuxer(f *testing.F) {
	var cluster []byte
	cluster = appendUint(cluster, idTimestamp, 0)
	cluster = append(cluster, simpleBlock(0, 0x82, []byte{1, 20}, opusPacket(20, 1), opusPacket(30, 2))...)
	f.Add(buildFile(cluster))
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := NewDemuxer(bytes.NewReader(data))
		if err != nil {
			return
		}
		if a := d.Audio(); a != nil {
			src := a.PacketSource()
			for range a.FrameCount() {
				if _, err := src.ReadPacket(); err != nil {
					t.Fatalf("indexed frame unreadable: %v", err)
				}
			}
			src.SeekKeyframe(time.Second)
		}
		d.Duration()
	})
}
