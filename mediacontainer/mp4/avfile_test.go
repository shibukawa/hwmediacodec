package mp4_test

import (
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/capture"
	"github.com/shibukawa/hwmediacodec/internal/mediatest"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// TestAVFileRecordsCaptureAndSound records what a camera app does: a
// capture.Recorder writes the pictures into AVFile.Video from its own
// goroutine while an AAC encoder writes the sound into AVFile.Audio from
// another. ffprobe must see one H.264 and one AAC stream, both starting at
// 0 and lasting 2 s, and the demuxer must read both tracks back.
func TestAVFileRecordsCaptureAndSound(t *testing.T) {
	mediatest.RequireFFmpeg(t)
	mediatest.RequireHardware(t, hwmediacodec.H264, hwmediacodec.Encode)
	const sampleRate, channels = 48000, 2
	enc := audioEncoderOrSkip(t, hwmediacodec.AAC, sampleRate, channels)
	out := filepath.Join(t.TempDir(), "rec.mp4")
	f, err := mp4.CreateAVFile(out, hwmediacodec.H264, capture.TimeScale, enc.Format(), sampleRate)
	if err != nil {
		t.Fatal(err)
	}
	if f.Video() != f.Video() || f.Audio() != f.Audio() {
		t.Error("Video and Audio must return the same writer on every call")
	}
	const w, h, fps, frames = 320, 240, 30, 60
	rec, err := capture.New(w, h, f.Video(), capture.Options{FPS: fps, Bitrate: 2_000_000, Queue: 64})
	if err != nil {
		t.Fatal(err)
	}

	// The sound: 2 s in 10 ms buffers, like a microphone callback, on
	// the same clock as the pictures (both count from 0).
	pcm := testutil.AudioSignal(sampleRate, channels, sampleRate*2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		mediatest.EncodeAudio(t, enc, pcm, sampleRate/100, 0, f.Audio())
	}()
	screen := newFakeScreen(w, h)
	start := time.Now()
	for i := range frames {
		screen.advance()
		rec.CaptureAt(screen, start.Add(time.Duration(i)*time.Second/fps))
	}
	wg.Wait()
	if err := rec.Close(); err != nil { // closes the video track
		t.Fatal(err)
	}
	if err := f.Audio().Close(); err != nil { // and this, the file
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("Close after both tracks: %v", err)
	}

	mediatest.CheckDecodes(t, out)
	vs, as := mediatest.VideoStream(t, out), mediatest.AudioStream(t, out)
	t.Logf("video %s from %.4f s for %.4f s, audio %s from %.4f s for %.4f s", vs.CodecName, vs.Start(), vs.Seconds(), as.CodecName, as.Start(), as.Seconds())
	if vs.CodecName != "h264" || vs.Width != w || vs.Height != h || vs.FrameCount() != frames {
		t.Errorf("video stream %s %dx%d %d frames; want h264 %dx%d %d frames", vs.CodecName, vs.Width, vs.Height, vs.FrameCount(), w, h, frames)
	}
	if as.CodecName != "aac" || as.Rate() != sampleRate || as.Channels != channels {
		t.Errorf("audio stream %s %d Hz %d channels; want aac %d Hz %d channels", as.CodecName, as.Rate(), as.Channels, sampleRate, channels)
	}
	if math.Abs(vs.Start()) > 0.0015 || math.Abs(as.Start()) > 0.0015 {
		t.Errorf("video starts at %.4f s and audio at %.4f s; want both at 0", vs.Start(), as.Start())
	}
	if math.Abs(vs.Seconds()-2) > 0.01 || math.Abs(as.Seconds()-2) > 0.002 {
		t.Errorf("video lasts %.4f s and audio %.4f s; want 2 s each", vs.Seconds(), as.Seconds())
	}

	in, err := mp4.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	v, a := in.Video(), in.Audio()
	if v == nil || a == nil {
		t.Fatalf("read back: video %v, audio %v", v != nil, a != nil)
	}
	if v.SampleCount() != frames {
		t.Errorf("read back %d pictures, want %d", v.SampleCount(), frames)
	}
	if got, want := a.Format(), enc.Format(); got.Codec != want.Codec || got.Delay != want.Delay || got.SampleRate != want.SampleRate {
		t.Errorf("audio format read back %+v, written %+v", got, want)
	}
	if d := a.PacketSource().Length(); d != 2*time.Second {
		t.Errorf("audio length read back %v, want 2 s", d)
	}
}

// TestAVFileClose checks when the file is finished: not when one track
// writer is closed, but when both are, or on Close. Writes after that fail.
func TestAVFileClose(t *testing.T) {
	mediatest.RequireFFmpeg(t)
	const sampleRate, channels = 48000, 2
	dir := t.TempDir()
	src, err := mp4.Open(mediatest.GenerateMP4(t, dir, mediatest.MP4Options{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 30, GOP: 15}))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	v := src.Video()
	pcm := testutil.AudioSignal(sampleRate, channels, sampleRate)

	for _, byTracks := range []bool{true, false} {
		name := "file"
		if byTracks {
			name = "track_writers"
		}
		t.Run(name, func(t *testing.T) {
			enc := audioEncoderOrSkip(t, hwmediacodec.AAC, sampleRate, channels)
			path := filepath.Join(dir, name+".mp4")
			f, err := mp4.CreateAVFile(path, v.Codec, v.TimeScale, enc.Format(), sampleRate)
			if err != nil {
				t.Fatal(err)
			}
			for i := range v.SampleCount() {
				p, err := v.Packet(i)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.WriteVideoPacket(p); err != nil {
					t.Fatal(err)
				}
			}
			if byTracks {
				// Twice is still one track; the audio track is still
				// open, so the file takes its packets below.
				for range 2 {
					if err := f.Video().Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
			mediatest.EncodeAudio(t, enc, pcm, 1024, 0, f.Audio())
			if byTracks {
				err = f.Audio().Close()
			} else {
				err = f.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := f.WriteVideoPacket(hwmediacodec.Packet{}); err == nil {
				t.Error("WriteVideoPacket after the file was finished succeeded")
			}
			if err := f.Close(); err != nil {
				t.Errorf("second Close: %v", err)
			}
			mediatest.CheckDecodes(t, path)
			if vs, as := mediatest.VideoStream(t, path), mediatest.AudioStream(t, path); vs.CodecName != "h264" || as.CodecName != "aac" {
				t.Errorf("streams %s and %s; want h264 and aac", vs.CodecName, as.CodecName)
			}
		})
	}
}

// TestCreateAVFileRejects checks that a format the muxer cannot write is
// refused up front.
func TestCreateAVFileRejects(t *testing.T) {
	dir := t.TempDir()
	if _, err := mp4.CreateAVFile(filepath.Join(dir, "a.mp4"), hwmediacodec.AV1, 90000, hwmediacodec.AudioFormat{Codec: hwmediacodec.AAC, SampleRate: 48000, Channels: 2}, 48000); err == nil {
		t.Error("AV1 video accepted")
	}
	if _, err := mp4.CreateAVFile(filepath.Join(dir, "b.mp4"), hwmediacodec.H264, 90000, hwmediacodec.AudioFormat{Codec: hwmediacodec.AAC}, 48000); err == nil {
		t.Error("audio without a sample rate accepted")
	}
}
