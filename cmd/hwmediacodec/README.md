# hwmediacodec command

The command-line tool of hwmediacodec, a module of its own
(`github.com/shibukawa/hwmediacodec/cmd/hwmediacodec`) so that the core
module stays free of Ebitengine and pion. `probe` lists the hardware codecs
with the GPU behind each, `decode`, `encode`, `transcode`, `decode-audio`
and `encode-audio` exercise the API on files (see the
[main README](../../README.md)), and `check` is the project's official
check.

## check

```sh
go run github.com/shibukawa/hwmediacodec/cmd/hwmediacodec@latest check
go run ./cmd/hwmediacodec check                      # inside a clone: the whole run in a window
go run ./cmd/hwmediacodec check -auto                # the automatic checks only, no window (servers, CI)
go run ./cmd/hwmediacodec check -playback=false      # leave a stage out; -webrtc=false likewise
```

One command for people who want to report how the library does on their
machine. It runs three stages in one window and ends with a report:

1. **Automatic**: `Probe`, then every check once per backend that reports
   the codec (with `HWMEDIACODEC_BACKENDS` / `HWMEDIACODEC_AUDIO_BACKENDS`
   set to it, so a Windows machine with an NVIDIA card checks NVDEC/NVENC
   and Media Foundation separately). Decode: the bundled clip in H.264,
   HEVC and AV1, all 293 frames, which must come out in display order, at
   the track's size and not flat. Encode: 60 pictures of the clip (halved
   to 352x640) at 4 Mbit/s in H.264, HEVC and AV1, decoded back and
   compared by luma PSNR with the source (a working encoder reaches
   30 dB and more on this hard-to-code footage; the check also compares
   each picture with the next one, which a stream in the wrong order
   would match as well). Audio: the clip's AAC track, and two seconds of
   a soft chord through the AAC and Opus encoders and back.
2. **Playback**: the clip plays with sound in every codec the machine
   decodes, and the window asks whether picture and sound were right
   (Y or N, S skips).
3. **WebRTC**: the fireworks show is encoded in H.264 and HEVC
   (`webrtc.NewCodecBroadcaster`) and streamed to a page that opens in the
   default browser. The page reports the frames it decoded (from
   `getStats`) and asks whether the picture is right; a browser that
   cannot receive HEVC over WebRTC gets a refusal, recorded as such. The
   results are named after the browser ("HEVC picture in Edge 156 /
   nvidia"); opening the page URL in a second browser adds rows of its
   own, since Chrome and Edge on one machine can receive differently.
   Playback and WebRTC results also name the backend the library picks
   for them, the first one `Probe` lists for the codec.

The report is printed, saved as `hwmediacodec-report.md` and comes with a
link (`O` in the window) that opens a new GitHub issue with the title
(platform, backends, counts) and the report filled in; nothing is sent
until it is submitted there. A report longer than a link can carry (about
8 KB) is left out of it and pasted from the file. The report holds the
library and Go versions, the platform, the `Probe` table with the GPU (and
on Windows the Media Foundation transform) behind each entry, every result
and the user agent of each browser; the GPUs `Probe` named head the
report, and only the driver version is left for the reporter to fill in.
The exit status is 1 when a check failed. On an Apple M3 every check
passes (AV1 encode is reported as unsupported: Apple Silicon has no AV1
encoder).

The window is built on Windows, macOS and Linux (no cgo needed on any of
them). On Android, iOS and the other systems the command is built without
Ebitengine and `check` runs the automatic checks only; the
[mobile sample](../../examples/mobile) has the window there.

## Packages

| Directory | What it holds |
| --- | --- |
| [`checker/`](checker/) | The automatic checks and the report (results, Markdown, GitHub issue link); no Ebitengine, so it builds everywhere |
| [`checker/playback/`](checker/playback/) | The playback stage without its window, through `ebitenvideo`; the window here and the mobile sample drive it |
| [`assets/`](assets/) | The bundled sample clip (a ten-second portrait waterfall shot by the author, 720x1280 with AAC), embedded so the check and the players run from anywhere: as HEVC (1.4 MB) and, for decoders without HEVC, as H.264 (2.5 MB); `assets.Clips()` lists them in the order to try. `assets.AV1()` is the same clip in AV1 (1.2 MB) for the check |
| [`fireworks/`](fireworks/) | The scene the check streams over WebRTC and the `record`, `hls` and `webrtc` samples capture: a fireworks show over water with a Kage post-process, shells launched from the keyboard, now and then a gopher-shaped one |

The check mode of [examples/mobile](../../examples/mobile) uses `checker`,
`checker/playback` and `assets` as well, and the samples in
[examples](../../examples) play the clip in `assets`.
