//go:build linux

package sys

import (
	"errors"
	"testing"
)

// TestLoad checks that every symbol the backend needs can be bound when
// libva is installed. Without libva the test is skipped.
func TestLoad(t *testing.T) {
	err := Load()
	if errors.Is(err, ErrNotAvailable) {
		t.Skipf("libva not installed: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if GetDisplayDRM == nil || Initialize == nil || CreateBuffer == nil || DeriveImage == nil || PutImage == nil {
		t.Fatal("symbols not bound")
	}
	if s := StatusString(StatusErrorUnsupportedProfile); s == "" {
		t.Fatal("vaErrorStr returned an empty string")
	}
	t.Logf("vaErrorStr(unsupported profile) = %q", StatusString(StatusErrorUnsupportedProfile))
}
