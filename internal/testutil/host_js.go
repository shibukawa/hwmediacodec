//go:build js

package testutil

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"testing"
)

// hostEnv names the variable the browserexec wrapper sets to the base URL
// of its endpoints. Without it (a plain "go test" under Node.js) the host
// is out of reach and the ffmpeg-based tests skip.
const hostEnv = "HWMEDIACODEC_TEST_HOST"

var errNoHost = errors.New("no test host: run the js/wasm tests with scripts/browser_test.sh")

func hostURL(endpoint string, query url.Values) (string, error) {
	base := os.Getenv(hostEnv)
	if base == "" {
		return "", errNoHost
	}
	u := base + endpoint
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u, nil
}

// call sends one request to the wrapper and returns the response body.
func call(method, endpoint string, query url.Values, body []byte) ([]byte, http.Header, error) {
	u, err := hostURL(endpoint, query)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest(method, u, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("test host: %s %s: %s: %s", method, endpoint, resp.Status, bytes.TrimSpace(data))
	}
	return data, resp.Header, nil
}

func runHost(name string, args []string) (stdout, stderr []byte, err error) {
	body, err := json.Marshal(map[string]any{"name": name, "args": args})
	if err != nil {
		return nil, nil, err
	}
	data, header, err := call(http.MethodPost, "exec", nil, body)
	if err != nil {
		return nil, nil, err
	}
	n, err := strconv.Atoi(header.Get("X-Stderr-Length"))
	if err != nil || n < 0 || n > len(data) {
		return nil, nil, fmt.Errorf("test host: malformed exec response for %s", name)
	}
	stderr, stdout = data[:n], data[n:]
	status, _ := base64.StdEncoding.DecodeString(header.Get("X-Exit-Status"))
	if string(status) != "0" {
		return stdout, stderr, errors.New(string(status))
	}
	return stdout, stderr, nil
}

func lookPath(name string) (string, error) {
	data, _, err := call(http.MethodGet, "lookpath", url.Values{"name": {name}}, nil)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func tempDir(t testing.TB) string {
	data, _, err := call(http.MethodPost, "mktemp", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := string(data)
	t.Cleanup(func() { call(http.MethodDelete, "file", url.Values{"path": {dir}}, nil) })
	return dir
}

func writeFile(path string, data []byte) error {
	_, _, err := call(http.MethodPut, "file", url.Values{"path": {path}}, data)
	return err
}

func readFile(path string) ([]byte, error) {
	data, _, err := call(http.MethodGet, "file", url.Values{"path": {path}}, nil)
	return data, err
}
