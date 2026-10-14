// Package adts reads and writes ADTS streams: AAC access units with a
// seven-byte header in front of each, which is the format of .aac files
// and of AAC in MPEG transport streams. A hwmediacodec audio decoder
// consumes the bare access units and takes the stream parameters from
// Reader.Format; Writer puts the headers back in front of an encoder's
// packets.
package adts

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// Reader splits an ADTS stream into AAC access units. It is a
// hwmediacodec.PacketReader whose time scale is the sample rate.
type Reader struct {
	r      *bufio.Reader
	format codec.AudioFormat
	// next counts the access units returned so far.
	next int64
}

// NewReader reads the first frame header of r, which gives the stream
// parameters, and returns a reader positioned at the first access unit.
// An ID3v2 tag in front of the stream is skipped.
func NewReader(r io.Reader) (*Reader, error) {
	br := bufio.NewReaderSize(r, 16<<10)
	if tag, err := br.Peek(10); err == nil && string(tag[:3]) == "ID3" {
		size := int(tag[6]&0x7F)<<21 | int(tag[7]&0x7F)<<14 | int(tag[8]&0x7F)<<7 | int(tag[9]&0x7F)
		if _, err := br.Discard(10 + size); err != nil {
			return nil, fmt.Errorf("adts: skip ID3 tag: %w", err)
		}
	}
	b, err := br.Peek(aac.ADTSHeaderSize)
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("adts: read the first header: %w", err)
	}
	h, err := aac.ParseADTS(b)
	if err != nil {
		return nil, fmt.Errorf("adts: %w: %w", err, codec.ErrInvalidData)
	}
	cfg, err := aac.ParseConfig(h.Config())
	if err != nil {
		return nil, fmt.Errorf("adts: %w: %w", err, codec.ErrInvalidData)
	}
	return &Reader{r: br, format: codec.AudioFormat{
		Codec:        codec.AAC,
		SampleRate:   cfg.SampleRate,
		Channels:     cfg.Channels(),
		Config:       h.Config(),
		FrameSamples: cfg.FrameSamples,
	}}, nil
}

// Format describes the stream: hand it to hwmediacodec.NewAudioDecoder.
// ADTS does not record the encoder's delay, so Delay is 0.
func (r *Reader) Format() codec.AudioFormat {
	f := r.format
	f.Config = append([]byte(nil), f.Config...)
	return f
}

// Codec implements hwmediacodec.PacketReader.
func (r *Reader) Codec() codec.Codec { return codec.AAC }

// TimeScale implements hwmediacodec.PacketReader: the sample rate.
func (r *Reader) TimeScale() int32 { return int32(r.format.SampleRate) }

// Next returns the next access unit without its header, or io.EOF at the
// end of the stream.
func (r *Reader) Next() ([]byte, error) {
	b, err := r.r.Peek(aac.ADTSHeaderSize)
	if len(b) == 0 && err == io.EOF {
		return nil, io.EOF
	}
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("adts: frame %d: %w", r.next, err)
	}
	h, err := aac.ParseADTS(b)
	if err != nil {
		return nil, fmt.Errorf("adts: frame %d: %w: %w", r.next, err, codec.ErrInvalidData)
	}
	if h.SampleRate != r.format.SampleRate || h.ChannelConfig != int(r.format.Config[1]>>3&0xf) ||
		h.ObjectType != int(r.format.Config[0]>>3) {
		return nil, fmt.Errorf("adts: frame %d changes the stream format: %w", r.next, codec.ErrInvalidData)
	}
	if h.Blocks != 1 {
		return nil, fmt.Errorf("adts: frame %d holds %d access units, which is not supported: %w", r.next, h.Blocks, codec.ErrInvalidData)
	}
	if _, err := r.r.Discard(h.HeaderSize); err != nil {
		return nil, fmt.Errorf("adts: frame %d: %w", r.next, err)
	}
	au := make([]byte, h.FrameSize-h.HeaderSize)
	if _, err := io.ReadFull(r.r, au); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("adts: frame %d: %w", r.next, err)
	}
	r.next++
	return au, nil
}

// ReadPacket implements hwmediacodec.PacketReader: Next as a packet whose
// PTS counts samples from the first access unit.
func (r *Reader) ReadPacket() (codec.Packet, error) {
	au, err := r.Next()
	if err != nil {
		return codec.Packet{}, err
	}
	n := int64(r.format.FrameSamples)
	pts := (r.next - 1) * n
	return codec.Packet{Data: au, PTS: pts, DTS: pts, Duration: n, Keyframe: true}, nil
}

// Writer writes AAC packets as an ADTS stream. It is a
// hwmediacodec.PacketWriter.
type Writer struct {
	w   io.Writer
	cfg aac.Config
	buf []byte
}

// NewWriter returns a writer for packets of the stream format describes
// (AudioEncoder.Format of an AAC encoder). ADTS carries AAC-LC with a
// standard sample rate (HE-AAC goes in as the AAC-LC stream it is built
// on, for the decoder to recognise); other streams are refused.
func NewWriter(w io.Writer, format codec.AudioFormat) (*Writer, error) {
	if format.Codec != codec.AAC {
		return nil, fmt.Errorf("adts: cannot carry %s", format.Codec)
	}
	asc := format.Config
	if len(asc) == 0 {
		var err error
		if asc, err = aac.LCConfig(format.SampleRate, format.Channels); err != nil {
			return nil, fmt.Errorf("adts: %w", err)
		}
	}
	cfg, err := aac.ParseConfig(asc)
	if err != nil {
		return nil, fmt.Errorf("adts: %w", err)
	}
	if _, err := aac.AppendADTS(nil, cfg, 0); err != nil {
		return nil, fmt.Errorf("adts: %w", err)
	}
	return &Writer{w: w, cfg: cfg}, nil
}

// WritePacket implements hwmediacodec.PacketWriter: the header, then the
// access unit.
func (w *Writer) WritePacket(p codec.Packet) error {
	buf, err := aac.AppendADTS(w.buf[:0], w.cfg, len(p.Data))
	if err != nil {
		return fmt.Errorf("adts: %w", err)
	}
	w.buf = append(buf, p.Data...)
	n, err := w.w.Write(w.buf)
	if err == nil && n != len(w.buf) {
		return io.ErrShortWrite
	}
	return err
}
