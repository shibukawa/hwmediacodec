//go:build !linux || android

package main

// preferX11EGL is needed on Linux only; see egl_linux.go.
func preferX11EGL() {}
