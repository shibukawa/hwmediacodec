# hwmediacodec

Hardware video decoding and encoding from Go, without cgo.

The library loads the operating system's codec engines at run time
(VideoToolbox on macOS today; Media Foundation, Intel VPL, VA-API and
NVENC/NVDEC are planned for Windows and Linux) through
[purego](https://github.com/ebitengine/purego), so `CGO_ENABLED=0 go build`
works and the module cross-compiles from one machine.

## Status (milestone 2)

| Platform | Backend | Decode | Encode |
| --- | --- | --- | --- |
| macOS, Apple Silicon | VideoToolbox | H.264, HEVC (NV12, CPU memory) | H.264, HEVC (NV12 in, Annex-B out) |
| Windows | Media Foundation / NVENC | planned | planned |
| Linux | Intel VPL / VA-API / NVDEC | planned | planned |

Encoder controls: target bitrate with VBR or CBR, constant quality, keyframe
interval, forced keyframes, B-frames on/off, low-latency mode, profile, and
in-band VPS/SPS/PPS in front of every keyframe.

Known limitations of this milestone:

- Decoded frames are returned in decode order. Streams with B-frames decode
  correctly (every frame matches the reference) but arrive out of display
  order; the caller must reorder by PTS until the planned Go-side reorder
  buffer lands. VideoToolbox's temporal-processing flag did not reorder in
  our tests, so this cannot be delegated to the OS. The `transcode` command
  below inherits this: sources with B-frames are re-encoded in decode order.
- Input and output are Annex-B, one access unit per `Packet`; use
  `annexb.Reader` to split a raw elementary stream. Containers (MP4, MKV, TS)
  are not parsed. AVCC/HVCC output is not offered yet.
- Raw frames are NV12 in CPU memory. On an M3, 1080p H.264 decodes at
  roughly 800 frames per second including the copy, and 1080p H.264 encodes
  at roughly 200 frames per second (HEVC about 185) including the copy into
  the encoder's buffer.
- `Encoder.Flush` blocks until VideoToolbox has emitted every pending frame;
  it does not observe context cancellation once the call has started.
- Intel Macs are out of scope; the VideoToolbox backend requires hardware
  engines unless `WithSoftwareFallback` is given.

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

## Testing

Unit tests run everywhere. The decode and encode conformance tests run on
Apple Silicon and use the `ffmpeg` and `ffprobe` commands as the reference
(they are skipped when ffmpeg is not installed). Encoded streams are decoded
by ffmpeg and compared to the source by PSNR, and their keyframe and
B-frame structure is checked with ffprobe.

```sh
go test ./...
CGO_ENABLED=0 go test ./...   # exercises the cgo-free callback path
./scripts/crossbuild.sh       # CGO_ENABLED=0 builds for every target
```

Project knowledge (requirements, decisions, backend notes) lives in
`.knowledge/` and is maintained with the knowledge-memory skill.
