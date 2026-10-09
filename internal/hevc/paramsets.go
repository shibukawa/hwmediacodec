package hevc

// ParameterSets stores the sequence and picture parameter sets of a stream
// and resolves the ones a slice segment refers to.
type ParameterSets struct {
	sps map[uint32]*SPS
	pps map[uint32]*PPS
}

// NewParameterSets returns an empty store.
func NewParameterSets() *ParameterSets {
	return &ParameterSets{sps: map[uint32]*SPS{}, pps: map[uint32]*PPS{}}
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

// AddPPS parses and stores a PPS NAL unit, replacing any previous PPS with
// the same id. A PPS does not depend on its SPS for parsing, so the two may
// arrive in either order.
func (p *ParameterSets) AddPPS(nal []byte) (*PPS, error) {
	pps, err := ParsePPS(nal)
	if err != nil {
		return nil, err
	}
	p.pps[pps.ID] = pps
	return pps, nil
}

// SPS returns the stored SPS with the given id, or nil.
func (p *ParameterSets) SPS(id uint32) *SPS { return p.sps[id] }

// Lookup returns the PPS with the given id and the SPS it refers to.
func (p *ParameterSets) Lookup(ppsID uint32) (*SPS, *PPS, error) {
	pps := p.pps[ppsID]
	if pps == nil {
		return nil, nil, ErrMissingPPS
	}
	sps := p.sps[pps.SPSID]
	if sps == nil {
		return nil, nil, ErrMissingSPS
	}
	return sps, pps, nil
}
