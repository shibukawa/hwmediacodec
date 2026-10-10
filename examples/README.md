# hwmediacodec examples

Small, complete programs that show how to use `hwmediacodec` for everyday
jobs. They live in their own Go module (`github.com/shibukawa/hwmediacodec/examples`)
so that the core library does not depend on Ebitengine and pion, which the
game and streaming samples pull in. The reusable parts are packages of the
core module: [`mediacontainer/mp4`](../mediacontainer/mp4) and
[`net/hls`](../net/hls), [`capture`](../capture),
[`image/heif`](../image/heif) and [`image/avif`](../image/avif), and the
WebRTC broadcaster is the [`net/webrtc`](../net/webrtc) module. Inside the
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
go run github.com/shibukawa/hwmediacodec/examples/imgconv@latest photo.heic photo.png                          # HEIC or AVIF to PNG or JPEG
go run github.com/shibukawa/hwmediacodec/examples/imgconv@latest -quality 0.8 picture.png picture.heic         # PNG or JPEG to HEIC
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
| [`hls/`](hls/) | The fireworks show streamed live to browsers as fMP4 HLS (`mp4.Segmenter` + `hls.Playlist` from the core module); opens the player page |
| [`player/`](player/) | The video player: plays the bundled clip or any MP4/raw stream in a window of the video's aspect ratio, with pause, seeking and a progress bar |
| [`texture/`](texture/) | Video as a texture in Ebitengine: flat, on a spinning box with `DrawTriangles`, and through a gentle Kage shader; plays the bundled clip by default |
| [`webrtc/`](webrtc/) | The fireworks show streamed to browsers over WebRTC with pion, about 100 ms of latency; opens the player page |
| [`imgconv/`](imgconv/) | HEIC and AVIF still images converted to and from PNG/JPEG with the core module's `image/heif` and `image/avif` packages, falling back to cgo-free codecs where the machine has no hardware one |

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
- `mp4.Segmenter` cuts the packets into fMP4 segments for
  `hls.Playlist` (`hls`).
- `webrtc.Broadcaster` (the `net/webrtc` module) hands each access unit to
  a pion track (`webrtc`).

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
segment, each starting at a keyframe) and served from memory by
`hls.Playlist` (`net/hls` in the core module, a sliding-window
playlist with `#EXT-X-MAP` and `#EXT-X-MEDIA-SEQUENCE`). The sample itself
only adds the player page at `/`, which plays natively in Safari and
through hls.js (MSE) elsewhere;
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
`WithLowLatency`, Baseline profile and no B-frames into a
`webrtc.Broadcaster` from the [`net/webrtc`](../net/webrtc) module (see the
main README), which hands every access unit to a
[pion](https://github.com/pion/webrtc) track per viewer. The sample adds
the player page at `/`; the page POSTs its SDP offer to `/offer`, where
the broadcaster answers.

Measured in a Chromium browser on the same machine at 1280x720: jitter
buffer delay about 8 ms, decode about 1.3 ms per frame, no packet loss;
the end-to-end delay is dominated by the encoder's low-latency pipeline and
the display, a few frames in total. The module's tests use pion as the
viewer: access units received over the loopback RTP path are rebuilt with
pion's sample builder, compared NAL unit by NAL unit with what was sent,
and decoded by ffmpeg to the source's frame checksums; a PLI from the
viewer must reach the keyframe callback.

## imgconv

```sh
cd examples
go run ./imgconv photo.heic photo.png              # iPhone photos: HEVC tiles in a grid, irot
go run ./imgconv -quality 0.8 picture.png picture.heic
go run ./imgconv -tile 512 -rotate 90 picture.jpg picture.heic
go run ./imgconv picture.png picture.avif          # AV1: hardware where there is an encoder, else the fallback
go run ./imgconv picture.avif picture.png
go run ./imgconv -engine go picture.png picture.heic   # force the cgo-free codecs
go run ./imgconv -info photo.heic                  # file structure only, no decoder
```

A converter between HEIC/AVIF and PNG/JPEG. The hardware codecs do the
work through the core module's [`image/heif`](../image/heif) and
[`image/avif`](../image/avif) packages (see the main README). Those
packages return an error that wraps `hwmediacodec.ErrUnsupported` on a
machine without a hardware codec for the format, and the sample shows what
to do with it: `-engine auto` (the default) then switches to a codec that
needs neither hardware nor cgo and says so on standard error.

| Format | Fallback | What it is |
| --- | --- | --- |
| HEIC | [`github.com/gen2brain/h265`](https://github.com/gen2brain/h265) | An HEVC and HEIC codec written in Go (decoding and intra encoding) |
| AVIF | [`github.com/gen2brain/avif`](https://github.com/gen2brain/avif) | libavif with dav1d and libaom compiled to WebAssembly, run by wazero; no cgo, but not Go code |

- On an Apple Silicon Mac the fallback is what writes AVIF, since no chip
  there has an AV1 encoder; reading AVIF and both directions of HEIC stay
  on the hardware. `-engine hardware` and `-engine go` force one side.
- Nothing reaches the output file until one encoder has succeeded.
- The fallback encoders store no rotation, so `-rotate` turns the pixels
  instead of writing `irot`; viewers show the same picture. `-tile` is
  passed to the HEIC fallback and ignored by the AVIF one.
- The AVIF fallback is told to write 4:2:0, which hardware AV1 decoders
  read (its default 4:4:4 is refused by VideoToolbox, for one).
- The files cross over: the tests let the hardware decoders read what the
  fallback encoders wrote and the fallback HEIC decoder read what the
  hardware encoder wrote, a rotated grid included.

## Testing

```sh
cd examples
go test ./...
```

The hls and webrtc tests only check the player pages. 
The convert and thumbnails tests also need a hardware codec and skip
otherwise; they check codec, frame count, PSNR against the source, copied
audio and identical presentation times. (The MP4, HLS and HEIF tests moved
to the core module with their packages.) The imgconv tests run the fallback
codecs everywhere and compare them with the hardware ones where the machine
has them. The
texture sample's mesh (projection, culling, texture coordinates) has a unit
test and the player opens the bundled clip through a seek; the rendering
was checked by recording the window.
