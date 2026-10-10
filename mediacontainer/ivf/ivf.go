// Package ivf reads and writes IVF files, the minimal container for raw AV1
// (and VP8/VP9) streams: a 32-byte file header followed by frames that each
// carry a 12-byte header with the frame size and timestamp. One IVF frame
// holds one AV1 temporal unit, which is what a hwmediacodec AV1 decoder
// consumes as a Packet.
package ivf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// HeaderSize is the size of the file header in bytes.
const HeaderSize = 32

// frameHeaderSize is the size of the header in front of every frame.
const frameHeaderSize = 12

// Header is the IVF file header.
type Header struct {
	// FourCC names the codec: "AV01", "VP90" or "VP80".
	FourCC string
	Width  int
	Height int
	// Frame timestamps are in units of TimebaseNum/TimebaseDen seconds.
	TimebaseNum uint32
	TimebaseDen uint32
	// FrameCount is the number of frames the header declares, or 0 when the
	// writer did not know it.
	FrameCount uint32
}

var signature = [4]byte{'D', 'K', 'I', 'F'}

// Reader splits an IVF file into frames.
type Reader struct {
	r   io.Reader
	hdr Header
	err error
}

// NewReader reads the file header from r and returns a Reader positioned at
// the first frame.
func NewReader(r io.Reader) (*Reader, error) {
	var h [HeaderSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("ivf: read header: %w", err)
	}
	if [4]byte(h[0:4]) != signature {
		return nil, errors.New("ivf: not an IVF file (missing DKIF signature)")
	}
	if v := binary.LittleEndian.Uint16(h[4:6]); v != 0 {
		return nil, fmt.Errorf("ivf: unsupported version %d", v)
	}
	size := int(binary.LittleEndian.Uint16(h[6:8]))
	if size < HeaderSize {
		return nil, fmt.Errorf("ivf: header size %d is smaller than %d", size, HeaderSize)
	}
	rd := &Reader{r: r, hdr: Header{
		FourCC:      string(h[8:12]),
		Width:       int(binary.LittleEndian.Uint16(h[12:14])),
		Height:      int(binary.LittleEndian.Uint16(h[14:16])),
		TimebaseDen: binary.LittleEndian.Uint32(h[16:20]),
		TimebaseNum: binary.LittleEndian.Uint32(h[20:24]),
		FrameCount:  binary.LittleEndian.Uint32(h[24:28]),
	}}
	if size > HeaderSize {
		// A longer header than the standard one: skip what we do not know.
		if _, err := io.CopyN(io.Discard, r, int64(size-HeaderSize)); err != nil {
			return nil, fmt.Errorf("ivf: read header: %w", err)
		}
	}
	return rd, nil
}

// Header returns the file header.
func (r *Reader) Header() Header { return r.hdr }

// Next returns the next frame (one temporal unit for AV1) and its
// timestamp in timebase units. It returns io.EOF when the file is
// exhausted and io.ErrUnexpectedEOF when it ends inside a frame. The
// returned slice is owned by the caller.
func (r *Reader) Next() (frame []byte, pts uint64, err error) {
	if r.err != nil {
		return nil, 0, r.err
	}
	var h [frameHeaderSize]byte
	if _, err := io.ReadFull(r.r, h[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			err = fmt.Errorf("ivf: truncated frame header: %w", err)
		}
		r.err = err
		return nil, 0, err
	}
	size := binary.LittleEndian.Uint32(h[0:4])
	pts = binary.LittleEndian.Uint64(h[4:12])
	frame = make([]byte, size)
	if _, err := io.ReadFull(r.r, frame); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		r.err = fmt.Errorf("ivf: truncated frame of %d bytes: %w", size, err)
		return nil, 0, r.err
	}
	return frame, pts, nil
}

// Writer writes an IVF file.
type Writer struct {
	w      io.Writer
	hdr    Header
	start  int64 // offset of the file header when w can seek, else -1
	wrote  bool
	frames uint32
	err    error
}

// NewWriter returns a Writer that produces an IVF file on w for the codec
// named by fourcc ("AV01", "VP90", "VP80") with the given picture size and
// timebase (frame timestamps are in units of timebaseNum/timebaseDen
// seconds; 1/30 for a 30 fps stream stamped in frames). The file header is
// written with the first frame or by Close. When w is an io.WriteSeeker
// (an *os.File, for example), Close rewrites the header with the final
// frame count; otherwise the header declares 0 frames, which readers treat
// as unknown.
func NewWriter(w io.Writer, fourcc string, width, height int, timebaseNum, timebaseDen uint32) *Writer {
	return &Writer{w: w, hdr: Header{FourCC: fourcc, Width: width, Height: height, TimebaseNum: timebaseNum, TimebaseDen: timebaseDen}, start: -1}
}

func (w *Writer) writeHeader() error {
	if w.wrote {
		return nil
	}
	if len(w.hdr.FourCC) != 4 {
		return fmt.Errorf("ivf: fourcc %q must be four bytes", w.hdr.FourCC)
	}
	if w.hdr.Width < 0 || w.hdr.Width > 0xffff || w.hdr.Height < 0 || w.hdr.Height > 0xffff {
		return fmt.Errorf("ivf: picture size %dx%d does not fit the 16-bit header fields", w.hdr.Width, w.hdr.Height)
	}
	if s, ok := w.w.(io.Seeker); ok {
		if off, err := s.Seek(0, io.SeekCurrent); err == nil {
			w.start = off
		}
	}
	w.wrote = true
	_, err := w.w.Write(w.header())
	return err
}

func (w *Writer) header() []byte {
	var h [HeaderSize]byte
	copy(h[0:4], signature[:])
	binary.LittleEndian.PutUint16(h[6:8], HeaderSize)
	copy(h[8:12], w.hdr.FourCC)
	binary.LittleEndian.PutUint16(h[12:14], uint16(w.hdr.Width))
	binary.LittleEndian.PutUint16(h[14:16], uint16(w.hdr.Height))
	binary.LittleEndian.PutUint32(h[16:20], w.hdr.TimebaseDen)
	binary.LittleEndian.PutUint32(h[20:24], w.hdr.TimebaseNum)
	binary.LittleEndian.PutUint32(h[24:28], w.frames)
	return h[:]
}

// WriteFrame appends one frame (one temporal unit for AV1) with its
// timestamp in timebase units.
func (w *Writer) WriteFrame(data []byte, pts uint64) error {
	if w.err != nil {
		return w.err
	}
	if err := w.writeHeader(); err != nil {
		w.err = err
		return err
	}
	var h [frameHeaderSize]byte
	binary.LittleEndian.PutUint32(h[0:4], uint32(len(data)))
	binary.LittleEndian.PutUint64(h[4:12], pts)
	if _, err := w.w.Write(h[:]); err != nil {
		w.err = err
		return err
	}
	if _, err := w.w.Write(data); err != nil {
		w.err = err
		return err
	}
	w.frames++
	return nil
}

// Close finishes the file: it writes the header if no frame did, and
// patches the frame count into it when the underlying writer can seek. It
// does not close the underlying writer.
func (w *Writer) Close() error {
	if w.err != nil {
		return w.err
	}
	if err := w.writeHeader(); err != nil {
		w.err = err
		return err
	}
	ws, ok := w.w.(io.WriteSeeker)
	if !ok || w.start < 0 {
		return nil
	}
	end, err := ws.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := ws.Seek(w.start, io.SeekStart); err != nil {
		return err
	}
	if _, err := ws.Write(w.header()); err != nil {
		return err
	}
	_, err = ws.Seek(end, io.SeekStart)
	return err
}
