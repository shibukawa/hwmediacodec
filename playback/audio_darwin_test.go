//go:build darwin

package playback

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// fakeDevice plays an AudioStream like an audio device, on the test's
// clock instead of a sound card's: every run reads the samples of one
// deviceTick while it is playing, and its position is what it has read.
type fakeDevice struct {
	t        *testing.T
	stream   *AudioStream
	ready    bool
	playing  bool
	base     int64 // the byte offset the last SetPosition stands for
	consumed int64 // bytes read since then
	// heard is what was read, with the position each piece was read at.
	heard []heardAudio
	// sets records the SetPosition calls.
	sets []time.Duration
}

type heardAudio struct {
	offset int64 // on the stream
	data   []byte
}

func (d *fakeDevice) Ready() bool { return d.ready }

func (d *fakeDevice) Position() time.Duration { return d.timeOf(d.base + d.consumed) }

func (d *fakeDevice) timeOf(offset int64) time.Duration {
	return time.Duration(offset/int64(d.stream.frameBytes)) * time.Second / time.Duration(d.stream.SampleRate())
}

// offsetOf is the byte offset of the sample at or before a time.
func (d *fakeDevice) offsetOf(at time.Duration) int64 {
	return int64(at) * int64(d.stream.SampleRate()) / int64(time.Second) * int64(d.stream.frameBytes)
}

func (d *fakeDevice) SetPosition(at time.Duration) error {
	off := d.offsetOf(at)
	if _, err := d.stream.Seek(off, io.SeekStart); err != nil {
		return err
	}
	d.base, d.consumed = off, 0
	d.sets = append(d.sets, at)
	return nil
}

func (d *fakeDevice) Play()  { d.playing = true }
func (d *fakeDevice) Pause() { d.playing = false }

// run plays for one deviceTick.
func (d *fakeDevice) run(tick time.Duration) {
	if !d.ready || !d.playing {
		return
	}
	n := int(int64(tick)*int64(d.stream.SampleRate())/int64(time.Second)) * d.stream.frameBytes
	buf := make([]byte, n)
	for got := 0; got < n; {
		m, err := d.stream.Read(buf[got:])
		if err != nil {
			d.t.Fatalf("device read: %v", err)
		}
		got += m
	}
	d.heard = append(d.heard, heardAudio{offset: d.base + d.consumed, data: buf})
	d.consumed += int64(n)
}

// heardFrom joins the 16-bit samples the device read from the time at
// on, for as long as one piece follows the other on the stream.
func (d *fakeDevice) heardFrom(at time.Duration) []int16 {
	var out []int16
	next := d.offsetOf(at)
	for _, h := range d.heard {
		if h.offset != next {
			if len(out) > 0 {
				break
			}
			continue
		}
		for i := 0; i+1 < len(h.data); i += 2 {
			out = append(out, int16(binary.LittleEndian.Uint16(h.data[i:])))
		}
		next += int64(len(h.data))
	}
	return out
}

// movie makes an MP4 file with a 30 fps H.264 track with B-frames and an
// AAC track that carries pcm.
func movie(t *testing.T, pcm []int16, rate, channels, frames int) *mp4.Demuxer {
	t.Helper()
	ffmpeg := testutil.RequireFFmpeg(t)
	if !testutil.HasEncoder(t, "libx264") {
		t.Skip("ffmpeg has no libx264")
	}
	dir := testutil.TempDir(t)
	testutil.WriteFile(t, dir+"/in.pcm", testutil.PCMBytes(pcm))
	out := dir + "/movie.mp4"
	cmd := testutil.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=30",
		"-f", "s16le", "-ar", fmt.Sprint(rate), "-ac", fmt.Sprint(channels), "-i", dir+"/in.pcm",
		"-frames:v", fmt.Sprint(frames), "-pix_fmt", "yuv420p",
		"-c:v", "libx264", "-preset", "veryfast", "-bf", "2", "-g", "15",
		"-c:a", "aac", "-b:a", "128k", out)
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
	return d
}

const deviceTick = time.Second / 60

// TestPlayerFollowsAudioDevice plays a film with sound through a device
// the test controls: the pictures shown are the ones for the device's
// position, from the start, after a jump, around a pause and to the end,
// and the samples the device gets are the ones of that position.
func TestPlayerFollowsAudioDevice(t *testing.T) {
	requireHardware(t)
	const rate, channels, frames = 48000, 2, 90
	pcm := testutil.AudioSignal(rate, channels, rate*frames/30)
	d := movie(t, pcm, rate, channels, frames)
	v := d.Video()
	p, err := New(v.PacketSource(), WithAudio(d.Audio().PacketSource()), WithAudioFormat(hwmediacodec.S16))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Audio() == nil {
		t.Skipf("no audio decoder: %v", p.AudioErr())
	}
	if l := p.Length(); l < 2990*time.Millisecond || l > 3030*time.Millisecond {
		t.Errorf("length %v, want about 3 s", l)
	}
	dev := &fakeDevice{t: t, stream: p.Audio()}
	p.AttachAudioDevice(dev)
	p.Play()

	frameTime := func(f *hwmediacodec.Frame) time.Duration {
		return time.Duration(f.PTS) * time.Second / time.Duration(v.TimeScale)
	}
	// step plays one deviceTick and checks the picture against the device: it
	// is never ahead of the sound, and with the device running not more
	// than two frames and a deviceTick behind it.
	shown, last := 0, time.Duration(-1)
	step := func() {
		t.Helper()
		dev.run(deviceTick)
		f, err := p.Advance(deviceTick)
		if err != nil {
			t.Fatal(err)
		}
		if f != nil {
			ft, pos := frameTime(f), dev.Position()
			f.Release()
			shown++
			last = ft
			if dev.ready && dev.playing && (ft > pos+time.Millisecond || pos-ft > 2*time.Second/30+deviceTick) {
				t.Fatalf("the picture of %v is shown at the device's %v", ft, pos)
			}
		}
		time.Sleep(deviceTick) // the decoders work on their own time
	}

	// A device that is not there yet: the clock holds on the first
	// picture for a while.
	for i := 0; i < 12; i++ {
		step()
	}
	if p.Position() != 0 || dev.playing || len(dev.sets) != 0 {
		t.Fatalf("before the device is ready: position %v, device playing %v, placed %d times", p.Position(), dev.playing, len(dev.sets))
	}
	// It comes up: it is placed at the clock and started.
	dev.ready = true
	for i := 0; i < 60; i++ {
		step()
	}
	if len(dev.sets) != 1 || dev.sets[0] != 0 || !dev.playing {
		t.Fatalf("after the device came up: placed at %v, playing %v", dev.sets, dev.playing)
	}
	if pos := p.Position(); pos < 900*time.Millisecond || pos > 1050*time.Millisecond || shown < 25 {
		t.Fatalf("after 60 ticks of sound: position %v, %d pictures", pos, shown)
	}
	inPlace(t, "the sound from the start", pcm, dev.heardFrom(0), channels)

	// A pause stops the device and with it the clock.
	p.Pause()
	at := p.Position()
	for i := 0; i < 10; i++ {
		step()
	}
	if dev.playing || p.Position() != at {
		t.Fatalf("paused: device playing %v, position %v after %v", dev.playing, p.Position(), at)
	}
	p.Play()
	if !dev.playing {
		t.Fatal("Play did not start the device again")
	}

	// A jump: the device goes along, waits for the picture and goes on
	// from there with the sound of that place.
	const target = 2 * time.Second
	if err := p.Seek(target); err != nil {
		t.Fatal(err)
	}
	if dev.playing || len(dev.sets) != 2 || dev.sets[1] != target {
		t.Fatalf("after Seek: device playing %v, placed at %v", dev.playing, dev.sets)
	}
	last = -1
	for i := 0; i < 20 && last < 0; i++ {
		step()
	}
	if last < target-time.Second/30 || last > target+time.Second/30 || !dev.playing {
		t.Fatalf("after the jump the first picture is the one of %v, device playing %v", last, dev.playing)
	}
	for i := 0; i < 30; i++ {
		step()
	}
	heard := dev.heardFrom(target)
	first := int(target * rate / time.Second)
	inPlace(t, "the sound after the jump", pcm[(first+256)*channels:], heard[256*channels:], channels)

	// To the end: the last picture, then the device stops.
	for i := 0; i < 120 && !p.Ended(); i++ {
		step()
	}
	if !p.Ended() || dev.playing || p.IsPlaying() {
		t.Fatalf("at the end: ended %v, device playing %v (position %v)", p.Ended(), dev.playing, p.Position())
	}
	if last < 2900*time.Millisecond {
		t.Errorf("the last picture shown is the one of %v", last)
	}
	if err := p.AudioErr(); err != nil {
		t.Errorf("audio error: %v", err)
	}
}

// TestPlayerWithoutReadyDevice: a device that does not come up (a browser
// before the first click) holds the pictures back for a moment only; when
// it comes up later it joins where the pictures are.
func TestPlayerWithoutReadyDevice(t *testing.T) {
	requireHardware(t)
	const rate, channels, frames = 48000, 2, 90
	pcm := testutil.AudioSignal(rate, channels, rate*frames/30)
	d := movie(t, pcm, rate, channels, frames)
	p, err := New(d.Video().PacketSource(), WithAudio(d.Audio().PacketSource()), WithAudioFormat(hwmediacodec.S16))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Audio() == nil {
		t.Skipf("no audio decoder: %v", p.AudioErr())
	}
	dev := &fakeDevice{t: t, stream: p.Audio()}
	p.AttachAudioDevice(dev)
	p.Play()
	step := func() {
		t.Helper()
		dev.run(deviceTick)
		f, err := p.Advance(deviceTick)
		if err != nil {
			t.Fatal(err)
		}
		if f != nil {
			f.Release()
		}
		time.Sleep(deviceTick)
	}
	// Half a second of waiting, then one second on the clock of Advance.
	for i := 0; i < 90; i++ {
		step()
	}
	pos := p.Position()
	if pos < 900*time.Millisecond || pos > 1050*time.Millisecond || dev.playing {
		t.Fatalf("after 1.5 s without a device: position %v, device playing %v", pos, dev.playing)
	}
	dev.ready = true
	for i := 0; i < 30; i++ {
		step()
	}
	if len(dev.sets) != 1 || dev.sets[0] < pos || dev.sets[0] > pos+2*deviceTick || !dev.playing {
		t.Fatalf("the device came up at %v: placed at %v, playing %v", pos, dev.sets, dev.playing)
	}
	joined := dev.sets[0]
	heard := dev.heardFrom(joined)
	first := int(int64(joined) * rate / int64(time.Second))
	inPlace(t, "the sound from where the device joined", pcm[(first+256)*channels:], heard[256*channels:], channels)
}

// TestPlayerLoopsWithSound: both tracks start again together, pass after
// pass, with the device's position running on.
func TestPlayerLoopsWithSound(t *testing.T) {
	requireHardware(t)
	const rate, channels, frames = 48000, 2, 30
	pcm := testutil.AudioSignal(rate, channels, rate*frames/30)
	d := movie(t, pcm, rate, channels, frames)
	v := d.Video()
	p, err := New(v.PacketSource(), WithAudio(d.Audio().PacketSource()), WithAudioFormat(hwmediacodec.S16), WithLoop())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Audio() == nil {
		t.Skipf("no audio decoder: %v", p.AudioErr())
	}
	length := p.Length()
	dev := &fakeDevice{t: t, stream: p.Audio(), ready: true}
	p.AttachAudioDevice(dev)
	p.Play()
	passes := map[int]int{} // pictures shown per pass
	for i := 0; i < 200; i++ {
		dev.run(deviceTick)
		f, err := p.Advance(deviceTick)
		if err != nil {
			t.Fatal(err)
		}
		if f != nil {
			ft := time.Duration(f.PTS) * time.Second / time.Duration(v.TimeScale)
			f.Release()
			pos := dev.Position()
			pass := int(pos / length)
			local := pos - time.Duration(pass)*length
			passes[pass]++
			if ft > local+time.Millisecond || local-ft > 2*time.Second/30+deviceTick {
				t.Fatalf("the picture of %v is shown at %v of pass %d", ft, local, pass)
			}
		}
		time.Sleep(deviceTick)
	}
	if p.Ended() || len(passes) < 3 || passes[0] < 25 || passes[1] < 25 || passes[2] < 25 {
		t.Fatalf("ended %v, pictures per pass %v", p.Ended(), passes)
	}
	// The sound of the second pass starts where the pass does.
	second := dev.heardFrom(0)
	n := int(int64(length) * rate / int64(time.Second))
	if len(second) < (n+rate/2)*channels {
		t.Fatalf("the device heard %d samples, less than a pass and a half", len(second)/channels)
	}
	inPlace(t, "the sound of the second pass", pcm, second[n*channels:min(len(second), (n+rate)*channels)], channels)
}
