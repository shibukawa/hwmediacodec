//go:build linux && !android

package fdkaac

import (
	"context"
	"errors"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// A frame of a minute of PCM is encoded in parts and the queue stays
// bounded until Flush.
func TestEncoderEncodesLongFrameInParts(t *testing.T) {
	const rate, channels = 48000, 2
	enc, err := Backend{}.NewAudioEncoder(context.Background(), codec.AudioEncoderConfig{Codec: codec.AAC, SampleRate: rate, Channels: channels, SampleFormat: codec.S16, TimeScale: rate, RateControl: codec.VBR})
	var unsupported *codec.UnsupportedError
	if errors.As(err, &unsupported) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	e := enc.(*encoder)
	testutil.CheckLongFrame(t, testutil.LongFrame{
		Encoder: enc, SampleRate: rate, Channels: channels,
		Pending: func() int { return len(e.pending) }, Bound: maxPending, Slack: 0,
		HasInput: e.hasInput,
	})
}
