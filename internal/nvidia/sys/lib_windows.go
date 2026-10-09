//go:build amd64

package sys

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Library names of the NVIDIA display driver. The driver installs all three
// in the system directory; there is no build of them for Windows on ARM.
var (
	cudaLibraries  = []string{"nvcuda.dll"}
	cuvidLibraries = []string{"nvcuvid.dll"}
	nvencLibraries = []string{"nvEncodeAPI64.dll"}
)

// openLibrary loads a driver DLL from the system directory only, so that a
// DLL of the same name next to the executable or in the working directory
// is never picked up.
func openLibrary(name string) (uintptr, error) {
	h, err := windows.LoadLibraryEx(name, 0, windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return uintptr(h), nil
}

func lookup(handle uintptr, name string) (uintptr, error) {
	return windows.GetProcAddress(windows.Handle(handle), name)
}
