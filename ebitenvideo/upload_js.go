//go:build js

package ebitenvideo

import (
	"strings"
	"syscall/js"
	"time"

	"github.com/shibukawa/hwmediacodec"
)

// In a browser a decoded picture is a WebCodecs VideoFrame that lives on
// the GPU, and WebGL takes a VideoFrame as the source of a texture upload
// without the pixels leaving the GPU. The usual way into an *ebiten.Image
// is WritePixels, for which the picture has to be read back into CPU
// memory, copied into Go memory and uploaded again. This file takes the
// short way: the player asks the decoder for its own frames
// (hwmediacodec.Native) and uploads them into the texture Ebitengine keeps
// for the player's image.
//
// Ebitengine does not hand out its textures, so they are found from the
// outside. This leans on how Ebitengine works today, and every step falls
// back to the copy path when it does not hold:
//
//  1. Before Ebitengine starts, texImage2D on the WebGL2 prototype is
//     wrapped, so that every texture allocation Ebitengine makes is seen:
//     which texture, what size, in what order. (Ebitengine takes its own
//     reference to the context's functions when it starts; the wrapper
//     has to be there by then, which an init function is.)
//  2. The player's image is an unmanaged one, so it has a texture to
//     itself. The first frames are written with WritePixels (their pixels
//     read back through a canvas), which makes Ebitengine allocate it.
//  3. A texture that was allocated after the image was made and has the
//     size Ebitengine gives it is a candidate. It is the image's texture
//     only if the pixels just written can be read back from it.
//  4. From then on every frame goes into that texture with one
//     texSubImage2D call, and the texture binding Ebitengine expects is
//     restored afterwards.
//
// If Ebitengine changes underneath (another renderer, another way of
// keeping textures), step 3 finds nothing. Reading every frame back
// through a canvas would be far slower than what the player did before
// there was a short way, so it then starts the stream again at the same
// position with RGBA frames from the decoder and WritePixels, as outside
// the browser (a source that cannot seek keeps the canvas).
//
// What the short way saves depends on the browser and the picture size
// (time per frame on the page's one thread, measured on an Apple M3):
// in Chrome it costs about 1.3 ms whatever the size, against 0.9 ms
// (720p), 1.7 ms (1080p) and 9.4 ms (4K) for RGBA frames, whose pixels the
// browser also has to read back from the GPU; in Safari, where RGBA frames
// are converted from YUV in Go, it is the lighter way at every size. In
// Firefox the decoders hand over frames that are in CPU memory already and
// uploading from the VideoFrame is the slower way (4.5 ms against 1.9 ms
// at 720p), so there the player asks for RGBA frames from the start.

// WebGL constants.
const (
	glTexture2D          = 0x0DE1
	glTextureBinding2D   = 0x8069
	glRGBA               = 0x1908
	glUnsignedByte       = 0x1401
	glFramebuffer        = 0x8D40
	glFramebufferBinding = 0x8CA6
	glColorAttachment0   = 0x8CE0
	glFramebufferOK      = 0x8CD5
	glNoError            = 0
)

// allocation is one texture allocation seen by the wrapper.
type allocation struct {
	gl, texture   js.Value
	width, height int
	seq           int
}

// textures is what the wrapper has seen: the most recent allocations and
// their running count.
var textures struct {
	seq    int
	recent []allocation
}

func init() {
	cls := js.Global().Get("WebGL2RenderingContext")
	if cls.Type() != js.TypeFunction {
		return // not a browser, or one without WebGL2
	}
	proto := cls.Get("prototype")
	original := proto.Get("texImage2D")
	if original.Type() != js.TypeFunction {
		return
	}
	proto.Set("texImage2D", js.FuncOf(func(this js.Value, args []js.Value) (result any) {
		// A JavaScript exception must not escape a Go callback.
		defer func() {
			if r := recover(); r != nil {
				result = nil
			}
		}()
		list := make([]any, len(args))
		for i, a := range args {
			list[i] = a
		}
		ret := original.Call("apply", this, list)
		// texImage2D(TEXTURE_2D, 0, RGBA, width, height, 0, RGBA,
		// UNSIGNED_BYTE, null): storage for a texture, as Ebitengine
		// allocates it.
		if len(args) == 9 && args[8].IsNull() && args[0].Int() == glTexture2D && args[1].Int() == 0 {
			textures.seq++
			textures.recent = append(textures.recent, allocation{
				gl:      this,
				texture: this.Call("getParameter", glTextureBinding2D),
				width:   args[3].Int(),
				height:  args[4].Int(),
				seq:     textures.seq,
			})
			if n := len(textures.recent); n > 64 {
				textures.recent = append(textures.recent[:0], textures.recent[n-64:]...)
			}
		}
		return ret
	}))
}

// internalSize is the size Ebitengine gives the texture of an image side:
// the next power of two, 16 at least.
func internalSize(x int) int {
	r := 16
	for r < x {
		r <<= 1
	}
	return r
}

// sample is one pixel written with WritePixels, to recognise the texture
// by.
type sample struct {
	x, y int
	rgba [4]byte
}

// directUpload is the state of the short way for the player's image.
type directUpload struct {
	since   int      // textures.seq when the image was made
	samples []sample // from the pixels written last, or nil
	tries   int      // searches for the texture so far
	gl      js.Value // set once the texture is found
	texture js.Value
	found   bool
	checked bool // an upload into the texture has been seen to work
	broken  bool // the short way does not work here
	// fellBack: the stream was started again with RGBA frames, or could
	// not be.
	fellBack bool
	last     bool // the frame that is showing took it

	canvas    js.Value // reads a VideoFrame back when it has to be copied
	context2D js.Value
	pixels    []byte

	stats uploadStats
}

// uploadStats counts the frames that took each way and the time the
// uploads kept the game's goroutine busy.
type uploadStats struct {
	direct, copied         int
	directTime, copiedTime time.Duration
}

// forceCopy keeps every player on the copy path, forceRGBA makes new
// players ask the decoder for RGBA pixels as they do outside the browser,
// and breakDirect makes the search for the texture fail. The tests set
// them to compare the ways and to reach the fallback.
var forceCopy, forceRGBA, breakDirect bool

func (d *directUpload) used() bool { return d.last }

// maxTries is the number of frames after which the search for the texture
// is given up; when no texture allocation at all has been seen by then,
// idleTries are enough.
const (
	maxTries  = 10
	idleTries = 3
)

// uploadFormat is the frame format the player asks the decoder for: the
// decoder's own frames where the WebCodecs backend can provide them.
func uploadFormat() hwmediacodec.PixelFormat {
	if ua := js.Global().Get("navigator"); ua.Type() == js.TypeObject && strings.Contains(ua.Get("userAgent").String(), "Gecko/") {
		return hwmediacodec.RGBA // Firefox: see the measurements above
	}
	if !forceRGBA && js.Global().Get("VideoDecoder").Type() == js.TypeFunction && js.Global().Get("OffscreenCanvas").Type() == js.TypeFunction {
		return hwmediacodec.Native
	}
	return hwmediacodec.RGBA
}

func (p *Player) upload(f *hwmediacodec.Frame) {
	d := &p.direct
	start := time.Now()
	vf, ok := f.Native().(js.Value)
	if !ok {
		d.last = false
		p.uploadPixels(f)
		d.stats.copied++
		d.stats.copiedTime += time.Since(start)
		return
	}
	if p.ensureImage(f.Width, f.Height) {
		*d = directUpload{since: textures.seq, canvas: d.canvas, context2D: d.context2D, pixels: d.pixels, stats: d.stats}
	}
	if !forceCopy && !d.broken {
		if !d.found && d.samples != nil {
			d.find(p.width, p.height)
		}
		if d.found && d.put(vf, p.width, p.height) {
			d.last = true
			d.stats.direct++
			d.stats.directTime += time.Since(start)
			return
		}
	}
	// The copy path: read the frame back and write its pixels.
	d.last = false
	pix, err := d.read(vf, p.width, p.height)
	if err != nil {
		return // the previous picture stays
	}
	p.img.WritePixels(pix)
	if !d.found && !d.broken {
		d.remember(pix, p.width, p.height)
	}
	d.stats.copied++
	d.stats.copiedTime += time.Since(start)
	if d.broken && !d.fellBack {
		p.fallBack()
	}
}

// fallBack starts the stream again where it is, with RGBA frames from the
// decoder: the way the player works outside the browser, for when the
// short way does not.
func (p *Player) fallBack() {
	p.direct.fellBack = true
	if p.reopen == nil || !p.core.Seekable() {
		return // nothing to start again from: the canvas stays
	}
	at := p.core.Position()
	// One decoder at a time reads the source.
	p.core.Close()
	core, err := p.reopen(hwmediacodec.RGBA)
	if err != nil {
		p.failed = err
		return
	}
	p.core = core
	if err := core.Seek(at); err != nil {
		p.failed = err
		return
	}
	core.Play()
}

// read returns the RGBA pixels of a VideoFrame by way of a canvas.
func (d *directUpload) read(vf js.Value, w, h int) (pix []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(js.Error)
			if !ok {
				panic(r)
			}
			err = e
		}
	}()
	if d.canvas.IsUndefined() || d.canvas.Get("width").Int() != w || d.canvas.Get("height").Int() != h {
		d.canvas = js.Global().Get("OffscreenCanvas").New(w, h)
		d.context2D = d.canvas.Call("getContext", "2d", map[string]any{"willReadFrequently": true})
	}
	d.context2D.Call("drawImage", vf, 0, 0, w, h)
	data := d.context2D.Call("getImageData", 0, 0, w, h).Get("data")
	if cap(d.pixels) < w*h*4 {
		d.pixels = make([]byte, w*h*4)
	}
	d.pixels = d.pixels[:w*h*4]
	js.CopyBytesToGo(d.pixels, data)
	return d.pixels, nil
}

// remember keeps a few of the pixels just written, spread over the
// picture.
func (d *directUpload) remember(pix []byte, w, h int) {
	d.samples = d.samples[:0]
	for j := 1; j <= 4; j++ {
		for i := 1; i <= 4; i++ {
			x, y := i*w/5, j*h/5
			var s sample
			s.x, s.y = x, y
			copy(s.rgba[:], pix[(y*w+x)*4:])
			d.samples = append(d.samples, s)
		}
	}
}

// find looks for the image's texture among the allocations made since the
// image exists: the right size, and holding the pixels written last.
func (d *directUpload) find(w, h int) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(js.Error); !ok {
				panic(r)
			}
			d.broken = true
		}
	}()
	d.tries++
	if breakDirect {
		d.broken = true
		return
	}
	tw, th := internalSize(w), internalSize(h)
	for _, a := range textures.recent {
		if a.seq <= d.since || a.width != tw || a.height != th || !a.gl.Call("isTexture", a.texture).Bool() {
			continue
		}
		if d.holds(a) {
			d.gl, d.texture, d.found = a.gl, a.texture, true
			d.samples = nil
			return
		}
	}
	// No allocation since the image was made, frame after frame:
	// Ebitengine's textures do not come by the wrapper.
	if d.tries >= maxTries || (d.tries >= idleTries && textures.seq == d.since) {
		d.broken = true
	}
}

// holds reports whether the texture of an allocation holds the remembered
// pixels. It reads them back through a framebuffer of its own and leaves
// the framebuffer binding as it found it.
func (d *directUpload) holds(a allocation) bool {
	gl := a.gl
	previous := gl.Call("getParameter", glFramebufferBinding)
	fb := gl.Call("createFramebuffer")
	defer func() {
		gl.Call("bindFramebuffer", glFramebuffer, previous)
		gl.Call("deleteFramebuffer", fb)
	}()
	gl.Call("bindFramebuffer", glFramebuffer, fb)
	gl.Call("framebufferTexture2D", glFramebuffer, glColorAttachment0, glTexture2D, a.texture, 0)
	if gl.Call("checkFramebufferStatus", glFramebuffer).Int() != glFramebufferOK {
		return false
	}
	pixel := js.Global().Get("Uint8Array").New(4)
	var got [4]byte
	for _, s := range d.samples {
		gl.Call("readPixels", s.x, s.y, 1, 1, glRGBA, glUnsignedByte, pixel)
		js.CopyBytesToGo(got[:], pixel)
		if got != s.rgba {
			return false
		}
	}
	return len(d.samples) > 0
}

// put uploads a VideoFrame into the image's texture. It reports false,
// and gives the short way up, when the browser does not take it.
func (d *directUpload) put(vf js.Value, w, h int) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, isJS := r.(js.Error); !isJS {
				panic(r)
			}
			d.found, d.broken, ok = false, true, false
		}
	}()
	if vf.Get("displayWidth").Int() != w || vf.Get("displayHeight").Int() != h {
		return false // a picture that is shown at another size than it has
	}
	gl := d.gl
	if !gl.Call("isTexture", d.texture).Bool() {
		// Ebitengine dropped the texture (a lost context, a new atlas):
		// look for the new one.
		d.found, d.since = false, textures.seq
		return false
	}
	// getError waits for the GPU process, so only the first upload is
	// checked: it says whether the browser takes a VideoFrame here at all.
	check := !d.checked
	if check {
		gl.Call("getError") // whatever was pending is not this upload's
	}
	previous := gl.Call("getParameter", glTextureBinding2D)
	gl.Call("bindTexture", glTexture2D, d.texture)
	gl.Call("texSubImage2D", glTexture2D, 0, 0, 0, glRGBA, glUnsignedByte, vf)
	failed := check && gl.Call("getError").Int() != glNoError
	gl.Call("bindTexture", glTexture2D, previous)
	if failed {
		d.found, d.broken = false, true
		return false
	}
	d.checked = true
	return true
}
