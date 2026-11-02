//go:build linux && !android

package main

import "os"

// preferX11EGL works around a crash of Ebitengine's window on Wayland
// desktops. Ebitengine opens its window through X11 (XWayland on such a
// desktop) and hands the X11 display to eglGetDisplay, which does not say
// what platform the pointer belongs to. Mesa then goes by EGL_PLATFORM, or
// by WAYLAND_DISPLAY when that is unset, and Wayland sessions often set
// EGL_PLATFORM=wayland for every program: Mesa takes the X11 display for a
// Wayland one and crashes in eglInitialize. The window is always an X11
// one, so EGL_PLATFORM is set to x11 for this process.
func preferX11EGL() {
	if os.Getenv("EGL_PLATFORM") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return
	}
	os.Setenv("EGL_PLATFORM", "x11")
}
