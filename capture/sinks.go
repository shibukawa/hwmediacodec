package capture

import "github.com/shibukawa/hwmediacodec"

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
