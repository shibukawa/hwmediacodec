package h264

// ParameterSets stores the active SPS and PPS NAL units of a stream and
// resolves the parameter sets a slice refers to.
//
// A PPS is parsed lazily against the SPS it names, so that the order in
// which SPS and PPS arrive, and an SPS being re-sent with new content, are
// both handled.
type ParameterSets struct {
	sps    map[uint32]*SPS
	ppsRaw map[uint32][]byte
	pps    map[uint32]*PPS
	ppsSPS map[uint32]*SPS // SPS instance each parsed PPS was resolved against
}

// NewParameterSets returns an empty store.
func NewParameterSets() *ParameterSets {
	return &ParameterSets{
		sps:    map[uint32]*SPS{},
		ppsRaw: map[uint32][]byte{},
		pps:    map[uint32]*PPS{},
		ppsSPS: map[uint32]*SPS{},
	}
}

// AddSPS parses and stores an SPS NAL unit, replacing any previous SPS with
// the same id.
func (p *ParameterSets) AddSPS(nal []byte) (*SPS, error) {
	s, err := ParseSPS(nal)
	if err != nil {
		return nil, err
	}
	p.sps[s.ID] = s
	return s, nil
}

// AddPPS stores a PPS NAL unit. It is parsed when first used.
func (p *ParameterSets) AddPPS(nal []byte) error {
	// Validate the header and the ids now so that garbage is rejected
	// early, even though the full parse happens later.
	if _, typ, ok := NALHeader(nal); !ok || typ != NALPPS {
		return syntaxErr("not a PPS NAL unit")
	}
	id, _, err := ppsIDs(nal)
	if err != nil {
		return err
	}
	p.ppsRaw[id] = append([]byte(nil), nal...)
	delete(p.pps, id)
	delete(p.ppsSPS, id)
	return nil
}

// SPS returns the stored SPS with the given id, or nil.
func (p *ParameterSets) SPS(id uint32) *SPS { return p.sps[id] }

// Lookup returns the PPS with the given id and the SPS it refers to.
func (p *ParameterSets) Lookup(ppsID uint32) (*SPS, *PPS, error) {
	raw, ok := p.ppsRaw[ppsID]
	if !ok {
		return nil, nil, ErrMissingPPS
	}
	if pps := p.pps[ppsID]; pps != nil {
		if sps := p.sps[pps.SPSID]; sps != nil && sps == p.ppsSPS[ppsID] {
			return sps, pps, nil
		}
	}
	pps, err := ParsePPS(raw, p.SPS)
	if err != nil {
		return nil, nil, err
	}
	sps := p.sps[pps.SPSID]
	p.pps[ppsID] = pps
	p.ppsSPS[ppsID] = sps
	return sps, pps, nil
}

// ppsIDs reads pic_parameter_set_id and seq_parameter_set_id.
func ppsIDs(nal []byte) (ppsID, spsID uint32, err error) {
	r := newRBSPReader(nal[1:])
	if ppsID, err = r.ReadUE(); err != nil {
		return 0, 0, err
	}
	if spsID, err = r.ReadUE(); err != nil {
		return 0, 0, err
	}
	if ppsID > maxPPSID || spsID > maxSPSID {
		return 0, 0, syntaxErr("parameter set id out of range")
	}
	return ppsID, spsID, nil
}
