# hwmediacodec guide

The API in detail. The [README](README.md) has the overview and the
status of each platform; the `REPORT_*.md` files have what each backend
does on its platform ([macOS](REPORT_MACOS.md), [iOS](REPORT_IOS.md),
[Android](REPORT_ANDROID.md), [Linux](REPORT_LINUX.md),
[Windows](REPORT_WINDOWS.md), [browsers](REPORT_BROWSER.md)).

- [Codec API](#codec-api)
- [Readers, writers and pipelines](#readers-writers-and-pipelines)
- [Audio](#audio)
- [MP4 files](#mp4-files)
- [WebM files](#webm-files)
- [Live HLS](#live-hls)
- [WebRTC](#webrtc)
- [Screen recording](#screen-recording)
- [HEIC and AVIF images](#heic-and-avif-images)
- [Playback](#playback)
- [Ebitengine](#ebitengine)
- [Command-line tool](#command-line-tool)
- [Environment variables](#environment-variables)

## Codec API

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
		return err // errors.Is(err, hwmediacodec.ErrAgain): Receive first, then resend
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
`WithLowLatency` for live streaming. Encoder controls: target bitrate with
VBR or CBR, constant quality, keyframe interval, forced keyframes, B-frames
on/off, low-latency mode, profile, and in-band VPS/SPS/PPS in front of every
keyframe. Which of them a backend honours is in its report.

Pixel formats and frame order:

```go
dec, err := hwmediacodec.NewDecoder(ctx, hwmediacodec.H264,
	hwmediacodec.WithOutputFormat(hwmediacodec.RGBA)) // display order by default
...
f, err := dec.Receive(ctx)
img.WritePixels(f.Planes[0]) // *ebiten.Image of f.Width x f.Height; rows are 4*Width bytes
f.Release()

enc, err := hwmediacodec.NewEncoder(ctx, hwmediacodec.H264, w, h,
	hwmediacodec.WithInputFormat(hwmediacodec.RGBA), hwmediacodec.WithFrameRate(60))
pix := make([]byte, 4*w*h)
screen.ReadPixels(pix)
enc.Send(ctx, &hwmediacodec.Frame{Width: w, Height: h, Format: hwmediacodec.RGBA,
	Planes: [][]byte{pix}, Strides: []int{4 * w}, PTS: pts})
```

`PixelFormat.PlaneCount`, `PlaneLayout` and `FrameSize` describe the plane
layout of each format.

### Display order

Decoded frames come back in display order: the slice headers are parsed in
Go to derive picture order counts, and frames are held back no longer than
the stream's reorder bound. `WithDecodeOrder` switches back to the order the
hardware produces, which is what a transcoder that keeps the original
timestamps wants. (AV1 temporal units already arrive in presentation order
and yield one frame each, so neither applies to them.) On Windows the
display order comes from the Media Foundation decoder itself, and on Linux
with Intel VPL from the VPL runtime, so `WithDecodeOrder` has no effect
there.

The reorder bound comes from the stream. H.264 streams whose SPS has no VUI
`bitstream_restriction` (VideoToolbox's own encoder writes none) are held
back by the DPB size the level allows (4 frames at 1080p level 4.0, 7 at
320x240), as the standard requires, unless they signal that they cannot
reorder (`pic_order_cnt_type` 2, intra-only, constrained profiles).
Latency-sensitive callers decoding such streams should use
`WithDecodeOrder`. HEVC `pic_output_flag` 0 is not interpreted.

### RGBA and BGRA

`WithOutputFormat(hwmediacodec.RGBA)` (or `BGRA`) returns packed 8-bit
pixels with rows of exactly `4*Width` bytes, ready for
`ebiten.Image.WritePixels` or an `image.RGBA`; `WithInputFormat` accepts the
same formats for encoding, which is what `ebiten.Image.ReadPixels` produces.
Alpha is 255 on output and ignored on input.

- Where the engine converts by itself (VideoToolbox, the browser's
  `copyTo`, NVENC for input) it does so; see the reports.
- Otherwise the library converts in Go (`internal/pixconv`, rows split
  across cores, about 0.8 ms per 1080p frame on an Apple M3). Encoder
  input is converted with BT.709 in video range, chroma averaged over 2x2
  pixels, and the backend is asked to tag the stream as BT.709. Decoder
  output follows the stream's VUI, or for AV1 the sequence header's colour
  configuration (BT.601, BT.709 or BT.2020, video or full range); a stream
  that names no matrix gets BT.709 when the picture is wider than 704 or
  taller than 576 pixels and BT.601 otherwise, which is where VideoToolbox
  draws the line, so the colours are the same on every backend. Every
  chroma sample colours its 2x2 block of pixels (no chroma interpolation),
  and the frames live in Go memory.

### Packets

- H.264 and HEVC input and output are Annex-B, one access unit per
  `Packet`; use `annexb.Reader` to split a raw elementary stream.
- AV1 input is one temporal unit per `Packet` in the low-overhead OBU
  format (what an IVF frame or an ISOBMFF `av01` sample holds, with or
  without temporal delimiters); use `ivf.Reader` to split an IVF file.
  Every unit yields exactly one frame, in presentation order and with the
  packet's PTS. Decoding starts, and resumes after `Flush`, at a unit with
  a shown key frame; the sequence header is kept and handed to the decoder
  again when such a unit does not carry one. AV1 encoders produce temporal
  units without temporal delimiters, each key frame preceded by the
  sequence header (the form IVF, MP4 and WebM files store).
- The codec API does not parse containers; `mediacontainer/mp4` reads and
  writes MP4 files around it (see [MP4 files](#mp4-files)) and
  `mediacontainer/webm` WebM files with AV1 and Opus (see
  [WebM files](#webm-files)). Other Matroska codecs and TS are not
  covered, and AVCC/HVCC output is not offered by the encoders themselves.

## Readers, writers and pipelines

The Send/Receive loops above are written once in the root package, around
four small interfaces that the other packages implement:

| Interface | Role | Implemented by |
| --- | --- | --- |
| `PacketReader`, `PacketSeeker` | coded pictures in decode order, with a time scale; seeking by keyframe | `mp4.PacketSource`, `webm.PacketSource`, your own reader over `annexb.Reader` or `ivf.Reader`; consumed by `DecodeReader` and `playback` |
| `PacketWriter`, `PacketWriteCloser` | where encoded packets go | `mp4.VideoWriter`, `mp4.VideoFile`, `mp4.Segmenter`, `webm.VideoWriter`, `webm.AudioWriter`, `webrtc.Broadcaster`, `PacketWriterFunc` |
| `FrameReader` | raw frames out | `DecodeReader` (a `Decoder` fed from a `PacketReader`) |
| `FrameWriter` | raw frames in | `EncodeWriter` (an `Encoder` drained into a `PacketWriter`) |

A transcoder is the four of them in a row:

```go
src := video.PacketSource()                                   // *mp4.VideoTrack
dec, _ := hwmediacodec.NewDecoder(ctx, src.Codec(), hwmediacodec.WithTimeScale(src.TimeScale()))
enc, _ := hwmediacodec.NewEncoder(ctx, hwmediacodec.HEVC, w, h,
	hwmediacodec.WithTimeScale(src.TimeScale()), hwmediacodec.WithBitrate(6_000_000))
out := hwmediacodec.NewEncodeWriter(enc, track)               // track: *mp4.VideoWriter

n, err := hwmediacodec.CopyFrames(ctx, out, hwmediacodec.NewDecodeReader(dec, src))
err = out.Flush(ctx)                                          // the frames the encoder still holds
```

- `DecodeReader.ReadFrame` returns one frame at a time (release it when
  done) and `io.EOF` at the end; after `PacketSeeker.SeekKeyframe`, call
  `Reset` to drop what the decoder still holds.
- `EncodeWriter.WriteFrame` encodes a frame and writes the packets that
  became ready; `Flush` ends the stream.
- `CopyFrames` moves frames between any `FrameReader` and `FrameWriter`,
  so a filter (a scaler, an overlay) is a type that implements one of them
  around the other; `examples/convert` opens its encoder that way, for the
  size of the first decoded picture.
- `HasHardware(ctx, codec, direction)` tells in advance whether `NewDecoder`
  or `NewEncoder` can succeed without the software fallback.
- `Frame.RGBAImage()` copies an RGBA or BGRA frame into an `*image.RGBA`,
  and `RGBAFrame(img, pts)` wraps an image as encoder input without
  copying.
- `annexb.FromLengthPrefixed`, `AppendLengthPrefixed` and `AppendUnit`
  convert between Annex-B and the length-prefixed samples of MP4 and HEIF
  (AVCC, HVCC).

## Audio

`NewAudioDecoder` and `NewAudioEncoder` are the audio counterparts of the
video constructors, with the same Send/Receive/Flush loop. Packets are the
same `Packet` type; samples travel in an `AudioFrame`, which is plain Go
memory (interleaved 16-bit or float samples in `Data`) and needs no
`Release`. The codecs are the ones the operating system or the browser
ships, which run in software everywhere (`Probe` lists them with
`Hardware` false); what each platform has is in its report.

```go
// Decode the audio track of a file.
d, _ := mp4.Open("movie.mp4")
a := d.Audio()                                  // AAC or Opus; nil when there is none
dec, _ := hwmediacodec.NewAudioDecoder(ctx, a.Format())
src := a.PacketSource()
for {
	p, err := src.ReadPacket()
	if err == io.EOF { break }
	dec.Send(ctx, p)
	f, _ := dec.Receive(ctx)                    // one frame per packet
	play(f.Int16s(), f.SampleRate, f.Channels)  // interleaved samples
}

// Encode PCM and write it next to a video track.
enc, _ := hwmediacodec.NewAudioEncoder(ctx, hwmediacodec.AAC, 48000, 2,
	hwmediacodec.WithBitrate(128_000))
m, _ := mp4.Create("out.mp4")
aw, _ := m.AddAudioTrack(enc.Format(), 48000)   // time scale: the sample rate
enc.Send(ctx, hwmediacodec.S16Frame(samples, 48000, 2, 0))
for {
	p, err := enc.Receive(ctx)
	if errors.Is(err, hwmediacodec.ErrAgain) { break }
	aw.WritePacket(p)
}
enc.Flush(ctx)                                  // then Receive until io.EOF
m.Close()
```

- **`AudioFormat`** describes a compressed stream: codec, sample rate,
  channels, the codec configuration (`Config`: the AudioSpecificConfig of
  AAC, the `OpusHead` of Opus) and the encoder's delay. Containers report
  it (`mp4.AudioTrack.Format`, `adts.Reader.Format`), an encoder reports
  the one of its output (`AudioEncoder.Format`), and decoders and muxers
  take it. For a stream from elsewhere it is enough to fill in the codec,
  the sample rate and the channel count; an AAC decoder then assumes
  AAC-LC.
- **Packets** are raw: one AAC access unit without ADTS header, one Opus
  packet. `encoding/adts` adds and removes the ADTS header for `.aac`
  files.
- **Time.** An audio decoder hands each packet's PTS on to its frame.
  An encoder counts PTS and `Packet.Duration` in samples (the time scale
  defaults to the sample rate; `WithTimeScale` changes it), takes the PTS
  of the first frame as the start and treats everything sent after it as
  one continuous signal, in frames of any length. A frame longer than the
  encoder's output queue (a minute of PCM in one `Send`) is encoded in
  parts: `Send` stops once the queue is full and `Receive` encodes the
  rest as it hands packets out, so the queue stays bounded while the
  caller holds the frame; `Flush` encodes whatever is left at once.
- **Delay.** Encoders start early: the first packets of a stream code
  samples that are not part of the recording (2112 for AudioToolbox's AAC,
  312 for its Opus; `AudioFormat.Delay`), and the last packet is padded to
  full length. Timestamps account for this: a stream whose first sample
  is at 0 starts with a packet at PTS `-Delay`, and the last packet's
  `Duration` ends at the last real sample. The decoder returns every
  sample a packet codes, so a player drops what lies before the start of
  the presentation and after its end. The MP4 muxer records both in the
  track's edit list and sample table; ffmpeg then plays the samples that
  went in from the first one on (for AAC to the last one exactly; for Opus
  it also plays the padding of the last packet, as it does with files of
  its own). Backends that do not report their encoder's delay (MediaCodec,
  libfdk-aac, Media Foundation, WebCodecs) have it measured: the first
  encoder for a configuration encodes a short signal, decodes it and looks
  for it in the result.
- **Flush** resets a decoder (after a seek) and ends a signal on an
  encoder; both can be used again afterwards. After a jump a decoder
  needs a moment to run in: `mp4.AudioPacketSource.SeekKeyframe`
  positions itself one packet (AAC) or 80 ms (Opus) before the target for
  that reason, and the samples up to the target are to be dropped.
- **Channels** come out in the order of a WAV file (left, right, centre,
  LFE, surrounds), up to 5.1. Encoding takes mono and stereo.
- **Sample rate.** The decoder returns the stream's rate (48 kHz for Opus,
  whatever the recording had) and the encoder takes what it is given
  (Opus: 48 kHz only). The library does not resample.
- **No device.** The library neither plays nor records sound: it stops at
  PCM, like the video side stops at pixels. `playback` lines the samples
  up with the pictures for a device to play, and `ebitenvideo` plays them
  through Ebitengine (see [Sound](#sound)).

### AAC and licences

AAC is covered by patents that are licensed per unit to whoever ships an
encoder or decoder. This library ships neither: it contains no AAC code
(or Opus code) and bundles no codec library. It calls the codec that is
part of the operating system, whose vendor is the licensee, in the same
way it calls the system's H.264 and HEVC engines; that the codec runs on
the CPU rather than in hardware makes no difference to that. For the same
reason there is no pure Go fallback for AAC and there will not be one.
Opus is royalty-free and the safe choice where a codec has to be picked.
On Linux, where no vendor ships a codec with the system, the AAC backends
only ever use a library that is already installed (libavcodec, libfdk-aac):
whoever put it there, the distribution or the user, is the one who shipped
the codec, and a machine without either has no AAC. A program that wants
AAC on Linux should say which packages it needs rather than carry the
libraries along. This is how the situation is commonly understood, not
legal advice; a product that ships commercially should have it checked.

## MP4 files

The codec API stops at the elementary stream: packets in, frames out,
Annex-B on both ends. Files need a container, and
`github.com/shibukawa/hwmediacodec/mediacontainer/mp4` is the bridge for
MP4 and MOV, built on the box parser of
[mp4ff](https://github.com/Eyevinn/mp4ff) (the one dependency this package
adds; programs that do not import it do not link it):

```go
d, _ := mp4.Open("movie.mp4")
v := d.Video()                      // codec, size, time scale, sample table
pkt, _ := v.Packet(i)               // Annex-B access unit with SPS/PPS in front of keyframes
dec, _ := hwmediacodec.NewDecoder(ctx, v.Codec, hwmediacodec.WithTimeScale(int32(v.TimeScale)))
dec.Send(ctx, pkt)                  // pkt.PTS is the MP4 sample time

m, _ := mp4.Create("out.mp4")
vw, _ := m.AddVideoTrack(hwmediacodec.HEVC, v.TimeScale)
vw.WritePacket(p)                   // p from Encoder.Receive: Annex-B, PTS, DTS, Keyframe
aw := m.AddPassthroughTrack(d.Others()[0])
aw.WriteSample(s)                   // audio copied as is
m.Close()                           // writes moov
```

`d.Audio()` is the audio track for decoding (AAC in `mp4a`, Opus) and
`m.AddAudioTrack(enc.Format(), timeScale)` the track for an audio
encoder's packets; see [Audio](#audio).

What the package takes care of:

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
- The demuxer applies an empty edit in front of a track the same way:
  the wait is taken off `MediaTimeOffset`, so the times of every packet
  source count from the start of the presentation and two tracks of a
  file line up. The tracks of one `Demuxer` may be read from different
  goroutines.
- An audio track's edit list starts at the encoder's delay, so players
  begin at the first real sample, and the last sample is as short as the
  signal's end makes it. The tracks written from packets share a clock:
  when one starts later than the other (sound that begins half a second
  into the picture), it gets an empty edit of that length in front.
  Tracks copied with `AddPassthroughTrack` keep the edit list of their
  source, rescaled to the output's movie time scale (QuickTime files
  count theirs in 600ths of a second, ffmpeg's in the audio sample rate;
  `Demuxer.MovieTimeScale` reports the source's).
- Progressive output only (`ftyp`, `mdat`, `moov` at the end), `stco` or
  `co64` as the size requires, one chunk per run of samples of the same
  track. Fragmented input is rejected.
- `VideoTrack.PacketSource()` hands out access units with presentation
  times and seeks by the sync-sample table, which is what
  `playback.New` and `ebitenvideo.NewPlayerFromSource` take; `VideoTrack.ElementaryStream()`
  is the same track as a raw Annex-B stream.
- `CreateVideoFile` is the one-track file for encoder output (a
  `hwmediacodec.PacketWriteCloser`), and `Segmenter` cuts encoder packets into CMAF/fMP4
  segments for live HLS: an init segment, then one `moof`+`mdat` per
  segment, each starting at a keyframe. It needs PTS == DTS, so encode
  without B-frames. A stream whose keyframes stop coming is an error
  (after four segment lengths, or `MaxSegmentBytes` of held samples)
  rather than unbounded memory.
- Files are untrusted input. The sample tables are checked against the
  file and a track with more than `MaxSamples` (8 million, about 37 hours
  at 60 fps) is refused, because the index costs 24 bytes per sample
  whatever the file's size; lower it for a program that opens files from
  anywhere. The reader may start at any offset (an MP4 embedded in another
  file); offsets are taken from there.

## WebM files

`github.com/shibukawa/hwmediacodec/mediacontainer/webm` reads and writes
WebM, the Matroska subset of the web, for the pair every browser records
and plays: AV1 video (`V_AV1`) and Opus audio (`A_OPUS`). It is written
from the Matroska and WebM specifications without a dependency; VP8, VP9
and Vorbis tracks are listed by `Demuxer.Tracks` but not read.

```go
d, _ := webm.Open("movie.webm")
v := d.Video()                      // AV1, size, PacketSource with keyframe seeking
a := d.Audio()                      // Opus: Format() for NewAudioDecoder, PacketSource
p, _ := ebitenvideo.NewPlayerFromSource(v.PacketSource(), ebitenvideo.WithAudio(a.PacketSource()))

m, _ := webm.Create("out.webm")
vw, _ := m.AddVideoTrack(hwmediacodec.AV1, timeScale) // configuration from the first keyframe's sequence header
aw, _ := m.AddAudioTrack(enc.Format(), 48000)         // packets from an Opus AudioEncoder
vw.WritePacket(p)
aw.WritePacket(q)
m.Close()                           // Cues, SeekHead, duration, Segment size
```

Opus needs no Ogg container here: a block holds one Opus packet as the
encoder gives it, the OpusHead goes into the track's `CodecPrivate`, the
encoder's delay into `CodecDelay` and the padding of the last packet into
`DiscardPadding`, so ffmpeg and browsers start and stop at the real
samples. (A `.opus`/`.ogg` file is a different container that this
library does not write.)

What the package takes care of:

- The demuxer indexes every AV1 and Opus frame when it opens the file
  instead of trusting `Cues`, so the files a browser's `MediaRecorder`
  leaves behind (Segment and Clusters of unknown size, no Cues, no
  duration) open and seek like any other, and a file cut off while it
  was being written opens with the frames it has. Xiph, EBML and fixed
  lacing are read. Files are untrusted input: element sizes are checked
  against their parents and the file, and more than `MaxFrames` (8
  million) frames are refused.
- Video packets carry the block timestamps (milliseconds in nearly every
  file, `TimeScale` 1000). A keyframe whose block lacks the sequence
  header gets the one from the track's `av1C` in front, so a decoder can
  start at any keyframe.
- Audio packets are in samples at 48 kHz on the presentation's time line:
  the first packet starts the codec delay before 0, as an encoder's
  packets do, and `Duration` leaves the `DiscardPadding` out. Seeking
  lands the track's `SeekPreRoll` (80 ms) early, so drop the samples
  before the target.
- The muxer writes temporal delimiters out of the AV1 blocks, starts a
  Cluster at every video keyframe (every 5 s for audio only), puts a
  CuePoint on each and interleaves the tracks by time: a track that runs
  more than `InterleaveWindow` (1 s) ahead of another is written anyway.
  The output must be seekable. The video comes from an AV1 encoder (VA-API
  on Linux) or from a remux (an `mp4.VideoTrack` with AV1, an IVF file,
  another WebM file).

The tests generate files with ffmpeg (SVT-AV1 and libopus, also in its
live mode with unknown sizes) and compare every packet with ffprobe,
remux them and check that ffmpeg decodes the copy to the same pictures,
decode the AV1 of a written file bit-exactly against libdav1d, and take
Opus from the system's encoder through a WebM file to ffmpeg's decoder
and back.

## Live HLS

`github.com/shibukawa/hwmediacodec/net/hls` serves the fMP4 segments of
`mp4.Segmenter` as a live HLS stream. A `Playlist` keeps a sliding window
of them in memory (nothing is written to disk) and is an `http.Handler`
for the media playlist, the init segment and the media segments:

```go
playlist := hls.NewPlaylist(6, 2*time.Second)             // six segments of two seconds
seg, _ := mp4.NewSegmenter(hwmediacodec.H264, timeScale, 2*time.Second, playlist.SetInit, playlist.Add)
// seg.WritePacket(p) for every encoder packet (a hwmediacodec.PacketWriteCloser)
http.Handle("/live/", playlist)                            // players open /live/index.m3u8
// at the end
seg.Close()
playlist.End()                                             // EXT-X-ENDLIST
```

- Set the encoder's keyframe interval to the segment length (segments are
  cut at keyframes) and encode without B-frames (`WithLowLatency`).
- The handler answers GET and HEAD by the last path element (`index.m3u8`,
  `init.mp4`, `seg_N.m4s`), so it can be mounted under any prefix; it
  answers 503 until the first segment exists and sends
  `Access-Control-Allow-Origin: *`. Segments are cacheable only for as
  long as the window lists them, so a restarted stream (whose sequence
  numbers start over) is not served stale segments from a browser cache.
- The stream is unauthenticated: anyone who can reach the handler can
  watch. Serve it on a loopback or trusted interface (the samples listen on
  `127.0.0.1` unless told otherwise) or behind the application's own
  authentication.
- Safari plays the stream natively; other browsers need hls.js or another
  MSE player (`examples/hls` has such a page). HEVC plays in Safari only.
  Latency is a few seconds, which is what plain HLS gives;
  `net/webrtc` is the low-latency path.

## WebRTC

`github.com/shibukawa/hwmediacodec/net/webrtc` is the low-latency
counterpart of `net/hls`: a `Broadcaster` hands every access unit of an
H.264 stream (or, with `NewCodecBroadcaster`, an HEVC one) to a
[pion](https://github.com/pion/webrtc) track per viewer.
It is a module of its own, so that the WebRTC stack stays out of the core
module's dependencies (`go get github.com/shibukawa/hwmediacodec/net/webrtc`).

```go
var rec *capture.Recorder
bc := webrtc.NewBroadcaster(60, nil, func() { rec.RequestKeyframe() }) // nil: no STUN/TURN, LAN only
rec, _ = capture.New(w, h, bc, capture.Options{
	FPS: 60, LowLatency: true, Profile: hwmediacodec.ProfileBaseline})
http.Handle("/offer", bc)   // the page POSTs its SDP offer and gets the answer
```

- `Broadcaster` is a `hwmediacodec.PacketWriteCloser`; packet times
  are in 90 kHz units. pion's payloader splits the Annex-B NAL units
  into RTP (STAP-A for the parameter sets, FU-A for large slices; the
  RFC 7798 aggregation and fragmentation units for HEVC).
- `NewCodecBroadcaster(hwmediacodec.HEVC, ...)` streams HEVC. Browsers
  receive it where their platform decodes HEVC (Safari, and Chrome and
  Edge with a hardware decoder; Chromium 152 on an Apple M3 does). On
  Windows with a GeForce RTX 2060 Max-Q, Chrome 156 shows the HEVC stream
  and Edge on the same machine does not (2026-10-30); H.264 works in both.
  Edge decodes HEVC only with the paid "HEVC Video Extensions" Store
  package installed (according to caniuse; without it its offer has no
  H.265), while Chrome reaches the GPU's decoder by itself. Chrome on
  Linux has not been seen to show an HEVC stream yet (see the
  [Linux report](REPORT_LINUX.md#status)). An offer without H.265 is refused with `ErrBadOffer`,
  which `ServeHTTP` answers with 400.
- Signalling is a single HTTP POST of the browser's SDP offer as JSON; the
  answer is returned once ICE gathering is done, so no trickle ICE and no
  WebSocket. `Accept` does the same for programs with their own signalling.
- A viewer joining or reporting a picture loss calls the keyframe callback,
  and a viewer only starts receiving at a keyframe. Requests closer
  together than half a second are merged, so a viewer flooding the stream
  with picture loss indications cannot turn it into keyframes only. Encode
  with `WithLowLatency`, Baseline profile and no B-frames.
- Each viewer is served by a goroutine of its own from a bounded queue, so
  a slow network stalls neither the encoder nor the other viewers; a viewer
  that falls about a second behind is dropped. `MaxViewers` (32 by
  default) caps the connections, counting those still being set up;
  `ServeHTTP` answers 503 beyond it or after `Close`, 400 for an offer it
  cannot answer, and drops a connection whose request is cancelled during
  ICE gathering (`AcceptContext`). The endpoint itself is unauthenticated,
  like `net/hls`: serve it on a trusted interface or behind the
  application's authentication.
- `examples/webrtc` has a player page; measured there in a Chromium browser
  on the same machine at 1280x720, the jitter buffer delay was about 8 ms.

## Screen recording

`github.com/shibukawa/hwmediacodec/capture` is a package of the core
module that records what a game or any other renderer draws. A `Recorder`
reads the pixels of a `Source` on every frame, encodes them on a
background goroutine and pushes the packets into a
`hwmediacodec.PacketWriteCloser`:

```go
rec, err := capture.New(1280, 720, sink, capture.Options{FPS: 60, Bitrate: 8_000_000})

func (g *game) Draw(screen *ebiten.Image) {
	g.scene.Draw(screen)
	rec.Capture(screen)      // ReadPixels, then the encoder runs on its own goroutine
}
// on exit
rec.Close()                  // flushes the encoder, closes the sink
```

- A `Source` is `Bounds()` plus `ReadPixels([]byte)` (tightly packed
  RGBA). `*ebiten.Image` satisfies it as it is, but the package does not
  import Ebitengine: anything that can hand out RGBA pixels can be
  recorded.
- `Capture` never blocks on the encoder. `ReadPixels` runs on the calling
  goroutine (about 2 ms at 720p for an Ebitengine screen on an M3; the GPU
  has to finish the frame first, so a heavy scene shows up in this
  number); the buffer then goes through a bounded queue to the encoder,
  and when the queue is full the frame is dropped and counted (`Dropped`).
- PTS come from the wall clock quantised to the frame rate, in units of
  `capture.TimeScale` (90 kHz), so a dropped frame leaves a gap instead
  of speeding the recording up, and a 120 Hz display showing a 60 fps game
  does not record every frame twice. `CaptureAt` takes the time stamp
  explicitly.
- The sink is any `hwmediacodec.PacketWriteCloser`
  (`WritePacket(hwmediacodec.Packet)` plus `Close`);
  `hwmediacodec.PacketWriterFunc` adapts a closure. The packets are Annex-B access
  units with in-band parameter sets, so writing `Packet.Data` to a file
  gives a playable elementary stream. `mediacontainer/mp4` has an MP4
  file sink (`CreateVideoFile`) and an fMP4 segmenter for HLS, and
  `net/webrtc` a sink that feeds a pion track.
- `Options` selects the codec, bitrate or quality, keyframe interval, low
  latency and profile; `Extra` appends any other `EncoderOption`.
  `RequestKeyframe` forces a keyframe on the next captured frame (a new
  viewer joining a live stream).

## HEIC and AVIF images

`github.com/shibukawa/hwmediacodec/image/heif` (HEIC: HEVC pictures) and
`github.com/shibukawa/hwmediacodec/image/avif` (AVIF: AV1 pictures) read
and write HEIF still images with the hardware codecs. They follow the
conventions of `image/jpeg` and `image/png`, and importing one registers
its format with the standard `image` package:

```go
import (
	_ "github.com/shibukawa/hwmediacodec/image/avif"
	_ "github.com/shibukawa/hwmediacodec/image/heif"
)

img, format, err := image.Decode(file)         // format: "heic", "avif" or "heif"
cfg, _, err := image.DecodeConfig(file)        // size after rotation; nothing is decoded
```

Both packages have the same functions:

```go
img, err := heif.Decode(r)                     // image.Image (an *image.RGBA)
err = heif.Encode(w, img, nil)                 // HEIC with the encoder's default quality
err = heif.Encode(w, img, &heif.Options{Quality: 0.8, TileSize: 512, Rotation: 90})

rgba, info, err := heif.DecodeBytes(data, hwmediacodec.WithSoftwareFallback())
info, err = heif.DecodeInfo(data)              // size, tiles, rotation; no decoder needed

heif.MaxPixels = 4096 * 4096                   // bounds for every decode, as http.DefaultMaxHeaderBytes
rgba, info, err = heif.DecodeBytesWithLimits(data, heif.Limits{MaxPixels: 8192 * 8192})
rgba, info, err = heif.DecodeWithLimits(file, heif.Limits{MaxTiles: 64}, hwmediacodec.WithSoftwareFallback())
```

- Each package reads only its own codec and says so when handed the
  other's file. A file whose major brand is the generic `mif1` does not
  name its codec: `image.Decode` reports it as `"heif"` and decodes it when
  the package for its pictures is imported.
- Decoding needs a hardware HEVC or AV1 decoder and encoding a hardware
  encoder; the error wraps `hwmediacodec.ErrUnsupported` on a machine
  without one (no Apple Silicon chip encodes AV1, so `avif.Encode` fails
  there). The packages have no software codec of their own;
  `examples/imgconv` shows how to fall back to cgo-free ones.
  `DecodeConfig` and `DecodeInfo` read only the file structure and work
  everywhere. `Decode` and `DecodeConfig` given a reader that can seek
  (an `*os.File`) read the headers and then only the items they need; any
  other reader is read into memory up to `MaxInputBytes`.
- Limits: a HEIF file declares its picture and grid sizes itself, and its
  item table may point every grid cell at one coded tile, so a few hundred
  bytes can describe a 16 GiB canvas stitched from 65,000 decodes.
  Decoding refuses a file beyond `MaxPixels` (16384 x 16384), `MaxTiles`
  (4096) or `MaxInputBytes` (256 MiB) with an error wrapping `ErrTooLarge`
  before allocating the canvas or opening a decoder; the defaults are
  libavif's and libheif's. They are package variables because the decoders
  `image.Decode` calls take no options; `DecodeWithLimits` and
  `DecodeBytesWithLimits` take a `Limits` for one call. `DecodeConfig` and
  `DecodeInfo` still report the declared size, as `image/png` does.
- The result is opaque RGBA: alpha planes are neither read nor written,
  and only 8-bit 4:2:0 pictures are handled (iPhone HDR photos are 10-bit
  and out of scope).
- Colours: the decoder converts to RGB with what the bitstream declares.
  An HEVC stream that declares nothing (some software encoders leave the
  VUI out and describe the colours only in the item's `colr` property) is
  converted from NV12 in Go with the `colr` matrix and range; otherwise a
  full-range picture would come out with stretched contrast.

A HEIF file is an ISOBMFF `meta` box of items (coded pictures, a `grid`
that tiles them) with properties (`hvcC`/`av1C` decoder configuration,
`ispe` size, `irot`, `imir`, `clap`, `colr`, `pixi`) and an `mdat` with the
coded data. The two packages share the code that parses and writes it
(mp4ff only supplies the `hvcC`/`av1C` record parsers) and leave the
pictures to the codecs:

- **Decode**: for each coded item, `hvcC`'s parameter sets plus the
  length-prefixed NAL units become one Annex-B packet (AV1: the temporal
  unit, with the sequence header from `av1C` if the item lacks one); all
  items go through one decoder opened with `WithOutputFormat(RGBA)` and
  `WithDecodeOrder()`, grids are stitched tile by tile and cropped to the
  grid size, then `clap`, `irot` and `imir` are applied in their stored
  order.
- **Encode**: one keyframe per picture or tile (`WithKeyframeInterval(1)`,
  `ForceKeyframe`, `WithQuality`), VPS/SPS/PPS from the packet into `hvcC`,
  the slices as the item data. `TileSize` writes a grid like phone cameras
  do; `Rotation` stores an `irot`. Odd sizes are padded by a replicated
  row or column and declared through `clap`, because the hardware encoders
  work on even 4:2:0 pictures (VideoToolbox rounds an odd request down to
  320x202 for 321x203).
- **AVIF**: decoding works wherever `Probe` lists an AV1 decoder (M3 and
  newer Macs, recent GPUs on Linux and Windows); encoding needs an AV1
  encoder, which only the VA-API backend drives so far. `avif.Encode`
  through it has not been verified: on an AMD Radeon 890M, `imgconv` fell
  back to its software encoder for a 720x1280 picture.

Tests use macOS ImageIO (`sips`) as the reference for the files this
package writes (single, grid, rotated, odd sizes) and for HEIC input, and
ffmpeg for AVIF input and for the rotation direction (ffmpeg maps `irot` to
a display matrix and autorotates; ImageIO keeps it as orientation
metadata). ImageIO resamples `clap`-cropped pictures instead of cropping,
and ffmpeg rounds odd `clap` sizes to even, so odd pictures are compared
with the source instead.

## Playback

`github.com/shibukawa/hwmediacodec/playback` plays a stream against a
clock the caller drives. It decodes on a background goroutine, a few frames
ahead, in display order and as RGBA, and returns the frame that is due;
where the picture goes is up to the caller (a texture, a window, an image):

```go
p, err := playback.New(track.PacketSource(), playback.WithLoop()) // *mp4.VideoTrack
p.Play()

// once per tick of the display or game loop:
f, err := p.Advance(dt)          // dt: the time since the previous call
if f != nil {
	show(f.Planes[0], f.Width, f.Height) // RGBA, f.Strides[0] bytes per row
	f.Release()
}
```

- `New` takes any `hwmediacodec.PacketReader`, which carries the
  presentation times; a `PacketSeeker` (a keyframe index, as the MP4
  demuxer's `VideoTrack.PacketSource()`) gives the player `Seek`, `Length`
  and looping.
- `NewStream(r, codec, fps)` plays a raw Annex-B elementary stream.
  Elementary streams carry no timestamps, so the frame rate is a
  parameter; an `io.ReadSeeker` gets seeking too, by scanning the stream
  once for keyframes on the first seek.
- Frames are skipped when decoding or the caller falls behind
  (`Skipped` counts them). `Seek(t)` restarts decoding at the keyframe
  before `t` and drops the frames up to it, so the next picture is the one
  at `t`; `Position` reports `t` meanwhile.
- `WithOutputFormat(NV12)` hands out the decoder's native format instead,
  for renderers that convert in a shader.

### Sound

`WithAudio` gives the player the audio track that goes with the pictures.
The package still opens no device: the sound comes out as a stream of
samples for one to play, and the device tells the player how far it has
got.

```go
p, err := playback.New(video.PacketSource(),           // *mp4.VideoTrack
	playback.WithAudio(audio.PacketSource()),          // *mp4.AudioTrack
	playback.WithAudioChannels(2))
stream := p.Audio()                                    // nil: no decoder for the track, see p.AudioErr()
dev := newDevice(stream, stream.SampleRate())          // yours: plays the stream, implements playback.AudioDevice
p.AttachAudioDevice(dev)
p.Play()
// Advance(dt) as before: the frame returned is the one for the sample at the loudspeaker.
```

- **`AudioStream`** is an `io.ReadSeeker` of interleaved samples (float by
  default, 16-bit with `WithAudioFormat`; the track's channels, or mono or
  stereo with `WithAudioChannels`) laid out on the time line of the
  pictures: the byte at offset n belongs to the time n / bytes per second.
  The encoder's delay and padding are cut off, a track that starts later
  or ends earlier than the pictures is silence there, and with `WithLoop`
  the stream goes on with the next pass behind the end of the presentation
  (one pass is as long as the longer track). It never ends and never
  blocks for long: samples the decoder does not have in time are silence.
- **`AudioDevice`** is five methods around whatever plays the stream:
  `Ready`, `Position` (the time of the sample at the loudspeaker: bytes
  read less bytes still buffered), `SetPosition` (drop the buffer, seek the
  stream), `Play` and `Pause`.
- **The sound is the clock.** With a device attached, `Advance` shows the
  picture for the device's position and ignores its argument; a late
  picture is skipped, the sound is never touched. `Play`, `Pause` and
  `Seek` steer the device along. After a start or a jump the device waits
  (up to half a second) until the first picture is in, so the two start
  together.
- **No device, no sound, still a film.** While the device is not ready (a
  browser does not let a page make sound before the first click) the
  player holds the first picture for half a second and then runs on the
  clock of `Advance` as it does without sound; when the device comes up it
  is placed where the pictures are and takes over. A platform without an
  audio backend, or a decoder that gives up, leaves the pictures playing:
  `Audio` is nil or silent and `AudioErr` says why.
- The position a device reports usually leaves out the latency of the
  operating system's output (some tens of milliseconds, more over
  Bluetooth), so the sound trails the picture by that much, which is the
  direction in which it is noticed least.

## Ebitengine

`github.com/shibukawa/hwmediacodec/ebitenvideo` is a separate Go module in
this repository (its own `go.mod`) that depends on both `hwmediacodec` and
Ebitengine; the core module never imports Ebitengine, so users without a
game engine do not pull it in. It is `playback` driven from the game loop,
with the current frame in an `*ebiten.Image`:

```go
player, err := ebitenvideo.NewPlayer(file, hwmediacodec.H264, 30, ebitenvideo.WithLoop())
player.Play()

func (g *game) Update() error { return player.Update() }   // one tick per call
func (g *game) Draw(screen *ebiten.Image) {
	if img := player.Image(); img != nil {                   // updated in place
		screen.DrawImage(img, nil)
	}
}
```

`NewPlayer` is `playback.NewStream` and `NewPlayerFromSource` is
`playback.New`; the options and the methods (`Seek`, `Position`, `Length`,
`Skipped`, ...) are those of the playback package. One `Update` stands for
1/TPS seconds, or for the wall-clock time since the previous one when the
TPS is `SyncWithFPS`.

With `ebitenvideo.WithAudio` the film has its sound, played through
Ebitengine's audio, and the pictures follow it (see [Sound](#sound)):

```go
d, _ := mp4.Open("movie.mp4")
player, err := ebitenvideo.NewPlayerFromSource(d.Video().PacketSource(),
	ebitenvideo.WithAudio(d.Audio().PacketSource()), ebitenvideo.WithLoop())
player.SetVolume(0.8)
```

The player uses the game's audio context (`audio.CurrentContext`) and
creates one at the track's sample rate when the game has none; a track at
another rate goes through Ebitengine's resampler, and the sound is mixed
to stereo. `HasAudio`, `AudioErr`, `Volume` and `SetVolume` are the
additions to the player. A minimal example plays a raw stream in a window
(space pauses, the arrow keys seek); the MP4-capable player with a bundled
clip is `examples/player`:

```sh
cd ebitenvideo && go run ./example -codec h264 -fps 30 ../video.h264
cd examples && go run ./player
```

The same player runs in a browser: Ebitengine builds for `GOOS=js
GOARCH=wasm` and the frames come from WebCodecs (see the
[browser report](REPORT_BROWSER.md)).
[wasmserve](https://github.com/hajimehoshi/wasmserve) builds and serves it
on `http://localhost:8080/`:

```sh
cd examples && go run github.com/hajimehoshi/wasmserve@latest ./player
```

The player takes RGBA frames from the decoder there as everywhere else,
not the browser's own `VideoFrame`s (`hwmediacodec.Native`): Ebitengine
does not hand out its textures, so an upload straight into the player's
image would depend on its internals staying as they are. What is missing
is a way to give Ebitengine an image source for a texture.

## Command-line tool

`cmd/hwmediacodec` is a module of its own (so that the core module stays
free of Ebitengine and pion) that exercises the same API; see
[its README](cmd/hwmediacodec/README.md).

```sh
go run ./cmd/hwmediacodec probe
go run ./cmd/hwmediacodec check                                        # the official check: window, playback, WebRTC, report
go run ./cmd/hwmediacodec check -auto                                  # the automatic checks only, no window
go run ./cmd/hwmediacodec decode -hash input.h264                       # display order, NV12
go run ./cmd/hwmediacodec decode -format rgba -o out.rgba input.hevc -codec hevc
go run ./cmd/hwmediacodec decode -decode-order -hash input.h264
go run ./cmd/hwmediacodec decode -codec av1 -hash input.ivf                  # AV1: IVF input (needs a GPU with an AV1 decoder)
go run ./cmd/hwmediacodec encode -size 1920x1080 -bitrate 8M -gop 60 -o out.h264 input.nv12
go run ./cmd/hwmediacodec encode -size 1920x1080 -format rgba -o out.h264 input.rgba
go run ./cmd/hwmediacodec transcode -in h264 -codec hevc -bitrate 6M -o out.hevc input.h264
go run ./cmd/hwmediacodec transcode -in h264 -codec av1 -bitrate 4M -o out.ivf input.h264   # AV1: IVF output (VA-API)
go run ./cmd/hwmediacodec encode-audio -codec opus -o out.webm input.pcm         # Opus into WebM (decode-audio reads .webm too)
```

`transcode` re-encodes the frames in the order the decoder returns them
(decode order), so sources with B-frames come out with their pictures
reordered; `examples/convert` is the converter that keeps the
presentation order and the container.

## Environment variables

| Variable | Effect |
| --- | --- |
| `HWMEDIACODEC_BACKENDS` | Restricts and orders the video backends, as a comma-separated list of the names `Probe` reports: `vaapi` bypasses Intel VPL to exercise VA-API on the same GPU, `vpl,nvidia` prefers the integrated Intel GPU on a machine that also has an NVIDIA card. Unknown names are ignored |
| `HWMEDIACODEC_AUDIO_BACKENDS` | The same for the audio backends (on Linux `libopus`, `libavcodec`, `fdk-aac`): `libopus,fdk-aac` decodes AAC with the FDK library |
| `HWMEDIACODEC_VAAPI_DEVICE` | Render node path for VA-API on multi-GPU machines (default: the first one libva can initialise) |
| `HWMEDIACODEC_VPL_DEVICE` | Render node path for Intel VPL (default: the first Intel render node) |
| `HWMEDIACODEC_NVIDIA_DEVICE` | CUDA device ordinal for NVDEC / NVENC |
