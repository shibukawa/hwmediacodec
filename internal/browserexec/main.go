// Command browserexec runs a GOOS=js GOARCH=wasm test binary in a real
// browser, where WebCodecs exists (Node.js, which the Go toolchain's own
// go_js_wasm_exec uses, has none). It is a "go test -exec" wrapper:
//
//	go build -o /tmp/bin/go_js_wasm_exec ./internal/browserexec
//	PATH=/tmp/bin:$PATH GOOS=js GOARCH=wasm go test .
//
// (scripts/browser_test.sh does exactly that.) The wrapper serves the test
// binary and a page that starts it on a loopback port, opens the page in a
// headless browser, prints what the test writes and exits with its exit
// code.
//
// A browser has neither a file system nor processes, and the conformance
// tests need both for their oracle, ffmpeg. The wrapper therefore lends
// the tests its own: package internal/testutil sends its ffmpeg and
// ffprobe invocations and its temporary files to the endpoints below
// instead of os/exec and os. The endpoints sit behind a random path token,
// listen on 127.0.0.1 only, run nothing but the tools named in allowed and
// touch no file outside the directory the wrapper created.
//
// Environment:
//
//	HWMEDIACODEC_BROWSER       the browser executable (default: Chrome,
//	                           Chromium or Edge where they are usually
//	                           installed). A Firefox binary gets Firefox's
//	                           flags (and on macOS is started through
//	                           "open"); any other program (for example
//	                           "open" with the arguments "-a Safari") is
//	                           started with the arguments below and the
//	                           page URL
//	HWMEDIACODEC_BROWSER_ARGS  extra browser arguments, space separated
//	HWMEDIACODEC_BROWSER_HEADED=1  show the browser window
//	HWMEDIACODEC_BROWSER_LOG=1     pass the browser's own log through
//
// Every other variable whose name starts with HWMEDIACODEC_ is handed to
// the test binary.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// allowed lists the host tools a test may run.
var allowed = map[string]bool{"ffmpeg": true, "ffprobe": true, "x265": true, "aomenc": true}

// page starts the test binary. Output goes to the wrapper in order; the
// exit code follows once everything has been delivered.
const page = `<!doctype html>
<meta charset="utf-8">
<title>go test (js/wasm)</title>
<script src="wasm_exec.js"></script>
<script>
(async () => {
  const cfg = await (await fetch("config")).json();
  let queue = Promise.resolve();
  const send = (bytes) => { queue = queue.then(() => fetch("out", {method: "POST", body: bytes})).catch(() => {}); };
  const say = (text) => send(new TextEncoder().encode(text));
  globalThis.fs.writeSync = (fd, buf) => { send(buf.slice()); return buf.length; };
  globalThis.fs.write = (fd, buf, offset, length, position, callback) => {
    send(buf.slice(offset, offset + length));
    callback(null, length);
  };
  // Once the program has exited, callbacks that still reach for it (a
  // game loop's next frame) fail by design; that is not the test's news.
  let exited = false;
  addEventListener("error", (e) => { if (!exited) say("browserexec: uncaught " + e.message + "\n"); });
  addEventListener("unhandledrejection", (e) => { if (!exited) say("browserexec: unhandled rejection: " + e.reason + "\n"); });
  let code = 0;
  const go = new Go();
  go.argv = cfg.argv;
  go.env = cfg.env;
  go.exit = (c) => { code = c; exited = true; };
  try {
    const result = await WebAssembly.instantiateStreaming(fetch("test.wasm"), go.importObject);
    await go.run(result.instance);
  } catch (e) {
    say("browserexec: " + e + "\n");
    code = code || 1;
  }
  await queue;
  await fetch("exit?code=" + code, {method: "POST"});
  // A tab opened for this page alone may close itself; where a browser
  // that was already running took the page (Safari), nothing else would.
  window.close();
})();
</script>
`

type server struct {
	wasm     string
	wasmExec string
	work     string // temporary files of the tests live below it
	argv     []string
	env      map[string]string

	mu   sync.Mutex
	exit chan int
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: browserexec test.wasm [test flags]")
		os.Exit(2)
	}
	code, err := run(os.Args[1], os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "browserexec:", err)
		os.Exit(2)
	}
	os.Exit(code)
}

func run(wasm string, args []string) (int, error) {
	wasmExec, err := findWasmExec()
	if err != nil {
		return 0, err
	}
	work, err := os.MkdirTemp("", "hwmc-browser-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(work)
	// macOS hands out /var/folders/... which is a symbolic link; the path
	// check below compares resolved paths.
	if resolved, err := filepath.EvalSymlinks(work); err == nil {
		work = resolved
	}

	s := &server{
		wasm:     wasm,
		wasmExec: wasmExec,
		work:     filepath.Join(work, "files"),
		argv:     append([]string{filepath.Base(wasm)}, testFlags(args)...),
		env:      map[string]string{},
		exit:     make(chan int, 1),
	}
	if err := os.Mkdir(s.work, 0o755); err != nil {
		return 0, err
	}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "HWMEDIACODEC_") && !strings.HasPrefix(k, "HWMEDIACODEC_BROWSER") {
			s.env[k] = v
		}
	}

	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return 0, err
	}
	prefix := "/" + hex.EncodeToString(token) + "/"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	base := "http://" + ln.Addr().String() + prefix
	s.env["HWMEDIACODEC_TEST_HOST"] = base
	mux := http.NewServeMux()
	routes := http.StripPrefix(strings.TrimSuffix(prefix, "/"), s.routes())
	if os.Getenv("HWMEDIACODEC_BROWSER_LOG") != "" {
		inner := routes
		routes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if name := strings.TrimPrefix(r.URL.Path, prefix); name != "out" {
				fmt.Fprintf(os.Stderr, "browserexec: %s %s\n", r.Method, name)
			}
			inner.ServeHTTP(w, r)
		})
	}
	mux.Handle(prefix, routes)
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	browser, signalBrowser, err := startBrowser(base, filepath.Join(work, "profile"))
	if err != nil {
		return 0, err
	}
	died := make(chan error, 1)
	go func() { died <- browser.Wait() }()
	stop := func() {
		signalBrowser(syscall.SIGTERM)
		select {
		case <-died:
		case <-time.After(3 * time.Second):
			signalBrowser(syscall.SIGKILL)
		}
	}

	// "go test" ends a test binary that runs too long with SIGQUIT; the
	// browser must not outlive the wrapper then.
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	select {
	case code := <-s.exit:
		stop()
		return code, nil
	case err := <-died:
		if err != nil {
			return 0, fmt.Errorf("the browser exited before the test finished (%v)", err)
		}
		// A launcher that hands the page to a running browser and exits
		// ("open -a Safari"): the test still reports here.
		select {
		case code := <-s.exit:
			return code, nil
		case <-interrupt:
			return 0, errors.New("interrupted")
		}
	case sig := <-interrupt:
		stop()
		return 0, fmt.Errorf("interrupted (%v)", sig)
	}
}

// testFlags drops the flags that make the test binary write files itself;
// there is no file system in the browser.
func testFlags(args []string) []string {
	var out []string
	for _, a := range args {
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch name {
		case "test.testlogfile", "test.gocoverdir", "test.coverprofile", "test.cpuprofile",
			"test.memprofile", "test.blockprofile", "test.mutexprofile", "test.trace", "test.outputdir":
			continue
		}
		out = append(out, a)
	}
	return out
}

func findWasmExec() (string, error) {
	root := runtime.GOROOT()
	if out, err := exec.Command("go", "env", "GOROOT").Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
		root = string(bytes.TrimSpace(out))
	}
	for _, rel := range []string{"lib/wasm/wasm_exec.js", "misc/wasm/wasm_exec.js"} {
		p := filepath.Join(root, rel)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("wasm_exec.js not found below GOROOT %s", root)
}

func findBrowser() (string, error) {
	if p := os.Getenv("HWMEDIACODEC_BROWSER"); p != "" {
		return p, nil
	}
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	case "windows":
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if root != "" {
				candidates = append(candidates,
					filepath.Join(root, `Google\Chrome\Application\chrome.exe`),
					filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`))
			}
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no Chrome, Chromium or Edge found; set HWMEDIACODEC_BROWSER to a browser executable")
}

// appBundle returns the macOS application bundle a path lies in (or is),
// or "".
func appBundle(path string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	if i := strings.Index(path, ".app/"); i >= 0 {
		return path[:i+len(".app")]
	}
	if strings.HasSuffix(path, ".app") {
		return path
	}
	return ""
}

// startBrowser opens the page in the browser. The returned function sends
// a signal to the browser; the command ends when the browser does.
func startBrowser(url, profile string) (cmd *exec.Cmd, signal func(syscall.Signal), err error) {
	path, err := findBrowser()
	if err != nil {
		return nil, nil, err
	}
	headed := os.Getenv("HWMEDIACODEC_BROWSER_HEADED") != ""
	var args []string
	name := strings.ToLower(filepath.Base(path))
	bundle := ""
	switch {
	case strings.Contains(name, "firefox"):
		if err := os.MkdirAll(profile, 0o755); err != nil {
			return nil, nil, err
		}
		args = []string{"-no-remote", "-profile", profile}
		if !headed {
			args = append(args, "-headless")
		}
		// A Firefox started by the path of its binary from a terminal
		// cannot start its content processes on macOS
		// ("sandbox_extension_issue_file_to_process failed ... Operation
		// not permitted") and spins without ever loading the page; started
		// as an application it can.
		bundle = appBundle(path)
	case !strings.Contains(name, "chrom") && !strings.Contains(name, "edge"):
		// Not a browser this wrapper knows the flags of.
	default:
		args = []string{
			"--user-data-dir=" + profile,
			"--no-first-run", "--no-default-browser-check",
			"--disable-background-networking", "--disable-component-update", "--disable-sync",
			"--disable-extensions", "--disable-features=Translate,MediaRouter",
			// The page does its work without anybody looking at it.
			"--disable-background-timer-throttling", "--disable-renderer-backgrounding",
			"--disable-backgrounding-occluded-windows",
		}
		if !headed {
			args = append(args, "--headless=new")
		}
	}
	args = append(args, strings.Fields(os.Getenv("HWMEDIACODEC_BROWSER_ARGS"))...)
	args = append(args, url)
	if bundle != "" {
		// A new instance (-n) that "open" waits for (-W); "-g" keeps a
		// headed window from taking the focus.
		cmd = exec.Command("open", append([]string{"-n", "-W", "-g", "-a", bundle, "--args"}, args...)...)
	} else {
		cmd = exec.Command(path, args...)
	}
	if os.Getenv("HWMEDIACODEC_BROWSER_LOG") != "" {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start %s: %w", path, err)
	}
	if bundle != "" {
		// The browser is not a child of this process; it is found by
		// the profile directory in its command line, which is this
		// run's own.
		return cmd, func(sig syscall.Signal) {
			exec.Command("pkill", "-"+strconv.Itoa(int(sig)), "-f", profile).Run()
		}, nil
	}
	return cmd, func(sig syscall.Signal) { cmd.Process.Signal(sig) }, nil
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, page)
	})
	mux.HandleFunc("/wasm_exec.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		http.ServeFile(w, r, s.wasmExec)
	})
	mux.HandleFunc("/test.wasm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/wasm")
		http.ServeFile(w, r, s.wasm)
	})
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"argv": s.argv, "env": s.env})
	})
	mux.HandleFunc("/out", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		io.Copy(os.Stdout, r.Body)
		s.mu.Unlock()
	})
	mux.HandleFunc("/exit", func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(r.URL.Query().Get("code"))
		if err != nil {
			code = 2
		}
		select {
		case s.exit <- code:
		default:
		}
	})
	mux.HandleFunc("/lookpath", s.lookPath)
	mux.HandleFunc("/exec", s.exec)
	mux.HandleFunc("/mktemp", s.mktemp)
	mux.HandleFunc("/file", s.file)
	return mux
}

func tool(name string) (string, error) {
	if !allowed[name] {
		return "", fmt.Errorf("%q is not a tool the tests may run", name)
	}
	return exec.LookPath(name)
}

func (s *server) lookPath(w http.ResponseWriter, r *http.Request) {
	if _, err := tool(r.URL.Query().Get("name")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	// The name is the handle; the tests never need the host path.
	io.WriteString(w, r.URL.Query().Get("name"))
}

// exec runs one tool. The request is a JSON object with the tool name and
// its arguments; the response body is the tool's standard output, the
// exit status and standard error travel in headers.
func (s *server) exec(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string   `json:"name"`
		Args []string `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	path, err := tool(req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	cmd := exec.Command(path, req.Args...)
	cmd.Dir = s.work
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	status := "0"
	if err := cmd.Run(); err != nil {
		status = err.Error()
	}
	msg := stderr.Bytes()
	if len(msg) > 1<<20 {
		// Enough for the trace output the parser tests read.
		msg = msg[:1<<20]
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Exit-Status", base64.StdEncoding.EncodeToString([]byte(status)))
	w.Header().Set("X-Stderr-Length", strconv.Itoa(len(msg)))
	// Standard error first, then standard output: headers have a size
	// limit that debug traces exceed.
	w.Write(msg)
	w.Write(stdout.Bytes())
}

func (s *server) mktemp(w http.ResponseWriter, r *http.Request) {
	dir, err := os.MkdirTemp(s.work, "t")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	io.WriteString(w, dir)
}

// file reads (GET), writes (PUT) or removes (DELETE) a path below the
// work directory.
func (s *server) file(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(r.URL.Query().Get("path"))
	if rel, err := filepath.Rel(s.work, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !filepath.IsAbs(path) {
		http.Error(w, "path outside the test directory", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, path)
	case http.MethodPut:
		data, err := io.ReadAll(r.Body)
		if err == nil {
			err = os.WriteFile(path, data, 0o644)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case http.MethodDelete:
		if err := os.RemoveAll(path); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
