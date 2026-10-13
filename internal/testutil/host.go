package testutil

import (
	"bytes"
	"testing"
)

// The helpers in this file are the package's only way to the host: the
// tools it runs (ffmpeg, ffprobe, x265, aomenc) and the files those read
// and write. Natively they are os/exec and os. In a browser (GOOS=js),
// which has neither, they go over HTTP to the wrapper that started the
// test (internal/browserexec), so the same conformance tests run against
// the WebCodecs backend with ffmpeg on the host as the oracle. Paths are
// host paths either way.

// Cmd is the part of exec.Cmd the tests use.
type Cmd struct {
	name string
	args []string
	// Stderr, when set, receives the tool's standard error.
	Stderr *bytes.Buffer
}

// Command prepares a host tool invocation, like exec.Command.
func Command(name string, args ...string) *Cmd {
	return &Cmd{name: name, args: args}
}

// Output runs the command and returns its standard output.
func (c *Cmd) Output() ([]byte, error) {
	stdout, stderr, err := runHost(c.name, c.args)
	if c.Stderr != nil {
		c.Stderr.Write(stderr)
	}
	return stdout, err
}

// Run runs the command.
func (c *Cmd) Run() error {
	_, err := c.Output()
	return err
}

// CombinedOutput runs the command and returns its standard error followed
// by its standard output. (The tools either log or print, so the two are
// not interleaved as exec.Cmd would.)
func (c *Cmd) CombinedOutput() ([]byte, error) {
	stdout, stderr, err := runHost(c.name, c.args)
	return append(stderr, stdout...), err
}

// LookPath reports whether the host has the named tool, like
// exec.LookPath. The result is what Command takes.
func LookPath(name string) (string, error) { return lookPath(name) }

// TempDir returns a fresh directory on the host that is removed when the
// test ends, like t.TempDir.
func TempDir(t testing.TB) string {
	t.Helper()
	return tempDir(t)
}

// WriteFile writes a file on the host or fails the test.
func WriteFile(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := writeFile(path, data); err != nil {
		t.Fatal(err)
	}
}

// ReadFile reads a whole file from the host or fails the test.
func ReadFile(t testing.TB, path string) []byte {
	t.Helper()
	b, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
