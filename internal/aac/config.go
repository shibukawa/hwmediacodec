// Package aac reads and writes the two small headers that describe an AAC
// stream: the AudioSpecificConfig of MPEG-4 (ISO/IEC 14496-3, section
// 1.6.2.1), which an MP4 file keeps in its esds box, and the ADTS frame
// header of raw .aac files and MPEG transport streams. It contains no
// codec.
package aac

import (
	"errors"
	"fmt"
)

// Audio object types.
const (
	ObjectTypeMain = 1
	ObjectTypeLC   = 2
	ObjectTypeSSR  = 3
	ObjectTypeLTP  = 4
	ObjectTypeSBR  = 5
	ObjectTypePS   = 29
)

// sampleRates is the sampling frequency index table.
var sampleRates = [...]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

// channelCounts maps a channel configuration to its number of channels
// (index 0, a layout described by a program config element, has none).
var channelCounts = [...]int{0, 1, 2, 3, 4, 5, 6, 8}

// ErrInvalid reports a header that cannot be parsed.
var ErrInvalid = errors.New("aac: invalid header")

// Config is a parsed AudioSpecificConfig.
type Config struct {
	// ObjectType is the audio object type of the core coder (ObjectTypeLC
	// for AAC-LC and for HE-AAC, whose core is AAC-LC).
	ObjectType int
	// SampleRate is the rate of the core coder in Hz.
	SampleRate int
	// ChannelConfig is the channel configuration (1 mono, 2 stereo, 6 for
	// 5.1, 7 for 7.1; 0 when a program config element describes the
	// layout).
	ChannelConfig int
	// FrameSamples is the number of samples per channel of one access unit
	// of the core coder: 1024, or 960 with the short frame length.
	FrameSamples int
	// SBR reports spectral band replication (HE-AAC) and PS parametric
	// stereo (HE-AAC v2), when the config signals them explicitly.
	SBR bool
	PS  bool
	// ExtensionSampleRate is the output rate with SBR, or 0 without it.
	ExtensionSampleRate int
}

// Channels is the number of channels of the core coder, or 0 when the
// config does not say (ChannelConfig 0).
func (c Config) Channels() int {
	if c.ChannelConfig < 0 || c.ChannelConfig >= len(channelCounts) {
		return 0
	}
	return channelCounts[c.ChannelConfig]
}

// OutputSampleRate is the rate of the decoded samples: the extension rate
// with SBR, the core rate otherwise.
func (c Config) OutputSampleRate() int {
	if c.SBR && c.ExtensionSampleRate > 0 {
		return c.ExtensionSampleRate
	}
	return c.SampleRate
}

// OutputChannels is the number of decoded channels: parametric stereo
// turns a mono core into stereo.
func (c Config) OutputChannels() int {
	if c.PS && c.Channels() == 1 {
		return 2
	}
	return c.Channels()
}

// OutputFrameSamples is the number of samples per channel one access unit
// decodes to.
func (c Config) OutputFrameSamples() int {
	if c.SampleRate > 0 {
		return c.FrameSamples * c.OutputSampleRate() / c.SampleRate
	}
	return c.FrameSamples
}

type bitReader struct {
	data []byte
	pos  int // in bits
}

func (r *bitReader) left() int { return len(r.data)*8 - r.pos }

func (r *bitReader) read(n int) (uint32, error) {
	if r.left() < n {
		return 0, fmt.Errorf("%w: AudioSpecificConfig is truncated", ErrInvalid)
	}
	var v uint32
	for range n {
		v = v<<1 | uint32(r.data[r.pos>>3]>>(7-r.pos&7)&1)
		r.pos++
	}
	return v, nil
}

func (r *bitReader) objectType() (int, error) {
	v, err := r.read(5)
	if err == nil && v == 31 {
		v, err = r.read(6)
		v += 32
	}
	return int(v), err
}

func (r *bitReader) sampleRate() (int, error) {
	idx, err := r.read(4)
	if err != nil {
		return 0, err
	}
	if idx == 15 {
		v, err := r.read(24)
		return int(v), err
	}
	if int(idx) >= len(sampleRates) {
		return 0, fmt.Errorf("%w: reserved sampling frequency index %d", ErrInvalid, idx)
	}
	return sampleRates[idx], nil
}

// ParseConfig parses an AudioSpecificConfig.
func ParseConfig(asc []byte) (Config, error) {
	r := &bitReader{data: asc}
	var c Config
	var err error
	if c.ObjectType, err = r.objectType(); err != nil {
		return c, err
	}
	if c.SampleRate, err = r.sampleRate(); err != nil {
		return c, err
	}
	cc, err := r.read(4)
	if err != nil {
		return c, err
	}
	c.ChannelConfig = int(cc)
	if c.ObjectType == ObjectTypeSBR || c.ObjectType == ObjectTypePS {
		// Hierarchical signalling: the extension comes first, then the
		// object type of the core.
		c.SBR, c.PS = true, c.ObjectType == ObjectTypePS
		if c.ExtensionSampleRate, err = r.sampleRate(); err != nil {
			return c, err
		}
		if c.ObjectType, err = r.objectType(); err != nil {
			return c, err
		}
	}
	c.FrameSamples = 1024
	switch c.ObjectType {
	case 1, 2, 3, 4, 6, 7, 17, 19, 20, 21, 22, 23:
		// GASpecificConfig: only the frame length matters here.
		short, err := r.read(1)
		if err != nil {
			return c, err
		}
		if short == 1 {
			c.FrameSamples = 960
		}
		dependsOnCore, err := r.read(1)
		if err != nil {
			return c, err
		}
		if dependsOnCore == 1 {
			if _, err := r.read(14); err != nil {
				return c, err
			}
		}
		if _, err := r.read(1); err != nil { // extensionFlag
			return c, err
		}
		if c.ChannelConfig == 0 {
			// A program config element follows; its layout is left to the
			// decoder and the sync extension after it is not looked for.
			return c, nil
		}
	default:
		return c, nil
	}
	// Backward compatible signalling: a sync extension after the config.
	if !c.SBR && r.left() >= 16 {
		if sync, _ := r.read(11); sync == 0x2b7 {
			if ext, err := r.objectType(); err == nil && ext == ObjectTypeSBR {
				if present, _ := r.read(1); present == 1 {
					if rate, err := r.sampleRate(); err == nil {
						c.SBR, c.ExtensionSampleRate = true, rate
						if r.left() >= 12 {
							if sync, _ := r.read(11); sync == 0x548 {
								ps, _ := r.read(1)
								c.PS = ps == 1
							}
						}
					}
				}
			}
		}
	}
	return c, nil
}

// LCConfig builds the AudioSpecificConfig of an AAC-LC stream.
func LCConfig(sampleRate, channels int) ([]byte, error) {
	cc := -1
	for i, n := range channelCounts {
		if i > 0 && n == channels {
			cc = i
		}
	}
	if cc < 0 {
		return nil, fmt.Errorf("aac: no channel configuration for %d channels", channels)
	}
	if sampleRate <= 0 || sampleRate >= 1<<24 {
		return nil, fmt.Errorf("aac: sample rate %d out of range", sampleRate)
	}
	for i, r := range sampleRates {
		if r == sampleRate {
			// 5 bits object type, 4 bits rate index, 4 bits channel
			// configuration, 3 zero bits of GASpecificConfig.
			v := uint16(ObjectTypeLC)<<11 | uint16(i)<<7 | uint16(cc)<<3
			return []byte{byte(v >> 8), byte(v)}, nil
		}
	}
	// An explicit 24-bit rate: 5 + 4 + 24 + 4 + 3 = 40 bits.
	v := uint64(ObjectTypeLC)<<35 | 15<<31 | uint64(sampleRate)<<7 | uint64(cc)<<3
	return []byte{byte(v >> 32), byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}, nil
}
