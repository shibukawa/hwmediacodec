package webcodecs

import (
	"math"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// webKitForm turns a tightly packed video-range NV12 picture into the form
// WebKit's H.264 and HEVC decoders hand it over in (measured on Safari 27
// against ffmpeg's decode of the same streams): luma widened to full
// range, chroma moved up a quarter line (three quarters of a row plus one
// quarter of the row below, the last row as it is) and widened. Safari's
// own arithmetic rounds a sample differently now and then, so a frame from
// the browser is within one step of this, not equal to it.
func webKitForm(nv12 []byte, width, height int) []byte {
	clip := func(v float64) byte { return byte(min(max(math.Round(v), 0), 255)) }
	out := make([]byte, len(nv12))
	luma := width * height
	for i, v := range nv12[:luma] {
		out[i] = clip(float64(int(v)-16) * 255 / 219)
	}
	rows, rowBytes := codec.NV12.PlaneLayout(1, width, height)
	src, dst := nv12[luma:], out[luma:]
	for y := 0; y < rows; y++ {
		below := min(y+1, rows-1)
		for x := 0; x < rowBytes; x++ {
			c := (3*float64(src[y*rowBytes+x]) + float64(src[below*rowBytes+x])) / 4
			dst[y*rowBytes+x] = clip((c-128)*255/224 + 128)
		}
	}
	return out
}

// restore is what the decoder does to a frame in that form.
func restore(pix []byte, width, height int) {
	luma := width * height
	rows, rowBytes := codec.NV12.PlaneLayout(1, width, height)
	narrowRange([][]byte{pix[:luma], pix[luma:]})
	unshiftChroma(pix[luma:], rowBytes, rowBytes, rows)
}

// TestRestoreWebKitForm checks that a picture that went through WebKit's
// widening and chroma shift comes back: the luma exactly, the chroma to
// within a step, sharp vertical chroma edges included.
func TestRestoreWebKitForm(t *testing.T) {
	const w, h = 64, 48
	cw, ch := w/2, h/2
	pix := make([]byte, codec.NV12.FrameSize(w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pix[y*w+x] = byte(16 + (x*7+y*13)%220) // 16..235
		}
	}
	uv := pix[w*h:]
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			cb, cr := 16+(x*9)%225, 240-(y*31)%225 // 16..240; Cr jumps from row to row
			if y >= ch/2 {
				cb = 240 - (x*9)%225 // a hard horizontal edge across the picture
			}
			uv[y*w+2*x], uv[y*w+2*x+1] = byte(cb), byte(cr)
		}
	}
	got := webKitForm(pix, w, h)
	restore(got, w, h)
	for i := 0; i < w*h; i++ {
		if got[i] != pix[i] {
			t.Fatalf("luma sample %d: %d came back as %d", i, pix[i], got[i])
		}
	}
	worst, exact := 0, 0
	for i := w * h; i < len(pix); i++ {
		d := int(got[i]) - int(pix[i])
		if d < 0 {
			d = -d
		}
		worst = max(worst, d)
		if d == 0 {
			exact++
		}
	}
	t.Logf("chroma: worst difference %d, %d of %d samples exact", worst, exact, len(pix)-w*h)
	if worst > 1 {
		t.Errorf("a chroma sample came back %d steps off, want at most 1", worst)
	}
	if exact*10 < (len(pix)-w*h)*6 {
		t.Errorf("only %d of %d chroma samples came back exactly", exact, len(pix)-w*h)
	}

	// Without the shift undone the edge would be tens of steps off: the
	// test has teeth.
	blurred := webKitForm(pix, w, h)
	narrowRange([][]byte{blurred[:w*h], blurred[w*h:]})
	off := 0
	for i := w * h; i < len(pix); i++ {
		if d := int(blurred[i]) - int(pix[i]); d > 8 || d < -8 {
			off++
		}
	}
	if off == 0 {
		t.Error("the picture has no chroma edge the shift would blur")
	}
}

func TestUnshiftChromaStrides(t *testing.T) {
	// Rows of 4 meaningful bytes in a stride of 6; the padding stays.
	uv := []byte{
		100, 100, 100, 100, 7, 7,
		130, 130, 130, 130, 7, 7,
		220, 220, 220, 220, 7, 7,
	}
	// The rows as they arrive, (3*c[y] + c[y+1]) / 4, of decoded rows
	// 100, 100, 220: row 1 is (4*130 - 220) / 3 = 100 and row 0 is
	// (4*100 - 100) / 3 = 100.
	unshiftChroma(uv, 6, 4, 3)
	want := []byte{
		100, 100, 100, 100, 7, 7,
		100, 100, 100, 100, 7, 7,
		220, 220, 220, 220, 7, 7,
	}
	for i := range want {
		if uv[i] != want[i] {
			t.Fatalf("byte %d = %d, want %d (%v)", i, uv[i], want[i], uv)
		}
	}
	// Results outside a byte are clipped.
	edge := []byte{250, 0, 10, 255}
	unshiftChroma(edge, 2, 2, 2)
	if edge[0] != 255 || edge[1] != 0 {
		t.Errorf("clipping: %v", edge)
	}
}
