// Package mediacodec implements the Android backend on top of the NDK
// MediaCodec C API (libmediandk.so), loaded at run time with purego.
//
// MediaCodec is a full codec API: the decoder takes Annex-B access units (or
// AV1 temporal units) and returns pictures in display order with the input
// timestamps attached, and the encoder returns access units with the
// parameter sets in a separate codec-config buffer. The backend drives it in
// synchronous mode (dequeue and queue with timeouts, no callbacks) and keeps
// frames in CPU memory: decoder output goes through MediaCodec's byte
// buffers, whose plane layout the framework describes in the output format
// ("image-data", a MediaImage2, or the colour format with stride and slice
// height), and is packed into NV12 in Go; encoder input is NV12 or I420 byte
// buffers filled from the caller's NV12 frame.
//
// Android 14 (API 34) is the supported minimum; every symbol used exists
// from API 28, where Load fails the backend reports no capability.
//
// Whether a codec is hardware is decided from its name
// (AMediaCodec_getName): "c2.android." and "OMX.google." are the framework's
// software codecs, as is any name containing ".sw.". The backend refuses
// those unless WithSoftwareFallback is given. Nothing of this has run on a
// device yet; see the README's Android notes.
package mediacodec
