# hwmediacodec

Hardware video decoding and encoding from Go, without cgo.

The library loads the operating system's codec engines at run time
(VideoToolbox on macOS and Media Foundation on Windows today; Intel VPL,
VA-API and NVENC/NVDEC are planned for Linux) through
[purego](https://github.com/ebitengine/purego) on macOS and
`golang.org/x/sys/windows` plus raw COM vtable calls on Windows, so
`CGO_ENABLED=0 go build` works and the module cross-compiles from one machine.

## Status

| Platform | Backend | Decode | Encode |
| --- | --- | --- | --- |
| macOS, Apple Silicon | VideoToolbox | H.264, HEVC (NV12, CPU memory, decode order) | not yet |
| Windows x64 / ARM64, Intel, AMD, NVIDIA | Media Foundation + Direct3D 11 (DXVA) | H.264, HEVC (NV12, CPU memory, display order); **not yet verified on hardware** | planned |
| Linux | Intel VPL / VA-API / NVDEC | planned | planned |

Known limitations:

- On macOS frames are returned in decode order. Streams with B-frames decode
  correctly (every frame matches the reference) but arrive out of display
  order; the caller must reorder by PTS until the planned Go-side reorder
  buffer lands. VideoToolbox's temporal-processing flag did not reorder in
  our tests, so this cannot be delegated to the OS. The Media Foundation
  decoders reorder internally, so Windows returns display order.
- Input is Annex-B, one access unit per `Packet`; use `annexb.Reader` to
  split a raw elementary stream. Containers (MP4, MKV, TS) are not parsed.
- Output is NV12 in CPU memory. On an M3, 1080p H.264 decodes at roughly
  800 frames per second including the copy.
- Intel Macs are out of scope; the VideoToolbox backend requires a hardware
  decoder unless `WithSoftwareFallback` is given.

### Windows (Media Foundation)

The Windows backend drives the synchronous decoder MFTs that ship with
Windows (`Msmpeg2vdec.dll` for H.264, the "HEVC Video Extensions" package
for HEVC) with a Direct3D 11 device attached through `IMFDXGIDeviceManager`,
so the GPU's DXVA engine does the decoding whatever the vendor. `Probe`
reports a codec only when the GPU exposes the matching DXVA profile with
NV12 output *and* a usable decoder MFT exists; `NewDecoder` fails with
`ErrUnsupported` otherwise instead of decoding in software.

- Requires Windows 10 or later, a GPU driver with DXVA support, and for
  HEVC the free "HEVC Video Extensions from Device Manufacturer" (or the
  paid "HEVC Video Extensions") Store package.
- Decoded textures are copied to CPU memory through a staging texture; the
  visible picture is cropped from the padded coded size using
  `MF_MT_MINIMUM_DISPLAY_APERTURE`.
- `WithSoftwareFallback` is not implemented on Windows yet. Vendor
  asynchronous hardware MFTs (for example the Intel VP9 decoder MFT) are not
  used; H.264 and HEVC go through the Microsoft decoders, which is the
  common path on Intel, AMD and NVIDIA.
- 8-bit 4:2:0 only (NV12). 10-bit HEVC (P010) is rejected. Above 1920x1088
  the Microsoft H.264 decoder may fall back to software internally without
  reporting it.
- 32-bit Windows is out of scope; the backend builds only for `windows/amd64`
  and `windows/arm64`.
- The backend was written and cross-compiled on macOS against the Windows
  SDK headers (GUIDs, vtable layouts and structure sizes are checked at
  compile time) and has **not been run on Windows hardware yet**. Run
  `go test ./...` on a Windows machine with ffmpeg installed to verify; the
  conformance tests skip when `Probe` reports no hardware decoder.

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
and on Windows machines with a hardware decoder, and use the `ffmpeg` command
as a reference decoder (they are skipped when it is not installed or when no
hardware decoder is present):

```sh
go test ./...
./scripts/crossbuild.sh   # CGO_ENABLED=0 builds for every target
```

Project knowledge (requirements, decisions, backend notes) lives in
`.knowledge/` and is maintained with the knowledge-memory skill.
