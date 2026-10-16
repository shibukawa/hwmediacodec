package main

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

// status is the outcome of one check.
type status int

const (
	pending status = iota
	running
	passed
	failed
	unsupported // no backend for it on this machine: not a failure
	skipped     // the user skipped it, or it could not run
)

func (s status) String() string {
	return [...]string{"pending", "running", "pass", "FAIL", "unsupported", "skipped"}[s]
}

// mark is the short form for the window and the Markdown table.
func (s status) mark() string {
	return [...]string{"..", ">>", "OK", "NG", "--", "-"}[s]
}

// result is one line of the report.
type result struct {
	Section string // "Decode", "Encode", "Audio", "Playback", "WebRTC"
	Name    string // "H.264 / videotoolbox"
	Status  status
	Detail  string
}

// report collects the results; the automatic checks, the window and the
// web page write to it from their own goroutines.
type report struct {
	mu      sync.Mutex
	started time.Time
	caps    []hwmediacodec.Capability
	probe   error
	results []*result
	browser string // the user agent of the WebRTC page
	notes   []string
}

func newReport() *report { return &report{started: time.Now()} }

// add appends a result and returns it for later updates through set.
func (r *report) add(section, name string) *result {
	r.mu.Lock()
	defer r.mu.Unlock()
	res := &result{Section: section, Name: name}
	r.results = append(r.results, res)
	return res
}

func (r *report) set(res *result, s status, format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res.Status = s
	res.Detail = fmt.Sprintf(format, args...)
}

func (r *report) note(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, fmt.Sprintf(format, args...))
}

// snapshot copies the results for drawing.
func (r *report) snapshot() []result {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]result, len(r.results))
	for i, res := range r.results {
		out[i] = *res
	}
	return out
}

// counts returns how many results passed and failed.
func (r *report) counts() (pass, fail int) {
	for _, res := range r.snapshot() {
		switch res.Status {
		case passed:
			pass++
		case failed:
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
func (r *report) backendNames() []string {
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

// title is the issue title: the platform and the backends, nothing about
// the user or the machine beyond that.
func (r *report) title() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	pass, fail := 0, 0
	for _, res := range r.results {
		switch res.Status {
		case passed:
			pass++
		case failed:
			fail++
		}
	}
	b := strings.Join(r.backendNames(), ", ")
	if b == "" {
		b = "no hardware backend"
	}
	return fmt.Sprintf("Check report: %s/%s (%s), %d passed, %d failed", runtime.GOOS, runtime.GOARCH, b, pass, fail)
}

// issueURL opens a new issue with the title filled in; the body is pasted
// by hand so that the user sees what is sent.
func (r *report) issueURL() string {
	return "https://github.com/shibukawa/hwmediacodec/issues/new?title=" + url.QueryEscape(r.title())
}

// markdown renders the report for a GitHub issue.
func (r *report) markdown() string {
	title := r.title()
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", title)
	fmt.Fprintf(&b, "- hwmediacodec: %s\n", libraryVersion())
	fmt.Fprintf(&b, "- Go: %s, %s/%s, %d CPUs\n", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	if r.browser != "" {
		fmt.Fprintf(&b, "- Browser: %s\n", r.browser)
	}
	fmt.Fprintf(&b, "- Run: %s, %s\n", r.started.UTC().Format("2006-01-02 15:04 MST"), time.Since(r.started).Round(time.Second))
	b.WriteString("- GPU / driver: _(please fill in, e.g. Intel UHD 770, driver 31.0.101.5186)_\n\n")

	b.WriteString("### Probe\n\n")
	if r.probe != nil {
		fmt.Fprintf(&b, "Probe failed: `%v`\n\n", r.probe)
	}
	if len(r.caps) == 0 {
		b.WriteString("No codec reported.\n\n")
	} else {
		b.WriteString("| Backend | Codec | Direction | Hardware | Max size |\n|---|---|---|---|---|\n")
		for _, c := range r.caps {
			size := ""
			if c.MaxWidth > 0 && c.MaxHeight > 0 {
				size = fmt.Sprintf("%dx%d", c.MaxWidth, c.MaxHeight)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %v | %s |\n", c.Backend, c.Codec, c.Direction, c.Hardware, size)
		}
		b.WriteString("\n")
	}

	sections := map[string][]*result{}
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
	b.WriteString("_Generated by `go run github.com/shibukawa/hwmediacodec/examples/check@latest`._\n")
	return b.String()
}

// cell keeps a detail on one table row.
func cell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", "\\|")
}
