//go:build linux

package sys

import (
	"errors"
	"testing"
)

func requireLibrary(t *testing.T) {
	t.Helper()
	err := Load()
	if errors.Is(err, ErrNotAvailable) {
		t.Skipf("libvpl not installed: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestLoad checks that every entry point binds when libvpl is installed.
func TestLoad(t *testing.T) {
	requireLibrary(t)
	if MFXLoad == nil || MFXCreateSession == nil || DecodeFrameAsync == nil || EncodeFrameAsync == nil || CoreSyncOperation == nil {
		t.Fatal("symbols not bound")
	}
}

// TestDispatcherFilter drives the dispatcher as far as it goes without a
// GPU. MFXSetConfigFilterProperty receives its mfxVariant by value as two
// integer words; the dispatcher checks the variant's type against the
// property name, so a wrong word layout would be rejected here.
func TestDispatcherFilter(t *testing.T) {
	requireLibrary(t)
	loader := MFXLoad()
	if loader == 0 {
		t.Fatal("MFXLoad returned NULL")
	}
	defer MFXUnload(loader)
	cfg := MFXCreateConfig(loader)
	if cfg == 0 {
		t.Fatal("MFXCreateConfig returned NULL")
	}
	name := []byte("mfxImplDescription.Impl\x00")
	lo, hi := VariantU32(ImplTypeHardware)
	if st := MFXSetConfigFilterProperty(cfg, &name[0], lo, hi); st != ErrNone {
		t.Fatalf("MFXSetConfigFilterProperty(Impl=hardware) = %s", StatusString(st))
	}
	// The same property with a 16-bit variant type must be refused, which
	// shows the dispatcher reads the type from the first word.
	cfg2 := MFXCreateConfig(loader)
	const variantTypeU16 = 3
	if st := MFXSetConfigFilterProperty(cfg2, &name[0], uint64(VariantVersion)|variantTypeU16<<32, 2); st == ErrNone {
		t.Error("MFXSetConfigFilterProperty accepted a U16 variant for a U32 property")
	}

	var session uintptr
	st := MFXCreateSession(loader, 0, &session)
	if st != ErrNone {
		t.Logf("MFXCreateSession = %s (no Intel GPU or GPU runtime on this machine)", StatusString(st))
		return
	}
	defer MFXClose(session)
	var v Version
	if st := MFXQueryVersion(session, &v); st != ErrNone {
		t.Fatalf("MFXQueryVersion = %s", StatusString(st))
	}
	var impl int32
	MFXQueryIMPL(session, &impl)
	t.Logf("hardware session: API %d.%d, impl %#x", v.Major, v.Minor, impl)
}
