# hwmediacodec

Hardware video decoding and encoding from Go, without cgo.

The library loads the operating system's codec engines at run time
(VideoToolbox on macOS and VA-API on Linux today; Media Foundation, Intel
VPL and NVENC/NVDEC are planned) through
[purego](https://github.com/ebitengine/purego), so `CGO_ENABLED=0 go build`
works and the module cross-compiles from one machine.

## Status

| Platform | Backend | Decode | Encode |
| --- | --- | --- | --- |
| macOS, Apple Silicon | VideoToolbox | H.264, HEVC (NV12, CPU memory) | H.264, HEVC (NV12 in, Annex-B out) |
| Linux, AMD (Mesa) and Intel (iHD / i965) | VA-API | H.264 (NV12, CPU memory) | not yet |
| Linux | Intel VPL / NVDEC | planned | planned |
| Windows | Media Foundation / NVENC | planned | planned |

Encoder controls: target bitrate with VBR or CBR, constant quality, keyframe
interval, forced keyframes, B-frames on/off, low-latency mode, profile, and
in-band VPS/SPS/PPS in front of every keyframe.

Known limitations:

- Decoded frames are returned in decode order on every backend. Streams with
  B-frames decode correctly (every frame matches the reference) but arrive
  out of display order; the caller must reorder by PTS until the planned
  Go-side reorder buffer lands. VideoToolbox's temporal-processing flag did
  not reorder in our tests, so this cannot be delegated to the OS; the H.264
  picture order count is now computed in Go for the VA-API backend, so the
  reorder buffer is the next step. The `transcode` command below inherits
  this: sources with B-frames are re-encoded in decode order.
- Input and output are Annex-B, one access unit per `Packet`; use
  `annexb.Reader` to split a raw elementary stream. Containers (MP4, MKV, TS)
  are not parsed. AVCC/HVCC output is not offered yet.
- Raw frames are NV12 in CPU memory. On an M3, 1080p H.264 decodes at
  roughly 800 frames per second including the copy, and 1080p H.264 encodes
  at roughly 200 frames per second (HEVC about 185) including the copy into
  the encoder's buffer.
- `Encoder.Flush` blocks until VideoToolbox has emitted every pending frame;
  it does not observe context cancellation once the call has started.
- VideoToolbox: Intel Macs are out of scope; the backend requires hardware
  engines unless `WithSoftwareFallback` is given.
- VA-API: H.264 only for now (HEVC is next); progressive frames only
  (interlaced field pictures are rejected with `ErrUnsupported`); 8-bit
  4:2:0 only. VA-API is a slice-level API, so the bitstream parsing,
  picture order count, reference marking and reference list construction
  run in Go (`internal/h264`), and the driver only accelerates the slice
  data. `Send` returns `ErrAgain` when more than a few decoded frames are
  waiting for `Receive`; drain and resend.

## Usage

Decoding:

```go
dec, err := hwmediacodec.NewDecoder(ctx, hwmediacodec.H264)
if err != nil { /* errors.Is(err, hwmediacodec.ErrUnsupported) on machines without hardware */ }
defer dec.Close()

r := annexb.NewReader(file, hwmediacodec.H264)
for {
	au, err := r.Next()
	if err == io.EOF {
		break
	}
	if err := dec.Send(ctx, hwmediacodec.Packet{Data: au, PTS: pts}); err != nil {
		return err // errors.Is(err, hwmediacodec.ErrAgain): Receive first, then resend
	}
	for {
		f, err := dec.Receive(ctx)
		if errors.Is(err, hwmediacodec.ErrAgain) {
			break
		}
		// f.Planes[0] is Y, f.Planes[1] is interleaved CbCr (NV12)
		f.Release()
	}
}
dec.Flush(ctx) // then Receive until io.EOF
```

Encoding:

```go
enc, err := hwmediacodec.NewEncoder(ctx, hwmediacodec.H264, 1920, 1080,
	hwmediacodec.WithFrameRate(30),
	hwmediacodec.WithBitrate(4_000_000),      // VBR; add WithRateControl(hwmediacodec.CBR) for CBR
	hwmediacodec.WithKeyframeInterval(60))    // keyframe every two seconds
if err != nil { /* errors.Is(err, hwmediacodec.ErrUnsupported) */ }
defer enc.Close()

for i, f := range frames { // *hwmediacodec.Frame, NV12 planes owned by the caller
	f.PTS = int64(i) * int64(hwmediacodec.DefaultTimeScale) / 30
	if err := enc.Send(ctx, f); err != nil { // the frame is copied before Send returns
		return err
	}
	for {
		p, err := enc.Receive(ctx)
		if errors.Is(err, hwmediacodec.ErrAgain) {
			break
		}
		out.Write(p.Data) // one Annex-B access unit; p.Keyframe, p.PTS, p.DTS
	}
}
enc.Flush(ctx) // then Receive until io.EOF
```

Set `Frame.ForceKeyframe` to start a segment on a given frame, `WithBFrames`
to allow reordering (packets then come in decode order with `DTS` lagging
`PTS`), `WithQuality(0.7)` for constant quality instead of a bitrate, and
`WithLowLatency` for live streaming.

A command-line tool exercises the same API:

```sh
go run ./cmd/hwmediacodec probe
go run ./cmd/hwmediacodec decode -hash input.h264
go run ./cmd/hwmediacodec encode -size 1920x1080 -bitrate 8M -gop 60 -o out.h264 input.nv12
go run ./cmd/hwmediacodec transcode -in h264 -codec hevc -bitrate 6M -o out.hevc input.h264
```

### Linux notes

The VA-API backend opens the first DRM render node (`/dev/dri/renderD128`
and up) that libva can initialise. Set `HWMEDIACODEC_VAAPI_DEVICE` to a
render node path to pick a GPU on multi-GPU machines. The user needs read
and write access to the node (usually the `render` or `video` group), and
`libva2`, `libva-drm2` and the GPU's VA driver (`mesa-va-drivers` for AMD,
`intel-media-va-driver` for Intel) must be installed. `Probe` reports no
VA-API capability, rather than an error, when any of these is missing.

## Testing

Unit tests run everywhere. The H.264 parser and decoded-picture-buffer
logic are checked against ffmpeg's own view of the stream (`trace_headers`,
`-debug mmco`, `-debug pict`), so they run on any machine with ffmpeg. The
decode conformance tests run where `Probe` reports a hardware decoder
(Apple Silicon, or Linux with a VA-API driver) and compare every frame with
ffmpeg's software decoder. The encode conformance tests run on Apple
Silicon: encoded streams are decoded by ffmpeg and compared to the source by
PSNR, and their keyframe and B-frame structure is checked with ffprobe. All
of them are skipped when ffmpeg is not installed.

```sh
go test ./...
CGO_ENABLED=0 go test ./...   # exercises the cgo-free callback path
./scripts/crossbuild.sh       # CGO_ENABLED=0 builds for every target
```

Project knowledge (requirements, decisions, backend notes) lives in
`.knowledge/` and is maintained with the knowledge-memory skill.
