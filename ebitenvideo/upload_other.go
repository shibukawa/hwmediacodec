//go:build !js

package ebitenvideo

import "github.com/shibukawa/hwmediacodec"

// directUpload is the browser's shortcut from the decoder to the texture;
// there is none outside the browser.
type directUpload struct{}

func (directUpload) used() bool { return false }

// uploadFormat is the frame format the player asks the decoder for.
func uploadFormat() hwmediacodec.PixelFormat { return hwmediacodec.RGBA }

func (p *Player) upload(f *hwmediacodec.Frame) { p.uploadPixels(f) }
