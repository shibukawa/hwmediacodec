package adts_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/encoding/adts"
)

// stream writes n access units of growing size through a Writer.
func stream(t *testing.T, format hwmediacodec.AudioFormat, n int) ([]byte, [][]byte) {
	t.Helper()
	var buf bytes.Buffer
	w, err := adts.NewWriter(&buf, format)
	if err != nil {
		t.Fatal(err)
	}
	var units [][]byte
	for i := range n {
		au := bytes.Repeat([]byte{byte(i + 1)}, 10+i*7)
		units = append(units, au)
		if err := w.WritePacket(hwmediacodec.Packet{Data: au}); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes(), units
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestWriterShortWrite(t *testing.T) {
	w, err := adts.NewWriter(shortWriter{}, hwmediacodec.AudioFormat{Codec: hwmediacodec.AAC, SampleRate: 48000, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WritePacket(hwmediacodec.Packet{Data: []byte{1, 2, 3}}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write: %v, want io.ErrShortWrite", err)
	}
}

func TestReaderRejectsFormatChange(t *testing.T) {
	format := hwmediacodec.AudioFormat{Codec: hwmediacodec.AAC, SampleRate: 48000, Channels: 2}
	first, _ := stream(t, format, 1)
	for name, nextFormat := range map[string]hwmediacodec.AudioFormat{
		"rate":     {Codec: hwmediacodec.AAC, SampleRate: 44100, Channels: 2},
		"channels": {Codec: hwmediacodec.AAC, SampleRate: 48000, Channels: 1},
	} {
		t.Run(name, func(t *testing.T) {
			second, _ := stream(t, nextFormat, 1)
			r, err := adts.NewReader(bytes.NewReader(append(first, second...)))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Next(); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Next(); !errors.Is(err, hwmediacodec.ErrInvalidData) {
				t.Fatalf("format change: %v, want ErrInvalidData", err)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	format := hwmediacodec.AudioFormat{Codec: hwmediacodec.AAC, SampleRate: 44100, Channels: 2}
	data, units := stream(t, format, 5)
	// An ID3 tag in front, as on files from a music library.
	tagged := append([]byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 3, 'x', 'y', 'z'}, data...)
	for name, in := range map[string][]byte{"plain": data, "id3": tagged} {
		r, err := adts.NewReader(bytes.NewReader(in))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := r.Format()
		if got.Codec != hwmediacodec.AAC || got.SampleRate != 44100 || got.Channels != 2 || got.FrameSamples != 1024 || !bytes.Equal(got.Config, []byte{0x12, 0x10}) {
			t.Errorf("%s: format %+v", name, got)
		}
		if r.Codec() != hwmediacodec.AAC || r.TimeScale() != 44100 {
			t.Errorf("%s: codec %s, time scale %d", name, r.Codec(), r.TimeScale())
		}
		for i, want := range units {
			p, err := r.ReadPacket()
			if err != nil {
				t.Fatalf("%s: packet %d: %v", name, i, err)
			}
			if !bytes.Equal(p.Data, want) || p.PTS != int64(i)*1024 || p.Duration != 1024 || !p.Keyframe {
				t.Errorf("%s: packet %d: %d bytes at PTS %d", name, i, len(p.Data), p.PTS)
			}
		}
		if _, err := r.ReadPacket(); err != io.EOF {
			t.Errorf("%s: after the last packet: %v, want io.EOF", name, err)
		}
	}
}

func TestReaderErrors(t *testing.T) {
	format := hwmediacodec.AudioFormat{Codec: hwmediacodec.AAC, SampleRate: 48000, Channels: 1}
	data, _ := stream(t, format, 2)
	if _, err := adts.NewReader(bytes.NewReader(nil)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("empty input: %v", err)
	}
	if _, err := adts.NewReader(bytes.NewReader([]byte("RIFF....WAVEfmt "))); !errors.Is(err, hwmediacodec.ErrInvalidData) {
		t.Errorf("not ADTS: %v", err)
	}
	// Cut inside the second access unit, and inside its header.
	for _, cut := range []int{len(data) - 3, 17 + 3} {
		r, err := adts.NewReader(bytes.NewReader(data[:cut]))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Next(); err != nil {
			t.Fatalf("first unit of a stream cut at %d: %v", cut, err)
		}
		if _, err := r.Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("stream cut at %d: %v, want io.ErrUnexpectedEOF", cut, err)
		}
	}
	// Garbage where the second header should be.
	broken := append(append([]byte{}, data[:17]...), 1, 2, 3, 4, 5, 6, 7, 8)
	r, err := adts.NewReader(bytes.NewReader(broken))
	if err != nil {
		t.Fatal(err)
	}
	r.Next()
	if _, err := r.Next(); !errors.Is(err, hwmediacodec.ErrInvalidData) {
		t.Errorf("lost sync: %v, want ErrInvalidData", err)
	}
}

func TestWriterRefuses(t *testing.T) {
	for name, f := range map[string]hwmediacodec.AudioFormat{
		"opus":        {Codec: hwmediacodec.Opus, SampleRate: 48000, Channels: 2},
		"odd rate":    {Codec: hwmediacodec.AAC, SampleRate: 12345, Channels: 2},
		"no channels": {Codec: hwmediacodec.AAC, SampleRate: 48000},
		"broken":      {Codec: hwmediacodec.AAC, Config: []byte{0x11}},
	} {
		if _, err := adts.NewWriter(io.Discard, f); err == nil {
			t.Errorf("%s: NewWriter succeeded", name)
		}
	}
	w, err := adts.NewWriter(io.Discard, hwmediacodec.AudioFormat{Codec: hwmediacodec.AAC, SampleRate: 48000, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WritePacket(hwmediacodec.Packet{Data: make([]byte, 9000)}); err == nil {
		t.Error("a packet too large for an ADTS frame was written")
	}
}
