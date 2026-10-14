//go:build darwin

package ebitenvideo

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"runtime"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2/audio"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
	"github.com/shibukawa/hwmediacodec/playback"
)

func requireHardware(t *testing.T) {
	t.Helper()
	if runtime.GOARCH != "arm64" {
		t.Skip("hardware decode tests target Apple Silicon")
	}
	if !hwmediacodec.HasHardware(context.Background(), hwmediacodec.H264, hwmediacodec.Decode) {
		t.Skip("no hardware decoder on this machine")
	}
}

// TestPlayerUploadsFrames drives a Player with Update ticks, the way a
// game does: the picture appears in an image of the stream's size, the end
// is reported, and seeking brings the player back. The decoding and the
// timing are tested in package playback.
func TestPlayerUploadsFrames(t *testing.T) {
	requireHardware(t)
	s := testutil.GenerateStream(t, hwmediacodec.H264, 160, 120, 30)
	data := testutil.ReadFile(t, s.Path)
	p, err := NewPlayer(bytes.NewReader(data), hwmediacodec.H264, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Update(); err != nil {
		t.Fatal(err)
	}
	if p.Image() != nil {
		t.Fatal("a paused player showed a frame")
	}
	p.Play()
	run := func(ticks int, until func() bool) {
		t.Helper()
		for i := 0; i < ticks && !until(); i++ {
			if err := p.Update(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	// Default TPS is 60, so the 30 frames at 30 fps need 60 ticks plus
	// decoding latency.
	run(600, p.Ended)
	if !p.Ended() || p.IsPlaying() {
		t.Fatalf("player did not end (err %v, position %v)", p.Err(), p.Position())
	}
	if w, h := p.Size(); w != 160 || h != 120 || p.Image() == nil {
		t.Fatalf("size %dx%d image %v", w, h, p.Image())
	}
	if b := p.Image().Bounds(); b.Dx() != 160 || b.Dy() != 120 {
		t.Fatalf("image bounds %v", b)
	}
	if !p.Seekable() {
		t.Error("a bytes.Reader source is not seekable")
	}
	if err := p.Seek(500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if p.Ended() || !p.IsPlaying() {
		t.Fatal("seek did not resume an ended player")
	}
	run(600, func() bool { return p.Position() > 500*time.Millisecond })
	if pos := p.Position(); pos <= 500*time.Millisecond || pos > 700*time.Millisecond {
		t.Errorf("position %v after the seek resumed, want just above 500ms", pos)
	}
	// The first seek scanned the raw stream, so its length is known now.
	if p.Length() != time.Second {
		t.Errorf("Length %v, want 1s", p.Length())
	}

	np, err := NewPlayer(bytes.NewBuffer(data), hwmediacodec.H264, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer np.Close()
	if np.Seekable() || !errors.Is(np.Seek(time.Second), ErrNotSeekable) {
		t.Error("a non-seekable reader accepted Seek")
	}
	if _, err := NewPlayer(bytes.NewBuffer(data), hwmediacodec.H264, 30, WithLoop()); err == nil {
		t.Error("looping over a non-seekable reader was accepted")
	}
}

// TestPlayerWithSound opens a film with its audio track. A test has no
// game loop, so Ebitengine's audio never comes up: the player holds the
// first picture for a moment and then plays on the clock of Update, which
// is what a browser does before the first click. That the pictures follow
// a running device is tested in package playback, with a device the test
// controls.
func TestPlayerWithSound(t *testing.T) {
	requireHardware(t)
	ffmpeg := testutil.RequireFFmpeg(t)
	if !testutil.HasEncoder(t, "libx264") {
		t.Skip("ffmpeg has no libx264")
	}
	out := testutil.TempDir(t) + "/movie.mp4"
	cmd := testutil.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100",
		"-frames:v", "30", "-pix_fmt", "yuv420p", "-c:v", "libx264", "-preset", "veryfast", "-g", "15",
		"-c:a", "aac", "-b:a", "96k", "-ac", "1", "-shortest", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}
	d, err := mp4.NewDemuxer(bytes.NewReader(testutil.ReadFile(t, out)))
	if err != nil {
		t.Fatal(err)
	}
	if d.Video() == nil || d.Audio() == nil {
		t.Fatal("the test movie lacks a track")
	}
	p, err := NewPlayerFromSource(d.Video().PacketSource(), WithAudio(d.Audio().PacketSource()))
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasAudio() {
		p.Close()
		t.Skipf("no audio decoder: %v", p.AudioErr())
	}
	if p.Volume() != 1 {
		t.Errorf("volume %g, want 1", p.Volume())
	}
	p.SetVolume(0.25)
	if p.Volume() != 0.25 {
		t.Errorf("volume %g after SetVolume(0.25)", p.Volume())
	}
	p.Play()
	run := func(ticks int, until func() bool) {
		t.Helper()
		for i := 0; i < ticks && !until(); i++ {
			if err := p.Update(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	// Thirty ticks of waiting for the device, then a second of film.
	run(25, func() bool { return false })
	if p.Position() != 0 {
		t.Errorf("position %v while the player waits for the audio device", p.Position())
	}
	run(600, p.Ended)
	if !p.Ended() || p.Image() == nil {
		t.Fatalf("the player did not end (position %v, error %v)", p.Position(), p.Err())
	}
	if err := p.Seek(500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	run(600, func() bool { return p.Position() > 600*time.Millisecond })
	if pos := p.Position(); pos <= 600*time.Millisecond || pos > 800*time.Millisecond {
		t.Errorf("position %v after the seek resumed", pos)
	}
	if err := p.AudioErr(); err != nil {
		t.Errorf("audio error: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// A player without WithAudio has no sound to steer.
	q, err := NewPlayerFromSource(d.Video().PacketSource())
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if q.HasAudio() || q.Volume() != 0 || q.AudioErr() != nil {
		t.Errorf("a player without sound: audio %v, volume %g, error %v", q.HasAudio(), q.Volume(), q.AudioErr())
	}
	q.SetVolume(1)
}

// TestSoundThroughResampler reads a player's audio stream the way
// Ebitengine does when the game's audio context has another sample rate:
// through Ebitengine's resampler, which fetches the stream in blocks and
// seeks it to block boundaries after a jump. What comes out must be the
// track's signal at the new rate, in place, from the start and after a
// jump.
func TestSoundThroughResampler(t *testing.T) {
	requireHardware(t)
	ffmpeg := testutil.RequireFFmpeg(t)
	if !testutil.HasEncoder(t, "libx264") {
		t.Skip("ffmpeg has no libx264")
	}
	const from, to, channels = 48000, 44100, 2
	pcm := testutil.AudioSignal(from, channels, from*2)
	dir := testutil.TempDir(t)
	testutil.WriteFile(t, dir+"/in.pcm", testutil.PCMBytes(pcm))
	cmd := testutil.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=30",
		"-f", "s16le", "-ar", "48000", "-ac", "2", "-i", dir+"/in.pcm",
		"-frames:v", "60", "-pix_fmt", "yuv420p", "-c:v", "libx264", "-preset", "veryfast", "-g", "15",
		"-c:a", "aac", "-b:a", "160k", dir+"/movie.mp4")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}
	d, err := mp4.NewDemuxer(bytes.NewReader(testutil.ReadFile(t, dir+"/movie.mp4")))
	if err != nil {
		t.Fatal(err)
	}
	core, err := playback.New(d.Video().PacketSource(), append(rgba(nil), playback.WithAudio(d.Audio().PacketSource()))...)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	stream := core.Audio()
	if stream == nil {
		t.Skipf("no audio decoder: %v", core.AudioErr())
	}
	if stream.SampleRate() != from || stream.Channels() != 2 || stream.Format() != hwmediacodec.F32 {
		t.Fatalf("stream: %d Hz, %d channels, %s", stream.SampleRate(), stream.Channels(), stream.Format())
	}
	resampled := audio.ResampleReaderF32(stream, 0, from, to)
	seeker, ok := resampled.(io.Seeker)
	if !ok {
		t.Fatal("the resampled stream cannot seek")
	}
	// The signal at the new rate, by linear interpolation: close enough
	// for a signal this low to tell a shift of a sample.
	want := func(sample int) []int16 {
		out := make([]int16, channels)
		x := float64(sample) * from / to
		i := int(x)
		for c := range out {
			a, b := float64(pcm[i*channels+c]), float64(pcm[(i+1)*channels+c])
			out[c] = int16(a + (b-a)*(x-float64(i)))
		}
		return out
	}
	read := func(first, n int) (got, ref []int16) {
		t.Helper()
		buf := make([]byte, n*channels*4)
		if _, err := io.ReadFull(resampled, buf); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n*channels; i++ {
			v := math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
			got = append(got, int16(max(-32768, min(32767, v*32768))))
		}
		for i := range n {
			ref = append(ref, want(first+i)...)
		}
		return got, ref
	}
	got, ref := read(0, to/2)
	if shift, snr := testutil.AudioAlignment(ref, got, channels, 8); shift != 0 || snr < 20 {
		t.Errorf("from the start: best SNR %.1f dB at a shift of %d samples", snr, shift)
	}
	// A jump to 1.2 s, as Player.SetPosition makes it: in bytes of the
	// resampled stream.
	const target = to * 6 / 5
	if _, err := seeker.Seek(target*channels*4, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, ref = read(target, to/4)
	const settle = 512
	if shift, snr := testutil.AudioAlignment(ref[settle*channels:], got[settle*channels:], channels, 8); shift != 0 || snr < 20 {
		t.Errorf("after a jump: best SNR %.1f dB at a shift of %d samples", snr, shift)
	}
	if err := core.AudioErr(); err != nil {
		t.Errorf("audio error: %v", err)
	}
}
