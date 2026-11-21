package mp4

import (
	"os"
	"sync"

	"github.com/shibukawa/hwmediacodec"
)

// VideoFile is an MP4 file with a single video track fed with encoder
// packets. It is a hwmediacodec.PacketWriteCloser, so a capture.Recorder
// or an EncodeWriter can write straight
// into it.
type VideoFile struct {
	m  *Muxer
	vw *VideoWriter
}

// CreateVideoFile creates the file at path with one H.264 or HEVC track.
// The time scale is the unit of Packet.PTS and Packet.DTS
// (capture.TimeScale for a Recorder).
func CreateVideoFile(path string, c hwmediacodec.Codec, timeScale uint32) (*VideoFile, error) {
	m, err := Create(path)
	if err != nil {
		return nil, err
	}
	vw, err := m.AddVideoTrack(c, timeScale)
	if err != nil {
		m.Close()
		return nil, err
	}
	return &VideoFile{m: m, vw: vw}, nil
}

// WritePacket appends one packet to the track.
func (f *VideoFile) WritePacket(p hwmediacodec.Packet) error { return f.vw.WritePacket(p) }

// Close writes the sample tables and closes the file.
func (f *VideoFile) Close() error { return f.m.Close() }

// AVFile is an MP4 file with one H.264 or HEVC track and one AAC or Opus
// track, both fed with encoder packets. The packets of the two tracks may
// come from different goroutines (a capture.Recorder for the pictures, an
// AudioEncoder drained from a microphone callback for the sound): writes
// are serialised, and the samples are interleaved in the order they
// arrive.
//
// The two tracks are lined up by their timestamps, so Packet.PTS of both
// must count on one clock (each in its own time scale): the track that
// starts later starts later in the file. The audio encoder's delay goes
// into the edit list, as with Muxer.AddAudioTrack.
type AVFile struct {
	mu     sync.Mutex
	m      *Muxer
	vw     *VideoWriter
	aw     *AudioWriter
	video  avTrack
	audio  avTrack
	open   int // tracks whose writer has not been closed
	closed bool
	err    error
}

// CreateAVFile creates the file at path with a video track of codec c
// whose packets count in videoTimeScale (capture.TimeScale for a
// Recorder), and an audio track for the packets of an encoder with the
// given format (AudioEncoder.Format) counting in audioTimeScale (the
// sample rate for an encoder opened without WithTimeScale).
func CreateAVFile(path string, c hwmediacodec.Codec, videoTimeScale uint32, audio hwmediacodec.AudioFormat, audioTimeScale uint32) (*AVFile, error) {
	m, err := Create(path)
	if err != nil {
		return nil, err
	}
	vw, err := m.AddVideoTrack(c, videoTimeScale)
	if err == nil {
		var aw *AudioWriter
		if aw, err = m.AddAudioTrack(audio, audioTimeScale); err == nil {
			f := &AVFile{m: m, vw: vw, aw: aw, open: 2}
			f.video = avTrack{f: f, write: f.WriteVideoPacket}
			f.audio = avTrack{f: f, write: f.WriteAudioPacket}
			return f, nil
		}
	}
	m.Close()
	os.Remove(path)
	return nil, err
}

// WriteVideoPacket appends one packet to the video track.
func (f *AVFile) WriteVideoPacket(p hwmediacodec.Packet) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.vw.WritePacket(p)
}

// WriteAudioPacket appends one packet to the audio track.
func (f *AVFile) WriteAudioPacket(p hwmediacodec.Packet) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.aw.WritePacket(p)
}

// Video returns the video track as a PacketWriteCloser, for a writer that
// owns its sink (capture.New closes it when the recording ends). Closing
// it ends the track; the file is finished once both tracks are closed, or
// by AVFile.Close. Every call returns the same writer.
func (f *AVFile) Video() hwmediacodec.PacketWriteCloser { return &f.video }

// Audio returns the audio track as a PacketWriteCloser (see Video).
func (f *AVFile) Audio() hwmediacodec.PacketWriteCloser { return &f.audio }

// Close writes the sample tables and closes the file, whether or not the
// track writers have been closed. A track that received no packet is left
// out of the file.
func (f *AVFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeLocked()
}

func (f *AVFile) closeLocked() error {
	if !f.closed {
		f.closed = true
		f.err = f.m.Close()
	}
	return f.err
}

type avTrack struct {
	f      *AVFile
	write  func(hwmediacodec.Packet) error
	closed bool
}

func (t *avTrack) WritePacket(p hwmediacodec.Packet) error { return t.write(p) }

func (t *avTrack) Close() error {
	f := t.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if !t.closed {
		t.closed = true
		f.open--
	}
	if f.open > 0 {
		return nil
	}
	return f.closeLocked()
}
