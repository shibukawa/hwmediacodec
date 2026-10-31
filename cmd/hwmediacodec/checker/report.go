package checker

import (
	"fmt"
	"net/url"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shibukawa/hwmediacodec"
)

// Status is the outcome of one check.
type Status int

const (
	Pending Status = iota
	Running
	Passed
	Failed
	Unsupported // no backend for it on this machine: not a failure
	Skipped     // the user skipped it, or it could not run
)

func (s Status) String() string {
	return [...]string{"pending", "running", "pass", "FAIL", "unsupported", "skipped"}[s]
}

// Mark is the short form for the window and the Markdown table.
func (s Status) Mark() string {
	return [...]string{"..", ">>", "OK", "NG", "--", "-"}[s]
}

// Result is one line of the report.
type Result struct {
	Section string // "Decode", "Encode", "Audio", "Playback", "WebRTC"
	Name    string // "H.264 / videotoolbox"
	Status  Status
	Detail  string
}

// Report collects the results; the automatic checks, the window and the
// web page write to it from their own goroutines.
type Report struct {
	// Generator names what made the report, in its last line. Device, when
	// set, takes the place of the GPU line for the reporter to fill in, and
	// PasteHint is the issue body when the report is too long for the
	// link. Set them before the checks start.
	Generator string
	Device    string
	PasteHint string

	mu       sync.Mutex
	started  time.Time
	caps     []hwmediacodec.Capability
	probe    error
	results  []*Result
	browsers []string // the user agents that opened the WebRTC page
	notes    []string
}

// NewReport returns an empty report with the desktop check's wording.
func NewReport() *Report {
	return &Report{
		Generator: "`go run github.com/shibukawa/hwmediacodec/cmd/hwmediacodec@latest check`",
		PasteHint: "_The report was too long for the link: please paste hwmediacodec-report.md here._",
		started:   time.Now(),
	}
}

// SetProbe records what Probe found.
func (r *Report) SetProbe(caps []hwmediacodec.Capability, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.caps, r.probe = caps, err
}

// Caps returns what Probe found, nil before the checks probed.
func (r *Report) Caps() []hwmediacodec.Capability {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.caps
}

// AddBrowser records a user agent that opened the WebRTC page; the page
// can be opened in several browsers, which may receive differently.
func (r *Report) AddBrowser(ua string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.browsers {
		if b == ua {
			return
		}
	}
	r.browsers = append(r.browsers, ua)
}

// Browsers returns the user agents AddBrowser recorded, in order.
func (r *Report) Browsers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.browsers...)
}

// Backend names the backend the library picks for c in dir: the first one
// Probe listed for it, as NewDecoder and NewEncoder try them in that
// order. It is empty when none did.
func (r *Report) Backend(c hwmediacodec.Codec, dir hwmediacodec.Direction) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cp := range r.caps {
		if cp.Codec == c && cp.Direction == dir {
			return cp.Backend
		}
	}
	return ""
}

// BrowserName shortens a user agent to the browser and its major version,
// "Edge 156", for check names. Unknown agents give "the browser".
func BrowserName(ua string) string {
	for _, b := range []struct{ token, name string }{
		{"Edg/", "Edge"}, {"EdgA/", "Edge"}, {"EdgiOS/", "Edge"},
		{"OPR/", "Opera"}, {"Firefox/", "Firefox"}, {"FxiOS/", "Firefox"},
		{"CriOS/", "Chrome"}, {"Chrome/", "Chrome"}, {"Version/", "Safari"},
	} {
		i := strings.Index(ua, b.token)
		if i < 0 {
			continue
		}
		v := ua[i+len(b.token):]
		if j := strings.IndexAny(v, ". "); j >= 0 {
			v = v[:j]
		}
		if v == "" {
			return b.name
		}
		return b.name + " " + v
	}
	return "the browser"
}

// gpus lists the devices Probe named for the hardware video backends.
func (r *Report) gpus() []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range r.caps {
		if c.Codec.IsAudio() || c.Device == "" {
			continue
		}
		d := c.Device
		if i := strings.Index(d, " ("); i > 0 {
			d = d[:i] // the Media Foundation transform after the GPU
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// Add appends a result and returns it for later updates through Set.
func (r *Report) Add(section, name string) *Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	res := &Result{Section: section, Name: name}
	r.results = append(r.results, res)
	return res
}

// Rename changes the name of a result, for checks whose subject is known
// only later (the browser that opened the WebRTC page).
func (r *Report) Rename(res *Result, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res.Name = name
}

func (r *Report) Set(res *Result, s Status, format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res.Status = s
	res.Detail = fmt.Sprintf(format, args...)
}

func (r *Report) Note(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, fmt.Sprintf(format, args...))
}

// Snapshot copies the results for drawing.
func (r *Report) Snapshot() []Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Result, len(r.results))
	for i, res := range r.results {
		out[i] = *res
	}
	return out
}

// Counts returns how many results passed and failed.
func (r *Report) Counts() (pass, fail int) {
	for _, res := range r.Snapshot() {
		switch res.Status {
		case Passed:
			pass++
		case Failed:
			fail++
		}
	}
	return pass, fail
}

// libraryVersion is the version of the hwmediacodec module this program
// was built with, "(devel)" inside a clone.
func libraryVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, m := range info.Deps {
		if m.Path == "github.com/shibukawa/hwmediacodec" {
			if m.Replace != nil {
				return m.Version + " (replaced by " + m.Replace.Path + ")"
			}
			return m.Version
		}
	}
	return info.Main.Version
}

// backendNames lists the video backends Probe reported, in order.
func (r *Report) backendNames() []string {
	var names []string
	seen := map[string]bool{}
	for _, c := range r.caps {
		if !c.Codec.IsAudio() && !seen[c.Backend] {
			seen[c.Backend] = true
			names = append(names, c.Backend)
		}
	}
	return names
}

// Title is the issue title: the platform and the backends, nothing about
// the user or the machine beyond that.
func (r *Report) Title() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	pass, fail := 0, 0
	for _, res := range r.results {
		switch res.Status {
		case Passed:
			pass++
		case Failed:
			fail++
		}
	}
	b := strings.Join(r.backendNames(), ", ")
	if b == "" {
		b = "no hardware backend"
	}
	return fmt.Sprintf("Check report: %s/%s (%s), %d passed, %d failed", runtime.GOOS, runtime.GOARCH, b, pass, fail)
}

// MaxIssueURL keeps the new-issue link below the length GitHub accepts
// (it answers 414 somewhere above 8 KB).
const MaxIssueURL = 8000

// IssueURL opens a new issue with the title and the report filled in;
// nothing is sent until the user submits the form. A report too long for
// a link is left out (the body is PasteHint) and has to be pasted. full
// reports whether the body is in the link.
func (r *Report) IssueURL() (link string, full bool) {
	base := "https://github.com/shibukawa/hwmediacodec/issues/new?title=" + url.QueryEscape(r.Title())
	if link := base + "&body=" + url.QueryEscape(r.Markdown()); len(link) <= MaxIssueURL {
		return link, true
	}
	return base + "&body=" + url.QueryEscape(r.PasteHint), false
}

// Markdown renders the report for a GitHub issue.
func (r *Report) Markdown() string {
	title := r.Title()
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", title)
	fmt.Fprintf(&b, "- hwmediacodec: %s\n", libraryVersion())
	fmt.Fprintf(&b, "- Go: %s, %s/%s, %d CPUs\n", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	for _, ua := range r.browsers {
		fmt.Fprintf(&b, "- Browser: %s\n", ua)
	}
	fmt.Fprintf(&b, "- Run: %s, %s\n", r.started.UTC().Format("2006-01-02 15:04 MST"), time.Since(r.started).Round(time.Second))
	if r.Device != "" {
		fmt.Fprintf(&b, "- Device: %s\n\n", r.Device)
	} else if gpus := r.gpus(); len(gpus) > 0 {
		fmt.Fprintf(&b, "- GPU: %s; driver: _(please fill in, e.g. 31.0.101.5186)_\n\n", strings.Join(gpus, ", "))
	} else {
		b.WriteString("- GPU / driver: _(please fill in, e.g. Intel UHD 770, driver 31.0.101.5186)_\n\n")
	}

	b.WriteString("### Probe\n\n")
	if r.probe != nil {
		fmt.Fprintf(&b, "Probe failed: `%v`\n\n", r.probe)
	}
	if len(r.caps) == 0 {
		b.WriteString("No codec reported.\n\n")
	} else {
		b.WriteString("| Backend | Codec | Direction | Hardware | Max size | Device |\n|---|---|---|---|---|---|\n")
		for _, c := range r.caps {
			size := ""
			if c.MaxWidth > 0 && c.MaxHeight > 0 {
				size = fmt.Sprintf("%dx%d", c.MaxWidth, c.MaxHeight)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %v | %s | %s |\n", c.Backend, c.Codec, c.Direction, c.Hardware, size, cell(c.Device))
		}
		b.WriteString("\n")
	}

	sections := map[string][]*Result{}
	var order []string
	for _, res := range r.results {
		if _, ok := sections[res.Section]; !ok {
			order = append(order, res.Section)
		}
		sections[res.Section] = append(sections[res.Section], res)
	}
	rank := map[string]int{"Decode": 0, "Encode": 1, "Audio": 2, "Playback": 3, "WebRTC": 4}
	sort.SliceStable(order, func(i, j int) bool { return rank[order[i]] < rank[order[j]] })
	for _, sec := range order {
		fmt.Fprintf(&b, "### %s\n\n| Check | Result | Detail |\n|---|---|---|\n", sec)
		for _, res := range sections[sec] {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", res.Name, res.Status, cell(res.Detail))
		}
		b.WriteString("\n")
	}
	if len(r.notes) > 0 {
		b.WriteString("### Notes\n\n")
		for _, n := range r.notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "_Generated by %s._\n", r.Generator)
	return b.String()
}

// cell keeps a detail on one table row.
func cell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", "\\|")
}
