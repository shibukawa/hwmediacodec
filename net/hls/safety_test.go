package hls

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

func TestPlaylistOwnsDataAndDropsReferences(t *testing.T) {
	p := NewPlaylist(3, time.Second)
	p.segs = make([]mp4.Segment, 0, 4)
	init := []byte{1, 2}
	p.SetInit(init)
	init[0] = 9
	segment := []byte{3, 4}
	p.Add(mp4.Segment{Seq: 1, Data: segment})
	segment[0] = 9
	for _, tc := range []struct {
		path string
		want byte
	}{{"/init.mp4", 1}, {"/seg_1.m4s", 3}} {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if got := w.Body.Bytes(); len(got) == 0 || got[0] != tc.want {
			t.Fatalf("%s = %v", tc.path, got)
		}
	}
	for i := 2; i <= 3; i++ {
		p.Add(mp4.Segment{Seq: i, Data: []byte{byte(i)}})
	}
	backing := p.segs[:cap(p.segs)]
	p.Add(mp4.Segment{Seq: 4, Data: []byte{4}})
	// append may reallocate; inspect the current array's dropped prefix.
	if cap(p.segs) == cap(backing)-1 && backing[0].Data != nil {
		t.Fatal("evicted segment data retained")
	}
}
