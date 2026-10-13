package hwmediacodec_test

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/shibukawa/hwmediacodec"
)

// TestNativeFormatOutsideTheBrowser: frames that stay with the decoder
// exist in the browser only; everywhere else the format is refused, not
// silently replaced.
func TestNativeFormatOutsideTheBrowser(t *testing.T) {
	if runtime.GOOS == "js" {
		t.Skip("the browser backend offers the Native format; see internal/webcodecs TestNativeFrames")
	}
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC, hwmediacodec.AV1} {
		dec, err := hwmediacodec.NewDecoder(context.Background(), c, hwmediacodec.WithOutputFormat(hwmediacodec.Native))
		if !errors.Is(err, hwmediacodec.ErrUnsupported) {
			if dec != nil {
				dec.Close()
			}
			t.Errorf("%s: NewDecoder with the Native format = %v, want ErrUnsupported", c, err)
		}
	}
	if hwmediacodec.Native.String() != "native" || hwmediacodec.Native.PlaneCount() != 0 || hwmediacodec.Native.FrameSize(640, 480) != 0 {
		t.Errorf("Native: %s, %d planes, %d bytes", hwmediacodec.Native, hwmediacodec.Native.PlaneCount(), hwmediacodec.Native.FrameSize(640, 480))
	}
	if _, err := hwmediacodec.NewEncoder(context.Background(), hwmediacodec.H264, 640, 480, hwmediacodec.WithInputFormat(hwmediacodec.Native)); !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Errorf("NewEncoder with the Native format = %v, want ErrUnsupported", err)
	}
}
