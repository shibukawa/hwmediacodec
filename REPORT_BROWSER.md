# Browser report

Backend: **WebCodecs** (`internal/webcodecs`), driven through `syscall/js`
in a `GOOS=js GOARCH=wasm` build, the one Ebitengine makes for the web:
`VideoDecoder` fed with Annex-B access units (or AV1 temporal units) as
`EncodedVideoChunk`s without a description, `VideoEncoder` configured for
Annex-B output, frames in CPU memory through `VideoFrame.copyTo`, and
`AudioDecoder` / `AudioEncoder` for sound. It is the one codec API a page
has; the browser puts VideoToolbox, Media Foundation, VA-API or MediaCodec
behind it. Back to the [README](README.md).

## Status

| Browser | H.264 dec | H.264 enc | HEVC dec | HEVC enc | AV1 dec | AV1 enc |
| --- | --- | --- | --- | --- | --- | --- |
| Chrome (macOS) | ✅ | ✅ | ✅ | ✅ | ✅ | |
| Safari (macOS) | ✅ | ✅ | ✅ | ✅ | ✅ | |
| Firefox (macOS) | ✅ | ✅ | | | ⚠️ | |
| Chrome / Edge (Windows, Linux, Android) | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | |

| Browser | AAC dec | AAC enc | Opus dec | Opus enc |
| --- | --- | --- | --- | --- |
| Chrome (macOS) | ✅ | ✅ | ✅ | ✅ |
| Safari (macOS) | ✅ | ✅ | ✅ | ✅ |
| Firefox (macOS) | ✅ | | ✅ | ✅ |
| Other operating systems | ⚠️ | ⚠️ | ⚠️ | ⚠️ |

- **Chrome on an Apple M3**: the whole conformance suite passes, video and
  audio, bit-exact where the other backends are
  (`scripts/browser_test.sh`, see [CONTRIBUTING](CONTRIBUTING.md)).
- **Firefox** (146 and 157 on macOS) and **Safari** (macOS) run it with
  the limits listed below: in Firefox the NV12 decode tests skip and the
  colour and quality points show up as failures; in Safari the checksum
  tests give way to the fixture comparison, and the AV1 tests pass or skip
  with what Safari offers the page. Firefox decodes HEVC through WebCodecs
  only with `dom.media.webcodecs.h265.enabled` switched on, and has no AAC
  encoder.
- **Playback with sound** (2026-10-14): the audio stream tests of package
  `playback` pass in Chrome through the same script. The bundled clip was
  played with its sound in a real Ebitengine loop in headless Chrome
  (WebCodecs for both tracks, WebAudio for the output, muted): before the
  page had its first input event the film ran on the tick clock, and from
  the event on the clock was the audio player's position, through a seek
  and the loop, with one skipped frame in nine seconds.
- Chrome on Windows, Linux and Android has not been run.
- No browser encodes AV1 through this library (only the VA-API backend
  encodes AV1).

## Notes

- **Build.** `GOOS=js GOARCH=wasm go build` with the standard Go
  toolchain; nothing else is needed, and purego is not part of the build.
  TinyGo and GopherJS are untested, and the WASI targets have no way to
  the browser's API.
- **Where it runs.** WebCodecs needs a secure context (https, or
  `localhost` during development) and exists in windows and dedicated
  workers. Elsewhere (Node.js, an `http://` page) `Probe` returns an
  empty list and `NewDecoder` / `NewEncoder` report `ErrUnsupported`.
- **Hardware is the browser's word.** Configurations ask for
  `hardwareAcceleration: "prefer-hardware"`. Chrome treats that as a
  requirement: with accelerated video decoding switched off it reports
  nothing as supported, `Probe` is empty and the hardware tests skip
  (measured). The specification calls it a hint, and Firefox and Safari
  take it as one: they answer the same for every preference (Firefox says
  yes to "hardware" VP8, Safari to "software" HEVC), so
  `Capability.Hardware` means something in Chromium browsers only. `WithSoftwareFallback` asks for
  `"no-preference"` and lets the browser's software codecs in (Chrome has
  them for H.264 and AV1 decoding and H.264 encoding, not for HEVC).
- **One thread.** WebCodecs answers in callbacks on the page's event loop,
  and a WebAssembly program shares that thread: a callback runs only while
  every goroutine is blocked. `Send` blocks while the browser's codec is
  saturated and `Flush` until everything is delivered; `Receive` never
  waits and reports `ErrAgain` when nothing has arrived. The documented
  Send/Receive loop, `DecodeReader`, `EncodeWriter`, `playback` and
  `ebitenvideo` all block often enough. A loop that only polls `Receive`
  until a frame shows up never lets the browser deliver it.
- **Order.** WebCodecs decoders emit presentation order (Chrome does, from
  the picture order counts). The library still sorts H.264 and HEVC frames
  by picture order count itself, so the result does not depend on the
  engine or on the timestamps of the packets; `WithDecodeOrder` returns the
  browser's order.
- **RGBA and BGRA output** is converted by the browser
  (`copyTo({format})`), which is far cheaper than converting in
  WebAssembly. For a stream that names no colour matrix the library puts
  its own rule (BT.709 above 704x576, BT.601 otherwise) into the decoder
  configuration, because browsers guess differently per codec (Chrome
  showed an untagged 320x240 AV1 stream as BT.709). Chrome follows that
  configuration for AV1 and ignores it for H.264, where its own guess
  (BT.601 below 720 lines) agrees with the rule at the usual sizes. A
  browser whose `copyTo` does not convert gets the Go conversion instead.
- **10-bit and other pictures without an NV12 form** (4:2:2, 4:4:4) decode
  to RGBA or BGRA only; with NV12 output the stream is refused with
  `ErrUnsupported`. Frames the browser exposes without a pixel format
  (`VideoFrame.format` is `null`: 10-bit hardware frames in Chrome, and
  what some Android hardware decoders return) are read back through an
  `OffscreenCanvas` for RGB output.
- **Encoder.** NV12 frames are wrapped in `VideoFrame`s (RGBA and BGRA are
  converted in Go first); bitrate with `"variable"` or `"constant"` mode,
  `WithQuality` through the `"quantizer"` mode with one quantiser per
  frame, `WithLowLatency` as `latencyMode: "realtime"`, keyframes forced
  from Go at the exact frame count. The codec string carries the profile
  and a level that fits the picture size and frame rate; without
  `WithProfile`, H.264 tries High, Main and Constrained Baseline in that
  order. WebCodecs encoders do not reorder frames, so `WithBFrames`
  changes nothing. A browser that ignores the request for Annex-B output
  and returns length-prefixed NAL units with an `avcC` / `hvcC` record gets
  them converted (that path has unit tests but no browser that takes it
  was run).
- **Frames that stay on the GPU.** `WithOutputFormat(hwmediacodec.Native)`
  returns the decoder's own pictures instead of pixels: the frame has no
  `Planes`, and `Frame.Native()` is the WebCodecs `VideoFrame` (a
  `syscall/js` value) that WebGL (`texImage2D`), WebGPU and canvases take
  as an image source without the pixels leaving the GPU. `Release` closes
  it, and since a hardware decoder has few pictures, a frame should not
  be held for long (the decoder keeps four at most for `Receive`). The
  order is the display order as for every other format. How such a frame
  turns into colours is then the browser's business: for a stream that
  names no matrix Chrome takes BT.601 at SD sizes while Firefox and Safari
  take BT.709 (measured on 320x240 streams), where the pixel formats
  follow the library's one rule. The Ebitengine player does not use
  these frames; see [Ebitengine](GUIDE.md#ebitengine) for why.

## Firefox

Firefox (146 and 157 on macOS, measured 2026-10-13) differs from Chrome in
what it gives the page:

- Its decoders hand over `BGRX` frames, not YUV. RGBA and BGRA output
  work; NV12 output is `ErrUnsupported` at `Receive`, because turning RGB
  back into YUV would need the matrix Firefox converted with. A
  transcoder there decodes to RGBA and encodes from RGBA.
- It ignores the colour description in the decoder configuration for
  H.264: an untagged 320x240 stream comes out as BT.709 where every
  other backend shows BT.601. Tagged streams and HD are not affected by
  the rule.
- Its encoder does not write the colour description into the stream, so
  RGB input (converted with BT.709) is untagged; decoders then assume
  BT.601 for pictures up to 704x576, and those come out in slightly wrong
  colours. HD is assumed BT.709 and is fine.
- `WithQuality` is accepted and has no effect (the stream has the same
  size for every value), and `latencyMode: "realtime"` is refused at
  configure although `isConfigSupported` accepts it; the backend
  notices (it waits for the configuration to take) and configures
  without it.
- No HEVC through WebCodecs by default, although its `<video>` element
  plays HEVC: `VideoDecoder` takes it only with the preference
  `dom.media.webcodecs.h265.enabled` switched on in `about:config`
  (measured in 157: with it, HEVC decodes, again to `BGRX`). There is no
  HEVC encoder either way. The samples therefore bundle their clip in
  H.264 as well and fall back to it.

## Safari

Safari (macOS, measured 2026-10-13) works for playback and differs in
these ways:

- Decoded H.264 and HEVC frames are NV12 in a form of WebKit's own,
  whatever the stream says: widened to full range, and the chroma plane
  moved up a quarter line (every row is three quarters of itself and a
  quarter of the row below; measured against ffmpeg sample by sample).
  The backend undoes both, so a frame has the stream's range and chroma
  position again: the luma exactly, the chroma to within a step or two
  (54 dB against ffmpeg on the test pattern, 76 dB and more for luma,
  where the only differences are samples outside the video range that
  the widening clipped). That is not bit-exact, so in Safari the
  checksum tests give way to a test that compares the frames as Safari
  hands them over with a fixture in that form, made from ffmpeg's decode
  of the same B-frame streams: every sample of every frame is within one
  step, in display order. Its `copyTo` does not convert YUV frames to
  RGB (the option is ignored, which the backend detects), so RGBA and
  BGRA output are converted in Go from the restored frames (41 dB
  against ffmpeg's).
- Its H.264 encoder stops returning chunks from the fifth frame on in
  `latencyMode: "quality"`, flush included. The first encoder a page
  opens therefore proves its latency mode with eight frames, and the
  backend uses `"realtime"` where `"quality"` does not work (and the
  other way round, which covers Firefox); the result is remembered for
  the page. In a WebKit browser, recognised by its user agent string,
  H.264 starts with `"realtime"` so that the proof does not have to wait
  three seconds for the mode that stalls; the proof still decides. In
  that mode constant bitrate undershoots (592 kbit/s for a 1 Mbit/s
  target at 320x240) and `WithQuality` is `ErrUnsupported`.
- NV12 input without a colour space is taken for full-range samples, so
  every input frame now says video range. Its HEVC encoder still writes
  a full-range stream (it converts the samples itself); the picture is
  right, the range is not the one asked for.
- Like Firefox it writes no colour description, with the same effect on
  RGB input at SD sizes.
- AV1 decodes there, bit-exact with ffmpeg, when Safari offers it, but
  Safari does not offer it to every page: whether `isConfigSupported`
  says yes for AV1 is settled per page process and came out as no in
  about half of the page loads on the same machine, before the page had
  used any codec. The library reports what the browser says at that
  moment, so `Probe` may or may not list `av1 decode` in Safari. 10-bit
  AV1 has no output there (no NV12 form, and no RGB conversion in
  `copyTo`).
- It suspends the timers of a tab that is not visible, and with them
  every timeout of the Go program: run the tests with the Safari window
  in view (`HWMEDIACODEC_BROWSER=open HWMEDIACODEC_BROWSER_ARGS="-a
  Safari"`).

## Audio

`AudioDecoder` and `AudioEncoder` sit behind the same API and the same
timestamps as on the other platforms. AAC and Opus as the browser has
them: AAC-LC to 5.1 and Opus in Chrome, Firefox and Safari on macOS,
HE-AAC in Chrome and Safari; Opus (48 kHz) encoding in every browser, AAC-LC
encoding where the browser has an encoder (Chrome and Safari on macOS, not
Firefox). WebCodecs leaves a few things unsaid that the backend settles
itself:

- A decoder stamps its output on its own (the first chunk's timestamp
  plus the samples so far), so frames are matched to packets by order.
  Every packet still yields one frame with its PTS and all its samples.
  An Opus decoder that is given the identification header drops the
  pre-skip itself, so the header is kept from it.
- An encoder's chunks are stamped from 0, with no word about the
  samples the encoder starts early by. `NewAudioEncoder` therefore runs
  a trial the first time a configuration is asked for: a short signal is
  encoded, an Opus encoder's delay is read from the identification
  header it reports, and an AAC encoder's is measured by decoding the
  trial in the same page and looking for the signal (2112 samples in
  Chrome and Safari on macOS, the figure of AudioToolbox). The trial
  takes some tens of milliseconds and its result is kept for the page.
- The trial also shows whether the encoder works: Chrome's AAC encoder
  on macOS says yes to 64 kbit/s stereo at 48 kHz in `isConfigSupported`
  and then fails on the first packets, which the backend reports as
  `ErrUnsupported` from `NewAudioEncoder` (80 kbit/s and above work).
- A bitrate the browser's encoder does not offer is not refused: Chrome
  encodes at its default instead (32 kbit/s asked, 138 kbit/s made), and
  nothing in the API tells. Chrome's AAC encoder gives the same packets
  for VBR and CBR.
- **Safari** (27 on macOS): its AAC encoder ignores the bitrate in its
  variable mode, so a bitrate is passed with the constant mode there,
  which holds it on average. It replaces the first 1024 samples of
  every signal with silence; the trial notices, and that much silence
  is sent ahead of the signal (the delay it reports is 3136, not 2112).
  It hands out the stream description as a whole ES_Descriptor, from
  which the AudioSpecificConfig is taken. Its decoder returns AAC with
  more than two channels in the order of the bitstream (centre first),
  which is put into the order of a WAV file. Its Opus encoder runs in
  the mode for telephony, as AudioToolbox's does by default, and a page
  cannot change that: Opus encoded in Safari is good for speech and
  loses low frequencies of music (17 dB against the input where the
  other browsers reach 38 dB on the test signal).
- **Firefox** (157 on macOS) has no AAC encoder and refuses HE-AAC and
  4.0 AAC streams after saying yes to their configuration (an
  `ErrInvalidData` from a later call). Opus in both directions and
  AAC-LC decoding in mono, stereo, 3.0, 5.0 and 5.1 pass.
- HE-AAC in Chrome comes out 962 samples (20 ms) later than on macOS
  and in Safari: the decoder Chrome uses leaves the delay of the band
  replication in its output and AudioToolbox takes it out.
- Sample rates and channel counts the browser accepts for encoding
  beyond the library's own rules (Opus at 48 kHz, mono or stereo) are
  the browser's business; an odd rate may be resampled without notice.
