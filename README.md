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
| macOS, Apple Silicon | VideoToolbox | H.264, HEVC (NV12, CPU memory) | not yet |
| Linux, AMD (Mesa) and Intel (iHD / i965) | VA-API | H.264 (NV12, CPU memory) | not yet |
| Linux | Intel VPL / NVDEC | planned | planned |
| Windows | Media Foundation / NVENC | planned | planned |

Known limitations:

- Frames are returned in decode order on every backend. Streams with
  B-frames decode correctly (every frame matches the reference) but arrive
  out of display order; the caller must reorder by PTS until the planned
  Go-side reorder buffer lands. (The H.264 picture order count is now
  computed in Go for the VA-API backend, so the reorder buffer is the next
  step.)
- Input is Annex-B, one access unit per `Packet`; use `annexb.Reader` to
  split a raw elementary stream. Containers (MP4, MKV, TS) are not parsed.
- Output is NV12 in CPU memory. On an M3, 1080p H.264 decodes at roughly
  800 frames per second including the copy.
- VideoToolbox: Intel Macs are out of scope; the backend requires a hardware
  decoder unless `WithSoftwareFallback` is given.
- VA-API: H.264 only for now (HEVC is next); progressive frames only
  (interlaced field pictures are rejected with `ErrUnsupported`); 8-bit
  4:2:0 only. VA-API is a slice-level API, so the bitstream parsing,
  picture order count, reference marking and reference list construction
  run in Go (`internal/h264`), and the driver only accelerates the slice
  data. `Send` returns `ErrAgain` when more than a few decoded frames are
  waiting for `Receive`; drain and resend.

## Usage

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

A command-line tool exercises the same API:

```sh
go run ./cmd/hwmediacodec probe
go run ./cmd/hwmediacodec decode -hash input.h264
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
ffmpeg's software decoder; they are skipped when ffmpeg is not installed:

```sh
go test ./...
./scripts/crossbuild.sh   # CGO_ENABLED=0 builds for every target
```

Project knowledge (requirements, decisions, backend notes) lives in
`.knowledge/` and is maintained with the knowledge-memory skill.
