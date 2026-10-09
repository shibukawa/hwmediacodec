package hwmediacodec

import (
	"context"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

type namedBackend string

func (b namedBackend) Name() string { return string(b) }
func (namedBackend) Probe(context.Context) ([]codec.Capability, error) {
	return nil, nil
}
func (namedBackend) NewDecoder(context.Context, codec.DecoderConfig) (codec.Decoder, error) {
	return nil, nil
}
func (namedBackend) NewEncoder(context.Context, codec.EncoderConfig) (codec.Encoder, error) {
	return nil, nil
}

// TestSelectBackends checks the HWMEDIACODEC_BACKENDS list: it filters and
// reorders the registered backends and ignores unknown names.
func TestSelectBackends(t *testing.T) {
	all := []codec.Backend{namedBackend("nvidia"), namedBackend("vpl"), namedBackend("vaapi")}
	cases := []struct {
		list string
		want []string
	}{
		{"", []string{"nvidia", "vpl", "vaapi"}},
		{"  ", []string{"nvidia", "vpl", "vaapi"}},
		{"vaapi", []string{"vaapi"}},
		{"vaapi, vpl", []string{"vaapi", "vpl"}},
		{"VPL,vpl,unknown", []string{"vpl"}},
		{"videotoolbox", nil},
	}
	for _, c := range cases {
		got := selectBackends(all, c.list)
		if len(got) != len(c.want) {
			t.Errorf("%q: got %d backends, want %v", c.list, len(got), c.want)
			continue
		}
		for i, b := range got {
			if b.Name() != c.want[i] {
				t.Errorf("%q: backend %d is %s, want %s", c.list, i, b.Name(), c.want[i])
			}
		}
	}
}
