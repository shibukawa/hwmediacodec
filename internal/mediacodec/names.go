package mediacodec

import (
	"strings"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediacodec/sys"
)

// Name is the backend identifier reported in Capability.Backend.
const Name = "mediacodec"

// mimeOf returns the MediaCodec MIME type of a codec.
func mimeOf(c codec.Codec) (string, bool) {
	switch c {
	case codec.H264:
		return sys.MimeH264, true
	case codec.HEVC:
		return sys.MimeHEVC, true
	case codec.AV1:
		return sys.MimeAV1, true
	}
	return "", false
}

// isSoftwareCodec reports whether a MediaCodec name denotes one of the
// framework's software codecs, following MediaCodecInfo.isSoftwareOnly:
// the Codec2 and OMX software names, and vendor names that say ".sw.".
func isSoftwareCodec(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "c2.android.") || strings.HasPrefix(n, "omx.google.") ||
		strings.Contains(n, ".sw.") || strings.HasSuffix(n, ".sw")
}

// profileLevel maps a requested profile to the MediaFormat profile and a
// level generous enough for the picture: level 4.1 up to 1080p, 5.1 up to
// 4K, 6.1 above (HEVC). The encoder writes the level it actually needs.
func profileLevel(c codec.Codec, p codec.Profile, width, height int) (profile, level int32, ok bool) {
	pixels := width * height
	switch c {
	case codec.H264:
		switch p {
		case codec.ProfileBaseline:
			profile = sys.AVCProfileBaseline
		case codec.ProfileMain:
			profile = sys.AVCProfileMain
		case codec.ProfileHigh:
			profile = sys.AVCProfileHigh
		default:
			return 0, 0, false
		}
		level = sys.AVCLevel41
		if pixels > 1920*1088 {
			level = sys.AVCLevel51
		}
		return profile, level, true
	case codec.HEVC:
		if p != codec.ProfileMain {
			return 0, 0, false
		}
		level = sys.HEVCMainTierLevel41
		switch {
		case pixels > 4096*2176:
			level = sys.HEVCMainTierLevel61
		case pixels > 1920*1088:
			level = sys.HEVCMainTierLevel51
		}
		return sys.HEVCProfileMain, level, true
	}
	return 0, 0, false
}
