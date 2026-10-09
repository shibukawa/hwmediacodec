package screencast

import (
	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/examples/container"
)

type mp4Sink struct {
	m  *container.Muxer
	vw *container.VideoWriter
}

// NewMP4File returns a Sink that writes the recording to an MP4 file.
func NewMP4File(path string, c hwmediacodec.Codec) (Sink, error) {
	m, err := container.Create(path)
	if err != nil {
		return nil, err
	}
	vw, err := m.AddVideoTrack(c, TimeScale)
	if err != nil {
		m.Close()
		return nil, err
	}
	return &mp4Sink{m: m, vw: vw}, nil
}

func (s *mp4Sink) WritePacket(p hwmediacodec.Packet) error { return s.vw.WritePacket(p) }
func (s *mp4Sink) Close() error                            { return s.m.Close() }

// Funcs adapts two functions to a Sink.
type Funcs struct {
	Write func(p hwmediacodec.Packet) error
	Done  func() error
}

// WritePacket implements Sink.
func (f Funcs) WritePacket(p hwmediacodec.Packet) error { return f.Write(p) }

// Close implements Sink.
func (f Funcs) Close() error {
	if f.Done == nil {
		return nil
	}
	return f.Done()
}
