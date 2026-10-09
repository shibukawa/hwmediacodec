package container

import (
	"errors"
	"fmt"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/annexb"
)

// paramSets collects the VPS/SPS/PPS seen in Annex-B packets and turns
// access units into length-prefixed MP4 samples.
type paramSets struct {
	codec hwmediacodec.Codec
	vps   [][]byte
	sps   [][]byte
	pps   [][]byte
}

func (ps *paramSets) complete() bool {
	return len(ps.sps) > 0 && len(ps.pps) > 0 && (ps.codec == hwmediacodec.H264 || len(ps.vps) > 0)
}

func (ps *paramSets) add(t int, nal []byte) {
	kind, _, ok := annexb.ParameterSetID(ps.codec, t, nal)
	if !ok {
		return
	}
	set := &ps.sps
	switch kind {
	case annexb.ParamVPS:
		set = &ps.vps
	case annexb.ParamPPS:
		set = &ps.pps
	}
	for _, have := range *set {
		if string(have) == string(nal) {
			return
		}
	}
	*set = append(*set, append([]byte(nil), nal...))
}

// sample converts one Annex-B access unit into an MP4 sample (4-byte
// length prefixes), recording parameter sets and dropping them and the
// access-unit delimiter from the sample. It returns nil data when the
// packet held parameter sets only.
func (ps *paramSets) sample(p hwmediacodec.Packet) (data []byte, sync bool, err error) {
	nals := annexb.Split(p.Data)
	if len(nals) == 0 {
		return nil, false, errors.New("container: empty access unit")
	}
	sync = p.Keyframe
	for _, nal := range nals {
		t := annexb.NALUnitType(ps.codec, nal)
		switch {
		case t < 0:
			continue
		case annexb.IsParameterSet(ps.codec, t):
			ps.add(t, nal)
			continue
		case ps.codec == hwmediacodec.H264 && t == annexb.H264NALAUD,
			ps.codec == hwmediacodec.HEVC && t == annexb.HEVCNALAUD:
			continue
		case annexb.IsVCL(ps.codec, t) && annexb.IsKeyframe(ps.codec, t):
			sync = true
		}
		n := len(nal)
		data = append(data, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
		data = append(data, nal...)
	}
	if data == nil {
		return nil, false, nil
	}
	if !ps.complete() {
		return nil, false, errors.New("container: the first video packet must carry the parameter sets (VPS/SPS/PPS)")
	}
	return data, sync, nil
}

// sampleEntry builds the avc1/hvc1 sample description and reports the
// picture size from the SPS.
func (ps *paramSets) sampleEntry() (entry *mp4.VisualSampleEntryBox, width, height int, err error) {
	if !ps.complete() {
		return nil, 0, 0, errors.New("container: no parameter sets")
	}
	switch ps.codec {
	case hwmediacodec.H264:
		sps, err := avc.ParseSPSNALUnit(ps.sps[0], false)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("container: parse SPS: %w", err)
		}
		width, height = int(sps.Width), int(sps.Height)
		avcC, err := mp4.CreateAvcC(ps.sps, ps.pps, true)
		if err != nil {
			return nil, 0, 0, err
		}
		entry = mp4.CreateVisualSampleEntryBox("avc1", uint16(width), uint16(height), avcC)
	case hwmediacodec.HEVC:
		sps, err := hevc.ParseSPSNALUnit(ps.sps[0])
		if err != nil {
			return nil, 0, 0, fmt.Errorf("container: parse SPS: %w", err)
		}
		w, h := sps.ImageSize()
		width, height = int(w), int(h)
		hvcC, err := mp4.CreateHvcC(ps.vps, ps.sps, ps.pps, true, true, true, true)
		if err != nil {
			return nil, 0, 0, err
		}
		entry = mp4.CreateVisualSampleEntryBox("hvc1", uint16(width), uint16(height), hvcC)
	default:
		return nil, 0, 0, fmt.Errorf("container: cannot describe %s samples", ps.codec)
	}
	return entry, width, height, nil
}

// setDescriptor fills the sample description of a fragmented-MP4 track.
func (ps *paramSets) setDescriptor(trak *mp4.TrakBox) error {
	switch ps.codec {
	case hwmediacodec.H264:
		return trak.SetAVCDescriptor("avc1", ps.sps, ps.pps, true)
	case hwmediacodec.HEVC:
		return trak.SetHEVCDescriptor("hvc1", ps.vps, ps.sps, ps.pps, nil, true)
	}
	return fmt.Errorf("container: cannot describe %s samples", ps.codec)
}
