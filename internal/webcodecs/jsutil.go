//go:build js && wasm

package webcodecs

import (
	"context"
	"errors"
	"fmt"
	"syscall/js"
	"time"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

const (
	// stallTimeout bounds every wait for the browser: a promise that does
	// not settle and a codec queue that does not move for this long are
	// reported as an error instead of blocking for good.
	stallTimeout = 30 * time.Second
	// maxPending bounds the decoded frames and encoded packets held for
	// Receive; Send reports ErrAgain beyond it.
	maxPending = 16
)

var (
	global     = js.Global()
	uint8Array = global.Get("Uint8Array")
)

// class returns a global constructor such as VideoDecoder. ok is false
// where the environment lacks it: outside a browser (Node.js), in a
// browser without WebCodecs, and on a page that is not a secure context
// (WebCodecs needs https or localhost).
func class(name string) (js.Value, bool) {
	v := global.Get(name)
	return v, v.Type() == js.TypeFunction
}

const noWebCodecs = "WebCodecs is not available here (it needs a browser that implements it and a secure context: https or localhost)"

func unsupported(c codec.Codec, dir codec.Direction, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: dir, Reason: reason}
}

// errorText renders a JavaScript error value as "Name: message".
func errorText(v js.Value) string {
	if v.Type() != js.TypeObject {
		return v.String()
	}
	name, msg := v.Get("name"), v.Get("message")
	switch {
	case name.Type() == js.TypeString && msg.Type() == js.TypeString:
		return name.String() + ": " + msg.String()
	case msg.Type() == js.TypeString:
		return msg.String()
	}
	return v.Call("toString").String()
}

// errorName returns the name of a JavaScript error ("EncodingError",
// "NotSupportedError", ...), or "".
func errorName(v js.Value) string {
	if v.Type() == js.TypeObject {
		if name := v.Get("name"); name.Type() == js.TypeString {
			return name.String()
		}
	}
	return ""
}

// jsError wraps a JavaScript error value. The browser gives no status
// code, so the text travels in the message.
func jsError(op string, v js.Value) error {
	return &codec.BackendError{Backend: Name, Op: op, Message: errorText(v)}
}

func backendErr(op, message string) error {
	return &codec.BackendError{Backend: Name, Op: op, Message: message}
}

// stallError reports a wait that hit its limit (stallTimeout unless set).
type stallError struct {
	op    string
	limit time.Duration
}

func (e *stallError) Error() string {
	limit := e.limit
	if limit == 0 {
		limit = stallTimeout
	}
	return fmt.Sprintf("hwmediacodec: %s: %s: the browser made no progress within %s", Name, e.op, limit)
}

// try runs fn and turns the JavaScript exception it throws, if any, into
// an error; syscall/js reports exceptions as panics.
func try(op string, fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(js.Error)
			if !ok {
				panic(r)
			}
			err = jsError(op, e.Value)
		}
	}()
	fn()
	return nil
}

func first(args []js.Value) js.Value {
	if len(args) == 0 {
		return js.Undefined()
	}
	return args[0]
}

// then runs ok or fail once the promise settles, without blocking; it is
// what a callback uses, which must not block.
func then(promise js.Value, ok, fail func(js.Value)) {
	var onOK, onFail js.Func
	release := func() {
		onOK.Release()
		onFail.Release()
	}
	onOK = js.FuncOf(func(_ js.Value, args []js.Value) any {
		release()
		ok(first(args))
		return nil
	})
	onFail = js.FuncOf(func(_ js.Value, args []js.Value) any {
		release()
		fail(first(args))
		return nil
	})
	promise.Call("then", onOK, onFail)
}

// await blocks the goroutine until the promise settles and returns its
// value, or the rejection as an error. The page's event loop runs
// meanwhile. It must not be called from a callback.
func await(ctx context.Context, op string, promise js.Value) (js.Value, error) {
	return awaitWithin(ctx, op, promise, stallTimeout)
}

// awaitWithin is await with a limit of its own.
func awaitWithin(ctx context.Context, op string, promise js.Value, limit time.Duration) (js.Value, error) {
	type result struct {
		value js.Value
		err   error
	}
	ch := make(chan result, 1)
	then(promise,
		func(v js.Value) { ch <- result{value: v} },
		func(v js.Value) { ch <- result{err: jsError(op, v)} })
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.value, r.err
	case <-ctx.Done():
		return js.Undefined(), ctx.Err()
	case <-timer.C:
		return js.Undefined(), &stallError{op: op, limit: limit}
	}
}

// waiter is how a method waits for a callback: the callback signals, the
// method blocks in wait and then looks at the state again.
type waiter chan struct{}

func newWaiter() waiter { return make(waiter, 1) }

// signal wakes the waiting method, if any. It never blocks.
func (w waiter) signal() {
	select {
	case w <- struct{}{}:
	default:
	}
}

// wait blocks until the next signal. deadline bounds a whole wait loop.
func (w waiter) wait(ctx context.Context, op string, deadline time.Time) error {
	left := time.Until(deadline)
	if left <= 0 {
		return &stallError{op: op}
	}
	timer := time.NewTimer(left)
	defer timer.Stop()
	select {
	case <-w:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return &stallError{op: op}
	}
}

// supported asks a codec class whether it can be configured with config.
// A configuration the browser rejects as malformed counts as unsupported;
// the error is the context's or a stalled browser's.
func supported(ctx context.Context, cls js.Value, config map[string]any) (bool, error) {
	var promise js.Value
	if err := try("isConfigSupported", func() { promise = cls.Call("isConfigSupported", config) }); err != nil {
		return false, nil
	}
	res, err := await(ctx, "isConfigSupported", promise)
	var rejected *codec.BackendError
	switch {
	case err == nil:
		return res.Get("supported").Truthy(), nil
	case errors.As(err, &rejected):
		return false, nil
	}
	return false, err
}

// accelerationOverride replaces the hardwareAcceleration member of every
// configuration when set. The tests set it to "prefer-software" to reach
// the browser's software codecs on a machine that has hardware ones.
var accelerationOverride string

// acceleration is the hardwareAcceleration member of a configuration.
func acceleration(allowSoftware bool) string {
	if accelerationOverride != "" {
		return accelerationOverride
	}
	if allowSoftware {
		return "no-preference"
	}
	return "prefer-hardware"
}

// jsBuffers recycles the Uint8Arrays pixel data crosses the boundary in;
// all of one size, since a stream's frames are.
type jsBuffers struct {
	size int
	free []js.Value
}

func (p *jsBuffers) get(size int) js.Value {
	if size != p.size {
		p.size, p.free = size, nil
	}
	if n := len(p.free); n > 0 {
		b := p.free[n-1]
		p.free = p.free[:n-1]
		return b
	}
	return uint8Array.New(size)
}

func (p *jsBuffers) put(b js.Value) {
	if b.Length() == p.size && len(p.free) < 4 {
		p.free = append(p.free, b)
	}
}

// goBuffers recycles the memory of released frames.
type goBuffers struct {
	size int
	free [][]byte
}

func (p *goBuffers) get(size int) []byte {
	if size != p.size {
		p.size, p.free = size, nil
	}
	if n := len(p.free); n > 0 {
		b := p.free[n-1]
		p.free = p.free[:n-1]
		return b
	}
	return make([]byte, size)
}

func (p *goBuffers) put(b []byte) {
	if len(b) == p.size && len(p.free) < maxPending {
		p.free = append(p.free, b)
	}
}
