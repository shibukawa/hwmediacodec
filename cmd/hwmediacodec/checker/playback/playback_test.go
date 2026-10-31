package playback

import (
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/cmd/hwmediacodec/checker"
)

// TestPlaybackAnswers walks the stage through the answers without a
// window: a decoder for the clip is needed, so it runs where one is.
func TestPlaybackAnswers(t *testing.T) {
	r := checker.NewReport()
	caps, err := hwmediacodec.Probe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r.SetProbe(caps, nil)
	p := New(r, 0, false)
	defer p.Close()
	if p.Done() {
		t.Skip("no decoder for the bundled clip on this machine")
	}
	first := p.Codec()
	p.Answer(true) // still watching: ignored
	if len(r.Snapshot()) != 0 {
		t.Fatal("an answer while watching was recorded")
	}
	p.Update()
	if p.Question() != AskPicture {
		t.Fatalf("question %d after the play time", p.Question())
	}
	p.Answer(true)
	if p.Player() != nil && p.Player().HasAudio() && p.Question() != AskSound {
		t.Fatalf("no sound question for a clip with audio")
	}
	if p.Question() == AskSound {
		p.Answer(false)
	}
	for !p.Done() {
		p.Skip()
	}
	res := r.Snapshot()
	if want := checker.CodecName(first) + " picture / " + r.Backend(first, hwmediacodec.Decode); res[0].Name != want || res[0].Status != checker.Passed {
		t.Errorf("first result %+v", res[0])
	}
	if res[1].Name == "Sound (AAC)" && res[1].Status != checker.Failed {
		t.Errorf("sound result %+v", res[1])
	}
	for _, x := range res[2:] {
		if x.Status != checker.Skipped {
			t.Errorf("result %+v after Skip", x)
		}
	}
}
