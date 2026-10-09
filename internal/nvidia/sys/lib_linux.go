package sys

import "github.com/ebitengine/purego"

// Library names of the proprietary driver, most specific first.
var (
	cudaLibraries  = []string{"libcuda.so.1", "libcuda.so"}
	cuvidLibraries = []string{"libnvcuvid.so.1", "libnvcuvid.so"}
	nvencLibraries = []string{"libnvidia-encode.so.1", "libnvidia-encode.so"}
)

func openLibrary(name string) (uintptr, error) {
	return purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}

func lookup(handle uintptr, name string) (uintptr, error) {
	return purego.Dlsym(handle, name)
}
