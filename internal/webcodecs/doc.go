// Package webcodecs implements the browser backend on top of the WebCodecs
// API (VideoDecoder, VideoEncoder, VideoFrame, EncodedVideoChunk), reached
// through syscall/js in a GOOS=js GOARCH=wasm build.
//
// WebCodecs is a full codec API and the browser sits between the page and
// the platform's engine (VideoToolbox, Media Foundation, VA-API, MediaCodec):
// the decoder takes Annex-B access units or AV1 temporal units and returns
// VideoFrame objects, the encoder takes VideoFrame objects and returns
// Annex-B access units. The backend keeps frames in CPU memory like every
// other one: a decoded VideoFrame is copied out with VideoFrame.copyTo and
// closed at once, an input frame is wrapped in a VideoFrame built from its
// NV12 bytes.
//
// # Hardware
//
// Whether a codec is hardware is the browser's answer. Configurations ask
// for hardwareAcceleration "prefer-hardware", which Chromium treats as a
// requirement (isConfigSupported reports false when only a software codec
// exists) and the specification as a hint; WithSoftwareFallback asks for
// "no-preference" instead and so lets the browser's software codecs in.
//
// # Asynchrony
//
// WebCodecs delivers its results in callbacks on the page's event loop,
// and a Go program compiled to WebAssembly shares that one thread: a
// callback runs only while every goroutine is blocked. The backend
// therefore blocks where the browser must catch up (Send while the codec's
// queue is saturated, Flush until the browser has delivered everything)
// and never blocks in a callback. Receive does not wait for the browser:
// like on the other backends it reports ErrAgain when nothing is ready, so
// a caller that polls it in a loop without ever blocking (no Send, no
// channel, no sleep) would spin forever. The Send/Receive loops of the
// package documentation and of DecodeReader and EncodeWriter are fine.
//
// The state of a decoder or encoder is touched by its methods and by the
// callbacks, which never run concurrently, so there is no lock.
//
// # Order
//
// The specification makes a decoder emit frames in presentation order.
// Chromium does so from the picture order counts, whatever the timestamps
// of the chunks say (measured with Chrome on macOS). The backend does not
// build on every engine getting there the same way, nor on the caller's
// timestamps being presentation times: it gives every chunk a timestamp of
// its own (a counter), which the frame brings back, and reports H.264 and
// HEVC output as not being in display order. The public API's reorder
// layer then sorts the frames by picture order count, whatever order they
// arrived in; for a stream with B-frames that holds frames back by the
// reorder bound a second time. WithDecodeOrder returns the browser's
// order. AV1 temporal units come in presentation order and yield one frame
// each.
//
// # Colour
//
// For RGBA and BGRA output the browser converts (VideoFrame.copyTo with a
// format), with the matrix and range the stream declares. For a stream
// that declares none the browsers guess, and not alike for every codec
// (Chrome took BT.709 for an untagged 320x240 AV1 stream and BT.601 for
// the same pictures in H.264), so the backend puts the library's own rule
// (pixconv.ColorFor) into the decoder configuration, which overrides the
// guess. Frames the browser keeps in a GPU texture without a pixel format
// (10-bit content, some hardware decoders) cannot be copied; for RGB
// output they are drawn on an OffscreenCanvas and read back. NV12 output
// exists for 8-bit 4:2:0 pictures the browser hands over as NV12 or I420
// only: a stream of another depth or sampling is refused with
// ErrUnsupported when its keyframe arrives, and a browser whose decoders
// hand over RGB (Firefox on macOS returns BGRX) gets ErrUnsupported from
// Receive.
//
// # Browsers
//
// Chrome on macOS passes the conformance tests. Firefox on macOS runs
// with limits that are its own (RGB frames only, the colour description of
// a configuration ignored for H.264 and not written by its encoder, no
// constant-quality mode, no HEVC), and so does Safari (full-range frames,
// which are brought back to the stream's range, an H.264 encoder that
// works in the "realtime" latency mode only, full-range HEVC output); the
// README's browser notes list them. Which latency mode an encoder works in
// is found out with a trial of a few frames the first time a page opens
// one.
package webcodecs
