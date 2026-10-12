//go:build !js

package testutil

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
)

func runHost(name string, args []string) (stdout, stderr []byte, err error) {
	cmd := exec.Command(name, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

func lookPath(name string) (string, error) { return exec.LookPath(name) }

func tempDir(t testing.TB) string { return t.TempDir() }

func writeFile(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }
