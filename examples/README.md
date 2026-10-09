# hwmediacodec examples

Small, complete programs that show how to use `hwmediacodec` for everyday
jobs. They live in their own Go module (`github.com/shibukawa/hwmediacodec/examples`)
so that the core library does not depend on Ebitengine and pion, which the
game and streaming samples pull in. MP4 files go through the core module's
[`mediacontainer/mp4`](../mediacontainer/mp4) package and screen capture
through its [`capture`](../capture) package. Inside the
repository the module points at the core with a `replace ../` directive.

Everything here was written and verified on an Apple Silicon Mac; the same
code runs wherever `hwmediacodec.Probe` reports a hardware engine.

Each program runs straight from its module path, with nothing to clone or
install (Go 1.27 or later is needed):

```sh
go run github.com/shibukawa/hwmediacodec/examples/convert@latest -codec hevc -bitrate 6M input.mp4 output.mp4   # H.264 / HEVC converter
go run github.com/shibukawa/hwmediacodec/examples/thumbnails@latest -every 10s -o thumbs movie.mp4              # keyframe thumbnails
go run github.com/shibukawa/hwmediacodec/examples/record@latest -o capture.mp4 -seconds 10                      # fireworks show to MP4
go run github.com/shibukawa/hwmediacodec/examples/hls@latest -addr :8080                                        # fireworks show over HLS, opens the browser
go run github.com/shibukawa/hwmediacodec/examples/webrtc@latest -addr :8080                                     # fireworks show over WebRTC, opens the browser
go run github.com/shibukawa/hwmediacodec/examples/player@latest                                                 # video player, bundled clip
go run github.com/shibukawa/hwmediacodec/examples/texture@latest                                                # video as a texture (flat, box, shader)
go run github.com/shibukawa/hwmediacodec/examples/heifconv@latest photo.heic photo.png                          # HEIC or AVIF to PNG or JPEG
go run github.com/shibukawa/hwmediacodec/examples/heifconv@latest -quality 0.8 picture.png picture.heic         # PNG or JPEG to HEIC
```

Inside a clone, `cd examples` and then `go run ./player` (and so on) runs the
same programs from the working tree.

Without arguments, `player` and `texture` play the bundled clip in
`assets/`. The library-only directories (`assets`, `internal/fireworks`)
have no program of their own.

| Directory | What it shows |
| --- | --- |
| [`assets/`](assets/) | The bundled sample clip (a ten-second portrait waterfall shot by the author, 720x1280 HEVC with AAC), embedded so the players run from anywhere |
| [`convert/`](convert/) | Video file converter (H.264 ↔ HEVC) that keeps timestamps and copies audio |
| [`thumbnails/`](thumbnails/) | Keyframe thumbnails from an MP4, decoding only the sync samples |
| [`internal/fireworks/`](internal/fireworks/) | The scene the three programs below capture: a fireworks show over water with a Kage post-process, shells launched from the keyboard, now and then a gopher-shaped one |
| [`record/`](record/) | The fireworks show recorded to an MP4 file |
| [`hls/`](hls/) | The fireworks show streamed live to browsers as fMP4 HLS (segmenter + in-memory playlist server); opens the player page |
| [`player/`](player/) | The video player: plays the bundled clip or any MP4/raw stream in a window of the video's aspect ratio, with pause, seeking and a progress bar |
| [`texture/`](texture/) | Video as a texture in Ebitengine: flat, on a spinning box with `DrawTriangles`, and through a gentle Kage shader; plays the bundled clip by default |
| [`webrtc/`](webrtc/) | The fireworks show streamed to browsers over WebRTC with pion, about 100 ms of latency; opens the player page |
| [`heif/`](heif/), [`heifconv/`](heifconv/) | HEIC and AVIF still images: decode (single pictures, grids, rotation, clean aperture) and encode HEIC with the HEVC encoder, AVIF where an AV1 encoder exists |

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

## Screen recording

The game samples capture their window with `capture.Recorder`, a
package of the core module
(`github.com/shibukawa/hwmediacodec/capture`; see the main README).
The samples differ in the sink the recorder writes into:

```go
sink, _ := mp4.CreateVideoFile("capture.mp4", hwmediacodec.H264, capture.TimeScale)
rec, _ := capture.New(1280, 720, sink, capture.Options{FPS: 60, Bitrate: 8_000_000})

func (g *game) Draw(screen *ebiten.Image) {
	g.scene.Draw(screen)
	rec.Capture(screen)      // ReadPixels, then the encoder runs on its own goroutine
}
// on exit
rec.Close()                  // flushes the encoder, finishes the file
```

- `mp4.CreateVideoFile` (`mediacontainer/mp4`) is an MP4 file with one video track
  (`record`, and `-record` of `texture`).
- `mp4.Segmenter` cuts the packets into fMP4 segments for the HLS
  playlist server (`hls`).
- The broadcaster in `webrtc` hands each access unit to a pion track.

## The fireworks scene

`internal/fireworks` is what `record`, `hls` and `webrtc` show. It is
written to look good on a stream and to give the encoder real work:

- It opens on a title screen ("HANABI", a blinking "PRESS A FOR THE AUTO
  SHOW" and the key list, drawn with `text/v2` and the Go font). `A`
  starts the automatic show; any other key starts manual play, and is
  already a launch when it is one of the keys below. `record` skips the
  title by default (`-auto`), `hls` and `webrtc` show it unless `-auto`.
- Shells rise from the shore with a spark trail and burst at their apex into
  one of six types: peony, chrysanthemum, willow, ring (a circle in a
  random plane), palm and crackle (stars that pop into sparks). Keys `1`
  to `6` launch them, `space` a random one, `F` a volley, `A` toggles the
  automatic show.
- `G` launches the gopher shell: about 900 stars whose velocities are
  solved so that they land on a gopher silhouette (body, ears, eyes with
  pupil holes, snout, teeth, arms, feet) 1.1 s after the burst, with a
  random tilt, non-uniform scale and per-star jitter, and gravity bending
  the figure afterwards. The automatic show fires one every 10 to 16 s.
- Stars are additive sprites drawn into a full-brightness layer for the
  current frame and, dimmed, into a persistence buffer that fades
  (multiply, then subtract a constant so 8-bit trails really reach black).
- A Kage shader composes the frame: night sky gradient with a warm city
  glow, procedural twinkling stars, the two particle layers with a cheap
  16-tap bloom, a skyline silhouette with lit windows (a mask image), and
  below the horizon a rippling, dimmed reflection of all of it.

## record

```sh
cd examples
go run ./record -o capture.mp4 -seconds 10
go run ./record -o capture.mp4 -codec hevc -bitrate 12M -size 1920x1080
go run ./record -o gopher.mp4 -seconds 8 -launch gopher
```

Runs the fireworks show in a window and records it until the window closes
or `-seconds` pass; `-launch` fires a given shell type at the start and
every four seconds. The summary line reports frames, drops and the average
capture cost per draw.

## hls

```sh
cd examples
go run ./hls -addr :8080            # opens http://localhost:8080/ in the browser
go run ./hls -segment 1s -bitrate 2M -open=false
```

The fireworks show, encoded with a keyframe interval equal to the segment
length and `WithLowLatency`, cut by `mp4.Segmenter` into CMAF/fMP4
segments (init segment with the parameter sets, then one `moof`+`mdat` per
segment, each starting at a keyframe) and served from memory with a
sliding-window playlist (`#EXT-X-MAP`, `#EXT-X-MEDIA-SEQUENCE`). The page
at `/` plays natively in Safari and through hls.js (MSE) elsewhere;
`/?hlsjs` forces hls.js. Latency is a few seconds, which is what plain HLS
gives; the WebRTC sample is the low-latency path. HEVC (`-codec hevc`)
plays in Safari only.

## player

```sh
cd examples
go run ./player                       # the bundled waterfall clip, looping
go run ./player movie.mp4
go run ./player -once -codec hevc -fps 30 stream.hevc
```

The plain video player: `ebitenvideo.NewPlayerFromSource` over
`mp4.VideoTrack.PacketSource()` for MP4 files (presentation times,
the sync-sample table for seeking, the length), `ebitenvideo.NewPlayer`
for raw streams. The window takes the video's aspect ratio; space pauses,
the arrow keys seek five seconds, Home restarts; a progress bar runs along
the bottom. Audio tracks are ignored (Ebitengine has no AAC decoder). With
no argument it plays `assets/waterfall-720p-hevc.mp4`, which is embedded
into the binary.

## texture

```sh
cd examples
go run ./texture                      # the bundled clip; keys: 1 flat, 2 box, 3 shader, space pause
go run ./texture movie.mp4
go run ./texture -codec hevc -fps 30 stream.hevc
go run ./texture -seconds 12 -record demo.mp4
```

`ebitenvideo.Player.Image()` is an ordinary `*ebiten.Image` that the player
updates in place, so the video goes wherever an image goes:

- **flat**: `DrawImage`, scaled to fit.
- **box**: `DrawTriangles` with the video as the texture of a spinning box
  whose side faces have the video's aspect ratio (a portrait clip gives a
  tall box). Ebitengine interpolates texture coordinates affinely, so each
  face is a grid of 8x8 cells whose corners are projected separately (the
  usual trick for perspective without a perspective divide); faces are
  culled by their projected winding and shaded by a directional light
  through the vertex colours.
- **shader**: `DrawRectShader` with the video in `Images[0]`; the Kage
  shader bends the picture slightly, ripples it and adds faint scanlines
  and a vignette with `imageSrc0At`, kept gentle so real footage still
  looks like itself.

MP4 input is demuxed by the `mediacontainer/mp4` package: `VideoTrack.PacketSource()`
implements `ebitenvideo.Source` and `Seeker` (access units with
presentation times, the sync-sample table for seeking, the track length),
so `ebitenvideo.NewPlayerFromSource` plays it without an `-fps` flag and
the arrow keys seek five seconds (`Home` restarts). Raw `.h264`/`.hevc`
files go through `NewPlayer` and seek as well, after a one-time scan for
keyframes. `-record` turns the capture recorder on the window itself.

## webrtc

```sh
cd examples
go run ./webrtc -addr :8080         # opens http://localhost:8080/ in the browser
go run ./webrtc -stun stun:stun.l.google.com:19302   # viewers outside the LAN
go run ./webrtc -open=false         # print the URL only
```

Both servers open the player page with `github.com/pkg/browser` unless
`-open=false` is given; the window keeps the keyboard, so launch shells
there and watch them in the browser. `-auto` skips the title screen and
`-minimized` minimizes the window a second after start: Ebitengine keeps
rendering a minimized window (measured on macOS: 60 fps, 2 s segments
kept coming for the whole run), so a stream can run with nothing on
screen; Ebitengine cannot run without a window at all, so a windowless
server would render frames some other way and feed the same recorder,
segmenter and broadcaster, none of which touch the display.

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

## heif and heifconv

```sh
cd examples
go run ./heifconv photo.heic photo.png              # iPhone photos: HEVC tiles in a grid, irot
go run ./heifconv -quality 0.8 picture.png picture.heic
go run ./heifconv -tile 512 -rotate 90 picture.jpg picture.heic
go run ./heifconv picture.avif picture.png          # AV1 decode (M3 and newer)
go run ./heifconv -info photo.heic
```

A HEIF file is an ISOBMFF `meta` box of items (coded pictures, a `grid`
that tiles them) with properties (`hvcC`/`av1C` decoder configuration,
`ispe` size, `irot`, `imir`, `clap`, `colr`, `pixi`) and an `mdat` with the
coded data. The `heif` package parses and writes that by hand (about 600
lines, no dependency beyond mp4ff's record parsers) and leaves the pictures
to hwmediacodec:

- **Decode**: for each coded item, `hvcC`'s parameter sets plus the
  length-prefixed NAL units become one Annex-B packet (AV1: the temporal
  unit, with the sequence header from `av1C` if the item lacks one); all
  items go through one decoder opened with `WithOutputFormat(RGBA)` and
  `WithDecodeOrder()`, grids are stitched tile by tile and cropped to the
  grid size, then `clap`, `irot` and `imir` are applied in their stored
  order. `Decode` returns an `*image.RGBA` and an `Info` (codec, size,
  tiles, rotation).
- **Encode**: one keyframe per picture or tile (`WithKeyframeInterval(1)`,
  `ForceKeyframe`, `WithQuality`), VPS/SPS/PPS from the packet into `hvcC`,
  the slices as the item data. `TileSize` writes a grid like phone cameras
  do; `Rotation` stores an `irot`. Odd sizes are padded by a replicated
  row or column and declared through `clap`, because the hardware encoders
  work on even 4:2:0 pictures (VideoToolbox rounds an odd request down to
  320x202 for 321x203).
- **AVIF**: decoding works wherever `Probe` lists an AV1 decoder (M3 and
  newer Macs); encoding needs an AV1 encoder, which no Apple Silicon chip
  has, so `Encode` with `Codec: AV1` returns `ErrUnsupported` there and
  works unchanged on a platform that gains one.

Tests use macOS ImageIO (`sips`) as the reference for the files this
package writes (single, grid, rotated, odd sizes) and for HEIC input, and
ffmpeg for AVIF input and for the rotation direction (ffmpeg maps `irot` to
a display matrix and autorotates; ImageIO keeps it as orientation
metadata). ImageIO resamples `clap`-cropped pictures instead of cropping,
and ffmpeg rounds odd `clap` sizes to even, so odd pictures are compared
with the source instead. 8-bit 4:2:0 only; iPhone HDR photos (10-bit) are
out of scope.

## Testing

```sh
cd examples
go test ./...
```

The HLS server and WebRTC broadcaster tests need only ffmpeg and ffprobe:
they feed ffmpeg-made streams through the segmenter and the WebRTC track
and let ffprobe play the served playlist over HTTP.
The convert, thumbnails and heif tests also need a hardware codec and skip
otherwise; they check codec, frame count, PSNR against the source, copied
audio and identical presentation times. (The MP4 demuxer, muxer and
segmenter tests moved to the core module with the package.) The
texture sample's mesh (projection, culling, texture coordinates) has a unit
test and the player opens the bundled clip through a seek; the rendering
was checked by recording the window.
