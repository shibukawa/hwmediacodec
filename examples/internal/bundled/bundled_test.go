package bundled

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/ebitenvideo"
	"github.com/shibukawa/hwmediacodec/examples/assets"
	"github.com/shibukawa/hwmediacodec/internal/mediatest"
)

var errNoDecoder = &hwmediacodec.UnsupportedError{Codec: hwmediacodec.HEVC, Direction: hwmediacodec.Decode, Reason: "test"}

// TestChoose checks the order of the attempts and which error comes back.
func TestChoose(t *testing.T) {
	broken := errors.New("broken file")
	cases := []struct {
		name    string
		results map[string]error // "round/clip" -> error; missing means success
		want    string           // the attempts made
		err     error
	}{
		{"first works", nil, "0/0", nil},
		{"second encoding", map[string]error{"0/0": errNoDecoder}, "0/0 0/1", nil},
		{"second round", map[string]error{"0/0": errNoDecoder, "0/1": errNoDecoder}, "0/0 0/1 1/0", nil},
		{"second round, second encoding", map[string]error{"0/0": errNoDecoder, "0/1": errNoDecoder, "1/0": errNoDecoder}, "0/0 0/1 1/0 1/1", nil},
		{"nothing works", map[string]error{"0/0": errNoDecoder, "0/1": fmt.Errorf("h264: %w", hwmediacodec.ErrUnsupported), "1/0": errNoDecoder, "1/1": errNoDecoder}, "0/0 0/1 1/0 1/1", errNoDecoder},
		{"another error ends it", map[string]error{"0/0": errNoDecoder, "0/1": broken}, "0/0 0/1", broken},
	}
	for _, tc := range cases {
		got := ""
		err := choose(2, 2, func(round, clip int) error {
			key := fmt.Sprintf("%d/%d", round, clip)
			if got != "" {
				got += " "
			}
			got += key
			if e, failed := tc.results[key]; failed {
				return e
			}
			return nil
		})
		if got != tc.want || err != tc.err {
			t.Errorf("%s: attempts %q (want %q), error %v (want %v)", tc.name, got, tc.want, err, tc.err)
		}
	}
	if err := choose(1, 0, func(int, int) error { return nil }); err == nil {
		t.Error("no clip: no error")
	}
}

// play runs the player until a frame shows.
func play(t *testing.T, p *ebitenvideo.Player) {
	t.Helper()
	p.Play()
	for i := 0; i < 400 && p.Image() == nil; i++ {
		if err := p.Update(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if p.Image() == nil {
		t.Fatal("no frame shown")
	}
	if w, h := p.Size(); w != 720 || h != 1280 {
		t.Fatalf("picture %dx%d", w, h)
	}
}

// TestOpenPrefersHEVC: where the machine decodes HEVC, that is the clip.
func TestOpenPrefersHEVC(t *testing.T) {
	mediatest.RequireHardware(t, hwmediacodec.HEVC, hwmediacodec.Decode)
	p, v, err := Open(ebitenvideo.WithLoop())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if v.Name != assets.WaterfallName || v.Width != 720 || v.Height != 1280 {
		t.Fatalf("opened %+v", v)
	}
	play(t, p)
}

// TestOpenH264Clip plays the H.264 encoding, which is what a machine
// without an HEVC decoder ends up with.
func TestOpenH264Clip(t *testing.T) {
	mediatest.RequireHardware(t, hwmediacodec.H264, hwmediacodec.Decode)
	clips := assets.Clips()
	p, v, err := open(clips[1:], [][]ebitenvideo.Option{{ebitenvideo.WithLoop()}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if v.Name != assets.WaterfallH264Name || v.Width != 720 || v.Height != 1280 {
		t.Fatalf("opened %+v", v)
	}
	play(t, p)
	if l := p.Length(); l < 9800*time.Millisecond || l > 9900*time.Millisecond {
		t.Errorf("length %v, want about 9.88 s", l)
	}
}

// TestOpenFallsBackToH264: without an HEVC decoder (Firefox, run through
// scripts/browser_test.sh) Open ends up with the H.264 encoding and plays
// it.
func TestOpenFallsBackToH264(t *testing.T) {
	ctx := context.Background()
	if hwmediacodec.HasHardware(ctx, hwmediacodec.HEVC, hwmediacodec.Decode) {
		t.Skip("this machine decodes HEVC; see TestOpenPrefersHEVC")
	}
	mediatest.RequireHardware(t, hwmediacodec.H264, hwmediacodec.Decode)
	p, v, err := Open(ebitenvideo.WithLoop())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if v.Name != assets.WaterfallH264Name {
		t.Fatalf("opened %+v", v)
	}
	play(t, p)
}
