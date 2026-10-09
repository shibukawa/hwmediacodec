package av1

// CodecConfigurationRecord returns the AV1CodecConfigurationRecord of the
// ISOBMFF AV1 binding (Section 2.3; the payload of an av1C box) for a
// stream with sequence header sh, whose sequence header OBU payload is
// seqPayload. The record carries no initial_presentation_delay, and its
// configOBUs hold the sequence header OBU re-framed with
// obu_has_size_field=1, as the binding requires.
func CodecConfigurationRecord(sh *SequenceHeader, seqPayload []byte) []byte {
	rec := make([]byte, 0, 6+len(seqPayload))
	rec = append(rec, 0x81) // marker=1, version=1
	rec = append(rec, sh.Profile<<5|sh.LevelIdx&0x1f)
	b := (sh.Tier & 1) << 7
	if sh.BitDepth >= 10 {
		b |= 1 << 6 // high_bitdepth
	}
	if sh.BitDepth == 12 {
		b |= 1 << 5 // twelve_bit
	}
	if sh.MonoChrome {
		b |= 1 << 4
	}
	b |= (sh.SubsamplingX & 1) << 3
	b |= (sh.SubsamplingY & 1) << 2
	b |= sh.ChromaSamplePosition & 3
	rec = append(rec, b, 0) // reserved, initial_presentation_delay_present=0
	return AppendOBU(rec, OBUSequenceHeader, seqPayload)
}
