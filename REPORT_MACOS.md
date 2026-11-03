# macOS report

Backends: **VideoToolbox** (video) and **AudioToolbox** (`AudioConverter`,
audio), loaded through purego. `CGO_ENABLED=0 go build` works.
The same backends serve iOS; see the [iOS report](REPORT_IOS.md) for what
differs there. Back to the [README](README.md).

## Status

| Codec | Decode | Encode |
| --- | --- | --- |
| H.264 | ✅ | ✅ |
| HEVC | ✅ | ✅ |
| AV1 | ✅ M3 and newer | (no AV1 encoder exists on Apple Silicon) |
| AAC | ✅ | ✅ |
| Opus | ✅ | ✅ |

Verified on an Apple M3: `hwmediacodec check` passes every check (AV1
encode is reported as unsupported), the conformance tests are bit-exact
with ffmpeg, and the audio codecs are compared with ffmpeg in both
directions.

- Decode: H.264, HEVC; display order; NV12, RGBA or BGRA in CPU memory.
- Encode: H.264, HEVC; NV12, RGBA or BGRA in, Annex-B out.
- Intel Macs are out of scope; the VideoToolbox backend requires hardware
  engines unless `WithSoftwareFallback` is given.

## Performance (Apple M3)

- 1080p H.264 with B-frames decodes at roughly 780 frames per second to
  NV12 in display order, 350 to BGRA and 250 to RGBA, including the copy;
  1080p H.264 encodes at roughly 200 frames per second from NV12 and 180
  from RGBA or BGRA.
- 1080p AV1 (SVT-AV1 preset 10, 300 frames) decodes at roughly 820 frames
  per second to NV12, 310 to BGRA and 220 to RGBA, including the copy.

## VideoToolbox notes

- RGBA and BGRA conversion is done by VideoToolbox: RGBA output is BGRA
  swapped in Go (about 1 ms per 1080p frame), since VideoToolbox rejects
  RGBA as a destination. Untagged streams are converted with the matrix
  VideoToolbox assumes (BT.601 up to 704x576, BT.709 for larger pictures,
  measured on an M3); the other backends follow the same rule. RGB input
  is converted with BT.709 and the stream is tagged accordingly.
- The hardware decoder rejects very small pictures (64x48 fails with
  "decoder malfunction"; 96x64 works).
- The encoder rounds odd picture sizes down to even: a 321x203 request
  yields a 320x202 stream. Pad to even and crop at the consumer (as
  `image/heif` does with a `clap` property).
- `Encoder.Flush` blocks until VideoToolbox has emitted every pending frame;
  it does not observe context cancellation once the call has started.
- VideoToolbox numbers decode times from the first PTS when B-frames are
  on, so a reordered picture can carry DTS > PTS; the MP4 muxer shifts
  every DTS back (see [MP4 files](GUIDE.md#mp4-files)).
- VideoToolbox's own H.264 encoder writes no VUI `bitstream_restriction`,
  so decoders hold such streams back by the DPB size of the level (see
  [Display order](GUIDE.md#display-order)).

## AV1

AV1 decoding uses the hardware decoder Apple added with the M3
(`VTIsHardwareDecodeSupported('av01')`). `Probe` lists `av1 decode` only on
such machines, and `NewDecoder` reports `ErrUnsupported` on M1 and M2 Macs
even with `WithSoftwareFallback`: macOS 26 lists a "SW AV1 Decoder"
(`com.apple.videotoolbox.videodecoder.av1.sw`) in `VTCopyVideoDecoderList`,
but VideoToolbox refuses to create a session with it
(`kVTCouldNotFindVideoDecoderErr` with hardware decoding disabled, with or
without `VTRegisterSupplementalVideoDecoderIfAvailable`, which changes
nothing for AV1; measured on an M3). No Apple Silicon chip has an AV1
encoder, so `NewEncoder` with `AV1` reports `ErrUnsupported`.

- A `Packet` is one temporal unit in the low-overhead OBU format, which is
  what an IVF frame or an ISOBMFF `av01` sample holds; `ivf.Reader` splits
  an IVF file and `ivf.Writer` writes one. Temporal delimiter OBUs may be
  present or absent: VideoToolbox produced identical frames either way, so
  the unit is handed over as it is. The sequence header is parsed in Go
  (`internal/av1`) to build the `av1C` record that goes into the format
  description (`CMVideoFormatDescriptionCreate` with the
  `SampleDescriptionExtensionAtoms` extension); a new sequence header (for
  example a size change) starts a new decompression session, and after
  `Flush` decoding resumes at the next unit with a shown key frame.
- Every temporal unit yields exactly one frame, in presentation order and
  with the packet's PTS; hidden alternate reference frames come out when
  the stream shows them through `show_existing_frame`. The Go reorder layer
  is therefore not used for AV1 and `WithDecodeOrder` has no effect.
- Main profile streams decode, 8-bit and 10-bit 4:2:0; the output is 8-bit
  (NV12, RGBA or BGRA). 8-bit decoding is bit-exact with libdav1d. 10-bit
  pictures are converted by VideoToolbox itself, within 53.5 dB PSNR of
  ffmpeg's conversion (the two round differently). High and Professional
  profile (4:4:4, 4:2:2, 12-bit) and monochrome streams report
  `ErrUnsupported`. Untagged streams are converted to RGB with BT.601, as
  for the other codecs.

## Audio (AudioToolbox)

- AAC-LC from mono to 5.1, HE-AAC and HE-AAC v2 (with the band replication
  and parametric stereo, at the full sample rate); Opus mono and stereo,
  any packet length; 16-bit or float samples. Encoding: AAC-LC and Opus
  (48 kHz), mono and stereo; bitrate with VBR or CBR.
- The encoder's delay is 2112 samples for AAC and 312 for Opus.
- AudioToolbox's Opus encoder starts in its telephony mode (the Opus
  "VoIP" application), which filters the low end. The backend switches it
  to the mode for general audio: the test signal (two tones and a sweep
  from 200 Hz up) comes back with a signal-to-noise ratio of 17 dB in the
  default mode and of 36 dB in this one.
- HE-AAC is decoded with its extensions when the AudioSpecificConfig
  signals them (as the files of `afconvert` and of most encoders do). A
  stream that leaves them out (implicit signalling, as ADTS files do) is
  decoded as the AAC-LC core at half the sample rate.
- Only an edit list gives `AudioFormat.Delay` for a track. Files that
  record the delay in iTunes metadata alone (`iTunSMPB`, as `afconvert`
  writes) report 0.
