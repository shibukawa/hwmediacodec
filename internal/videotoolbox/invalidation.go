//go:build darwin

package videotoolbox

import (
	"errors"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/videotoolbox/sys"
)

// errSessionInvalid is returned by decodePacket when VideoToolbox reports
// kVTInvalidSessionErr: the session was torn down behind the decoder's
// back. iOS does this when the application leaves the foreground and loses
// its access to the hardware codecs; macOS never does. The decoder and the
// encoder recover by opening a new session, as Apple recommends.
var errSessionInvalid = errors.New("videotoolbox: session invalidated")

// isInvalidSession reports whether err is a backend error carrying
// kVTInvalidSessionErr (an output callback reports it that way).
func isInvalidSession(err error) bool {
	var be *codec.BackendError
	return errors.As(err, &be) && be.Status == sys.StatusVTInvalidSession
}

// recoverSession replaces an invalidated decompression session with a new
// one for the same format description. The frames VideoToolbox still held
// are lost, so decoding resumes at a keyframe: when the packet that hit
// the error is one, retry decodes it on the new session; otherwise the
// packet is dropped and the decoder waits for the next keyframe, as it
// does after Flush.
func (d *decoder) recoverSession(keyframe bool, retry func() error) error {
	d.destroySession()
	d.outMu.Lock()
	d.inflight = map[uintptr]inflightFrame{}
	d.cbErr = nil
	d.outMu.Unlock()
	// recreateSession takes over one reference to the format description
	// and releases the decoder's old one, which here is the same object.
	sys.CFRetain(d.formatDesc)
	if err := d.recreateSession(d.formatDesc); err != nil {
		return err
	}
	if !keyframe {
		return nil
	}
	d.waitKeyframe = false
	err := retry()
	if err == errSessionInvalid {
		return &codec.BackendError{Backend: Name, Op: "VTDecompressionSessionDecodeFrame", Status: sys.StatusVTInvalidSession, Message: "the session was invalidated again right after it was recreated"}
	}
	return err
}

// reopenSession replaces an invalidated compression session with a new one
// configured like the old. Frames the old session had not emitted are
// lost; the next frame is forced to be a keyframe so that the stream stays
// decodable from there.
func (e *encoder) reopenSession() error {
	if e.session != 0 {
		sessionMu.Lock()
		sys.VTCompressionSessionInvalidate(e.session)
		sessionMu.Unlock()
		sys.Release(e.session)
		e.session = 0
	}
	sys.Release(e.keyProps)
	e.keyProps = 0
	e.outMu.Lock()
	e.cbErr = nil
	e.outMu.Unlock()
	if err := e.createSession(); err != nil {
		return err
	}
	e.forceNext = true
	return nil
}
