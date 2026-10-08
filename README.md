# hwmediacodec

Hardware video decoding and encoding from Go, without cgo.

The library loads the operating system's codec engines at run time
(VideoToolbox on macOS today; Media Foundation, Intel VPL, VA-API and
NVENC/NVDEC are planned for Windows and Linux) through
[purego](https://github.com/ebitengine/purego), so `CGO_ENABLED=0 go build`
works and the module cross-compiles from one machine.

## Status (milestone 1)

| Platform | Backend | Decode | Encode |
| --- | --- | --- | --- |
| macOS, Apple Silicon | VideoToolbox | H.264, HEVC (NV12, CPU memory) | not yet |
| Windows | Media Foundation / NVENC | planned | planned |
| Linux | Intel VPL / VA-API / NVDEC | planned | planned |

Known limitations of this milestone:

- Frames are returned in decode order. Streams with B-frames decode
  correctly (every frame matches the reference) but arrive out of display
  order; the caller must reorder by PTS until the planned Go-side reorder
  buffer lands. VideoToolbox's temporal-processing flag did not reorder in
  our tests, so this cannot be delegated to the OS.
- Input is Annex-B, one access unit per `Packet`; use `annexb.Reader` to
  split a raw elementary stream. Containers (MP4, MKV, TS) are not parsed.
- Output is NV12 in CPU memory. On an M3, 1080p H.264 decodes at roughly
  800 frames per second including the copy.
- Intel Macs are out of scope; the VideoToolbox backend requires a hardware
  decoder unless `WithSoftwareFallback` is given.

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
		return err
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

## Testing

Unit tests run everywhere. The decode conformance tests run on Apple Silicon
and use the `ffmpeg` command as a reference decoder (they are skipped when it
is not installed):

```sh
go test ./...
./scripts/crossbuild.sh   # CGO_ENABLED=0 builds for every target
```

Project knowledge (requirements, decisions, backend notes) lives in
`.knowledge/` and is maintained with the knowledge-memory skill.
