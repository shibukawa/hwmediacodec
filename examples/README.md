# hwmediacodec examples

Small, complete programs that show how to use `hwmediacodec` for everyday
jobs. They live in their own Go module (`github.com/shibukawa/hwmediacodec/examples`)
so that the core library keeps its two dependencies; the examples pull in
[mp4ff](https://github.com/Eyevinn/mp4ff) for MP4 files and, later in this
directory, Ebitengine and pion for the game and streaming samples. Inside the
repository the module points at the core with a `replace ../` directive.

Everything here was written and verified on an Apple Silicon Mac; the same
code runs wherever `hwmediacodec.Probe` reports a hardware engine.

| Directory | What it shows |
| --- | --- |
| [`container/`](container/) | The glue the other samples share: an MP4 demuxer that hands out Annex-B access units and a progressive MP4 muxer fed with encoder packets |
| [`convert/`](convert/) | Video file converter (H.264 ↔ HEVC) that keeps timestamps and copies audio |
| [`thumbnails/`](thumbnails/) | Keyframe thumbnails from an MP4, decoding only the sync samples |
| [`screencast/`](screencast/) | Captures an Ebitengine screen into the hardware encoder on a background goroutine; sinks for MP4 files and anything else |
| [`record/`](record/) | Ebitengine game whose screen is recorded to an MP4 file |
| [`hls/`](hls/) | Ebitengine game streamed live to browsers as fMP4 HLS (segmenter + in-memory playlist server) |
| [`texture/`](texture/) | Video as a texture in Ebitengine: flat, on a spinning cube with `DrawTriangles`, and through a Kage shader; plays MP4 files directly |
| [`webrtc/`](webrtc/) | Ebitengine game streamed to browsers over WebRTC with pion, about 100 ms of latency |

## container

`hwmediacodec` deliberately stops at the elementary stream: packets in, frames
out, Annex-B on both ends. Files need a container, and this package is the
smallest useful bridge, built on mp4ff's box parser:

```go
d, _ := container.Open("movie.mp4")
v := d.Video()                      // codec, size, time scale, sample table
pkt, _ := v.Packet(i)               // Annex-B access unit with SPS/PPS in front of keyframes
dec, _ := hwmediacodec.NewDecoder(ctx, v.Codec, hwmediacodec.WithTimeScale(int32(v.TimeScale)))
dec.Send(ctx, pkt)                  // pkt.PTS is the MP4 sample time

m, _ := container.Create("out.mp4")
vw, _ := m.AddVideoTrack(hwmediacodec.HEVC, v.TimeScale)
vw.WritePacket(p)                   // p from Encoder.Receive: Annex-B, PTS, DTS, Keyframe
aw := m.AddPassthroughTrack(d.Others()[0])
aw.WriteSample(s)                   // audio copied as is
m.Close()                           // writes moov
```

Design points worth copying into your own code:

- `Track.MediaTimeOffset` carries the edit list. A B-frame stream's first
  picture has a PTS above its DTS and the muxer hides that delay with an
  `elst`; sample PTS minus the offset is the time a player shows the frame.
- The muxer learns SPS/PPS/VPS from the packets (hwmediacodec encoders put
  them in front of every keyframe) and strips them from the samples, so the
  output is `avc1`/`hvc1` as QuickTime and browsers expect.
- VideoToolbox numbers decode times from the first PTS when B-frames are on,
  so a reordered picture can carry DTS > PTS. MP4 needs DTS ≤ PTS, so the
  muxer shifts every DTS back by the largest lag; durations are unchanged and
  the edit list absorbs the start delay. The tests check that ffprobe sees
  the same presentation times as in the source.
- Progressive output only (`ftyp`, `mdat`, `moov` at the end), `stco` or
  `co64` as the size requires, one chunk per run of samples of the same
  track. Fragmented input is rejected; use mp4ff's segmenter for that.

## convert

```sh
cd examples
go run ./convert -codec hevc -bitrate 6M input.mp4 output.mp4
go run ./convert -codec h264 -quality 0.7 -bframes -gop 60 input.mp4 output.mp4
```

Flags: `-codec h264|hevc`, `-bitrate 6M|2500k` (VBR, `-cbr` for constant),
`-quality 0..1` instead of a bitrate, `-gop` keyframe interval, `-bframes`,
`-profile baseline|main|high`, `-software` to allow the OS software codec,
`-q` to silence progress.

The pipeline is decoder → encoder with the MP4 sample times as PTS:
`WithTimeScale(track.TimeScale)` on both ends means the frame PTS the
decoder returns are the sample times, the encoder's packets come back with
PTS and DTS in that same unit, and the muxer stores them unchanged. The
decoder runs in display order (the default), which is the order an encoder
wants its input in. Audio and any other track are copied sample by sample,
interleaved with the video by time. On an M3, 1080p H.264 with B-frames
converts to HEVC at about 160 frames per second end to end.

## thumbnails

```sh
cd examples
go run ./thumbnails -every 10s -width 320 -o thumbs movie.mp4
go run ./thumbnails -all -format png movie.mp4
```

Only sync samples go to the decoder. An IDR / IRAP / AV1 keyframe decodes
without any other picture, so a two-hour file costs one decode per
thumbnail. The decoder is opened with `WithOutputFormat(RGBA)`, whose
planes map straight onto `image.RGBA`, and `WithDecodeOrder()`, so that
each keyframe comes out immediately instead of waiting in the reorder
buffer. `-every` picks the keyframe at or before each instant; `-all`
takes every keyframe; `-width` downsamples with a box filter. File names
carry the presentation time (`movie_00-01-30.000.jpg`).

## screencast

`screencast.Recorder` is the capture path the game samples share:

```go
sink, _ := screencast.NewMP4File("capture.mp4", hwmediacodec.H264)
rec, _ := screencast.New(1280, 720, sink, screencast.Options{FPS: 60, Bitrate: 8_000_000})

func (g *game) Draw(screen *ebiten.Image) {
	g.scene.Draw(screen)
	rec.Capture(screen)      // ReadPixels, then the encoder runs on its own goroutine
}
// on exit
rec.Close()                  // flushes the encoder, finishes the file
```

- `Capture` calls `screen.ReadPixels` (about 2 ms at 720p on an M3; the
  GPU has to finish the frame first, so a heavy scene shows up in this
  number) and hands the RGBA buffer to a goroutine that feeds the encoder
  with `WithInputFormat(RGBA)`. The game loop never waits for the encoder;
  if the queue is full the frame is dropped and counted.
- PTS come from the wall clock quantised to the frame rate, so a dropped
  frame leaves a gap instead of speeding the recording up, and a 120 Hz
  display showing a 60 fps game does not record every frame twice.
- A `Sink` is just `WritePacket` + `Close`; `NewMP4File` writes through the
  container muxer, `Funcs` adapts closures, and the HLS and WebRTC samples
  plug their own in.

## record

```sh
cd examples
go run ./record -o capture.mp4 -seconds 10
go run ./record -o capture.mp4 -codec hevc -bitrate 12M -size 1920x1080
```

Runs the demo animation in a window and records it until the window closes
or `-seconds` pass; the summary line reports frames, drops and the average
capture cost per draw.

## hls

```sh
cd examples
go run ./hls -addr :8080            # then open http://localhost:8080/
go run ./hls -segment 1s -bitrate 2M
```

The same animation, encoded with a keyframe interval equal to the segment
length and `WithLowLatency`, cut by `container.Segmenter` into CMAF/fMP4
segments (init segment with the parameter sets, then one `moof`+`mdat` per
segment, each starting at a keyframe) and served from memory with a
sliding-window playlist (`#EXT-X-MAP`, `#EXT-X-MEDIA-SEQUENCE`). The page
at `/` plays natively in Safari and through hls.js (MSE) elsewhere;
`/?hlsjs` forces hls.js. Latency is a few seconds, which is what plain HLS
gives; the WebRTC sample is the low-latency path. HEVC (`-codec hevc`)
plays in Safari only.

## texture

```sh
cd examples
go run ./texture movie.mp4                      # keys: 1 flat, 2 cube, 3 shader, space pause
go run ./texture -codec hevc -fps 30 stream.hevc
go run ./texture -seconds 12 -record demo.mp4 movie.mp4
```

`ebitenvideo.Player.Image()` is an ordinary `*ebiten.Image` that the player
updates in place, so the video goes wherever an image goes:

- **flat**: `DrawImage`, scaled to fit.
- **cube**: `DrawTriangles` with the video as the texture of all six faces.
  Ebitengine interpolates texture coordinates affinely, so each face is a
  grid of 8x8 cells whose corners are projected separately (the usual trick
  for perspective without a perspective divide); faces are culled by their
  projected winding and shaded by a directional light through the vertex
  colours.
- **shader**: `DrawRectShader` with the video in `Images[0]`; the Kage shader
  bends the picture, ripples it and adds scanlines and a vignette with
  `imageSrc0At`.

MP4 input is demuxed by the container package: `VideoTrack.ElementaryStream()`
is an `io.ReadSeeker` of the Annex-B stream (rewindable, which is what
`WithLoop` needs) and `FrameRate()` comes from the sample table, so the
player needs no `-fps`. `-record` turns the screencast recorder on the
window itself, which is how the demo recording in the repository's history
was made.

## webrtc

```sh
cd examples
go run ./webrtc -addr :8080         # then open http://localhost:8080/
go run ./webrtc -stun stun:stun.l.google.com:19302   # viewers outside the LAN
```

The low-latency counterpart of `hls`. The recorder encodes with
`WithLowLatency`, Baseline profile and no B-frames; every access unit is
handed to a [pion](https://github.com/pion/webrtc) `TrackLocalStaticSample`
per viewer, whose H.264 payloader splits the Annex-B NAL units into RTP
(STAP-A for the parameter sets, FU-A for large slices). Signalling is a
single HTTP POST of the browser's SDP offer; the answer is returned once
ICE gathering is done, so no trickle ICE and no WebSocket. A viewer joining
or sending a picture-loss indication calls `Recorder.RequestKeyframe`, and
a viewer only starts receiving at a keyframe.

Measured in a Chromium browser on the same machine at 1280x720: jitter
buffer delay about 8 ms, decode about 1.3 ms per frame, no packet loss;
the end-to-end delay is dominated by the encoder's low-latency pipeline and
the display, a few frames in total. The tests use pion as the viewer:
access units received over the loopback RTP path are rebuilt with pion's
sample builder, compared NAL unit by NAL unit with what was sent, and
decoded by ffmpeg to the source's frame checksums; a PLI from the viewer
must reach the keyframe callback.

## Testing

```sh
cd examples
go test ./...
```

The container, segmenter, HLS server and WebRTC broadcaster tests need
only ffmpeg and ffprobe: they compare sample tables, presentation times and
decoded frame checksums with ffprobe's view of the same files, feed
ffmpeg-made streams through the segmenter and the WebRTC track and let
ffprobe play the served playlist over HTTP.
The convert, thumbnails and screencast tests also need a hardware codec and
skip otherwise; they check codec, frame count, PSNR against the source,
copied audio, identical presentation times and the recorder's timing. The
texture sample's mesh (projection, culling, texture coordinates) has a unit
test; its rendering was checked by recording the window.
