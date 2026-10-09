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

## Testing

```sh
cd examples
go test ./...
```

The container tests need only ffmpeg and ffprobe (they compare sample
tables, presentation times and decoded frame checksums with ffprobe's view
of the same files). The convert and thumbnails tests also need a hardware
codec and skip otherwise; they check codec, frame count, PSNR against the
source, copied audio and that ffmpeg sees identical presentation times.
