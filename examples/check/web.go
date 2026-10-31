package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/capture"
	"github.com/shibukawa/hwmediacodec/examples/internal/checker"
	"github.com/shibukawa/hwmediacodec/net/webrtc"
)

// stream is one encoder feeding one WebRTC broadcaster.
type stream struct {
	codec hwmediacodec.Codec
	key   string // "h264", "hevc": the path of its offer endpoint
	bc    *webrtc.Broadcaster
	rec   *capture.Recorder

	backend string // the backend expected to encode it, for the check names

	// rows holds the results of each browser that opened the page, by
	// BrowserName; the first one takes over the rows added at the start
	// (named for "the browser" until then).
	first *rows
	rows  map[string]*rows
}

// rows are the results of one stream in one browser.
type rows struct {
	received *checker.Result // the page saw frames
	picture  *checker.Result // the user saw the fireworks
}

// name is the check name of one row of the stream.
func (s *stream) name(what, browser string) string {
	n := checker.CodecName(s.codec) + " " + what + " " + browser
	if s.backend != "" {
		n += " / " + s.backend
	}
	return n
}

// web serves the check page and the signalling for every stream.
type web struct {
	r       *checker.Report
	streams []*stream
	url     string
	srv     *http.Server

	mu       sync.Mutex
	answered map[string]bool
}

// webW x webH is the picture the encoders stream.
const webW, webH, webFPS = 640, 360, 30

// startWeb opens an encoder and a broadcaster for every codec in codecs
// and serves the page on addr. Codecs whose encoder does not open get a
// result saying so and no stream.
func startWeb(r *checker.Report, addr string, codecs []hwmediacodec.Codec) (*web, error) {
	w := &web{r: r, answered: map[string]bool{}}
	for _, c := range codecs {
		s := &stream{codec: c, key: strings.ToLower(c.String()), backend: r.Backend(c, hwmediacodec.Encode), rows: map[string]*rows{}}
		s.first = &rows{received: r.Add("WebRTC", s.name("stream received by", "the browser"))}
		received := s.first.received
		bc, err := webrtc.NewCodecBroadcaster(c, webFPS, nil, func() {
			if s.rec != nil {
				s.rec.RequestKeyframe()
			}
		})
		if err != nil {
			r.Set(received, checker.Unsupported, "%v", err)
			continue
		}
		opts := capture.Options{Codec: c, FPS: webFPS, Bitrate: 2_000_000, KeyframeInterval: webFPS * 4, LowLatency: true}
		if c == hwmediacodec.H264 {
			// Every browser decodes Constrained Baseline over WebRTC.
			opts.Profile = hwmediacodec.ProfileBaseline
		}
		rec, err := capture.New(webW, webH, bc, opts)
		if err != nil {
			bc.Close()
			r.Set(received, checker.StatusOf(err), "encoder: %v", err)
			continue
		}
		s.bc, s.rec = bc, rec
		s.first.picture = r.Add("WebRTC", s.name("picture in", "the browser"))
		r.Set(received, checker.Running, "waiting for the browser")
		r.Set(s.first.picture, checker.Pending, "")
		w.streams = append(w.streams, s)
	}
	if len(w.streams) == 0 {
		return w, nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		w.close()
		return nil, err
	}
	w.url = "http://" + ln.Addr().String() + "/"
	if a := ln.Addr().(*net.TCPAddr); a.IP.IsUnspecified() {
		w.url = fmt.Sprintf("http://localhost:%d/", a.Port)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", w.page)
	for _, s := range w.streams {
		mux.Handle("POST /offer/"+s.key, s.bc)
	}
	mux.HandleFunc("POST /result", w.result)
	w.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go w.srv.Serve(ln)
	return w, nil
}

// done reports whether the user answered for every stream.
func (w *web) done() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.answered) == len(w.streams)
}

func (w *web) close() {
	if w.srv != nil {
		w.srv.Close()
	}
	for _, s := range w.streams {
		if s.rec != nil {
			s.rec.Close() // closes the broadcaster too
		} else if s.bc != nil {
			s.bc.Close()
		}
	}
}

// pageResult is what the page posts: the receiver statistics once frames
// arrive, or the user's answer.
type pageResult struct {
	Codec     string `json:"codec"`
	Error     string `json:"error"`
	Frames    int    `json:"frames"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Decoder   string `json:"decoder"`
	MimeType  string `json:"mimeType"`
	Answer    string `json:"answer"` // "yes", "no"
	UserAgent string `json:"userAgent"`
}

func (w *web) result(rw http.ResponseWriter, req *http.Request) {
	var pr pageResult
	if err := json.NewDecoder(http.MaxBytesReader(rw, req.Body, 1<<16)).Decode(&pr); err != nil {
		http.Error(rw, "bad result", http.StatusBadRequest)
		return
	}
	var s *stream
	for _, cand := range w.streams {
		if cand.key == pr.Codec {
			s = cand
		}
	}
	if s == nil {
		http.Error(rw, "unknown codec", http.StatusBadRequest)
		return
	}
	ua := truncate(strings.TrimSpace(pr.UserAgent), 200)
	if ua != "" {
		w.r.AddBrowser(ua)
	}
	browser := checker.BrowserName(ua)
	rs := s.first // a post without a user agent: the first browser's rows
	if ua != "" {
		rs = w.rowsFor(s, browser)
	}
	switch {
	case pr.Answer == "yes" || pr.Answer == "no":
		st := checker.Passed
		if pr.Answer == "no" {
			st = checker.Failed
		}
		w.r.Set(rs.picture, st, "answered %s in %s", pr.Answer, browser)
		w.mu.Lock()
		w.answered[s.key] = true
		w.mu.Unlock()
	case pr.Error != "":
		w.r.Set(rs.received, checker.Failed, "%s", truncate(pr.Error, 300))
	default:
		dec := truncate(pr.Decoder, 60)
		if dec == "" {
			dec = "not reported"
		}
		w.r.Set(rs.received, checker.Passed, "%d frames %dx%d (%s), browser decoder %s", pr.Frames, pr.Width, pr.Height, truncate(pr.MimeType, 30), dec)
	}
	rw.WriteHeader(http.StatusNoContent)
}

// rowsFor returns the results of s in browser: the rows added at the start
// for the first browser that reports, new ones for each browser after it.
func (w *web) rowsFor(s *stream, browser string) *rows {
	w.mu.Lock()
	defer w.mu.Unlock()
	if rs, ok := s.rows[browser]; ok {
		return rs
	}
	rs := s.first
	if len(s.rows) == 0 {
		w.r.Rename(rs.received, s.name("stream received by", browser))
		w.r.Rename(rs.picture, s.name("picture in", browser))
	} else {
		rs = &rows{
			received: w.r.Add("WebRTC", s.name("stream received by", browser)),
			picture:  w.r.Add("WebRTC", s.name("picture in", browser)),
		}
		w.r.Set(rs.received, checker.Running, "waiting for %s", browser)
		w.r.Set(rs.picture, checker.Pending, "")
	}
	s.rows[browser] = rs
	return rs
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func (w *web) page(rw http.ResponseWriter, _ *http.Request) {
	type entry struct{ Key, Name string }
	var list []entry
	for _, s := range w.streams {
		list = append(list, entry{s.key, checker.CodecName(s.codec)})
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTemplate.Execute(rw, list)
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>hwmediacodec check</title>
<style>
body{margin:0;padding:16px;background:#14161a;color:#dde;font:15px system-ui,sans-serif}
h1{font-size:18px;font-weight:600;margin:0 0 4px}
p.lead{margin:0 0 16px;color:#99a}
.grid{display:flex;flex-wrap:wrap;gap:16px}
.card{background:#1d2026;border-radius:10px;padding:12px;flex:1 1 360px;max-width:680px}
.card h2{font-size:16px;margin:0 0 8px}
video{width:100%;aspect-ratio:16/9;background:#000;border-radius:6px}
.s{color:#aab;margin:8px 0;min-height:1.4em}
button{font:inherit;padding:6px 14px;margin-right:8px;border-radius:6px;border:1px solid #445;background:#2a2e36;color:#eef;cursor:pointer}
button.on{background:#3d6b45;border-color:#5a9}
button.off{background:#6b3d3d;border-color:#a55}
button:disabled{opacity:.5;cursor:default}
</style></head><body>
<h1>hwmediacodec check: WebRTC</h1>
<p class="lead">Each player below receives the fireworks show from the check window, encoded by this machine's hardware encoder. Answer for each one; the results go into the report.</p>
<div class="grid">
{{range .}}<div class="card" data-codec="{{.Key}}">
<h2>{{.Name}}</h2>
<video autoplay muted playsinline></video>
<div class="s">connecting…</div>
<div>Do you see fireworks moving smoothly?
<button data-a="yes" disabled>Yes</button><button data-a="no" disabled>No</button></div>
</div>
{{end}}</div>
<script>
function post(o) {
  o.userAgent = navigator.userAgent;
  return fetch('result', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(o)});
}
async function start(card) {
  const codec = card.dataset.codec, v = card.querySelector('video'), s = card.querySelector('.s');
  const buttons = card.querySelectorAll('button');
  buttons.forEach(b => b.onclick = () => {
    buttons.forEach(x => { x.classList.remove('on', 'off'); });
    b.classList.add(b.dataset.a === 'yes' ? 'on' : 'off');
    post({codec, answer: b.dataset.a});
  });
  const pc = new RTCPeerConnection();
  pc.addTransceiver('video', {direction: 'recvonly'});
  pc.ontrack = e => { v.srcObject = e.streams[0]; };
  await pc.setLocalDescription(await pc.createOffer());
  await new Promise(res => {
    if (pc.iceGatheringState === 'complete') return res();
    const t = setTimeout(res, 1000);
    pc.onicegatheringstatechange = () => { if (pc.iceGatheringState === 'complete') { clearTimeout(t); res(); } };
  });
  const r = await fetch('offer/' + codec, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(pc.localDescription)});
  if (!r.ok) {
    const why = (await r.text()).trim();
    s.textContent = 'refused: ' + why + (r.status === 400 ? ' (this browser may not receive ' + codec.toUpperCase() + ' over WebRTC)' : '');
    post({codec, error: 'the server refused the browser offer: ' + why});
    buttons.forEach(b => b.disabled = false);
    return;
  }
  await pc.setRemoteDescription(await r.json());
  let reported = false, since = 0;
  const timer = setInterval(async () => {
    let frames = 0, decoder = '', mime = '', w = 0, h = 0;
    const stats = await pc.getStats();
    const codecs = {};
    stats.forEach(x => { if (x.type === 'codec') codecs[x.id] = x.mimeType; });
    stats.forEach(x => {
      if (x.type === 'inbound-rtp' && x.kind === 'video') {
        frames = x.framesDecoded || 0; decoder = x.decoderImplementation || '';
        w = x.frameWidth || 0; h = x.frameHeight || 0; mime = codecs[x.codecId] || '';
      }
    });
    s.textContent = pc.connectionState + '  ' + mime + '  ' + frames + ' frames  ' + w + 'x' + h + (decoder ? '  decoder ' + decoder : '');
    if (frames > 0) buttons.forEach(b => b.disabled = false);
    if (!since && frames > 0) since = Date.now();
    if (!reported && since && Date.now() - since > 3000) {
      reported = true;
      post({codec, frames, width: w, height: h, decoder, mimeType: mime});
    }
    if (!since && pc.connectionState === 'failed') {
      clearInterval(timer);
      post({codec, error: 'the WebRTC connection failed'});
      buttons.forEach(b => b.disabled = false);
    }
  }, 500);
  setTimeout(() => {
    if (!since) { post({codec, error: 'no frame decoded within 15 seconds (connection ' + pc.connectionState + ')'}); buttons.forEach(b => b.disabled = false); }
  }, 15000);
}
document.querySelectorAll('.card').forEach(c => start(c).catch(e => {
  c.querySelector('.s').textContent = 'error: ' + e;
  post({codec: c.dataset.codec, error: String(e)});
  c.querySelectorAll('button').forEach(b => b.disabled = false);
}));
</script></body></html>
`))
