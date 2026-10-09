// Package ebitenvideo plays hardware-decoded H.264 and HEVC video in
// Ebitengine.
//
// It is a separate Go module (github.com/shibukawa/hwmediacodec/ebitenvideo)
// so that the core hwmediacodec module stays free of the Ebitengine
// dependency. A Player decodes an Annex-B elementary stream on a background
// goroutine through hwmediacodec, in display order and as RGBA, and keeps
// the frame that is due at the current playback position in an
// *ebiten.Image:
//
//	player, err := ebitenvideo.NewPlayer(file, hwmediacodec.H264, 30)
//	...
//	player.Play()
//
//	func (g *game) Update() error { return player.Update() }
//	func (g *game) Draw(screen *ebiten.Image) {
//		if img := player.Image(); img != nil {
//			screen.DrawImage(img, nil)
//		}
//	}
//
// Elementary streams carry no timestamps, so the frame rate is given to
// NewPlayer. Playback advances by one tick per Update (1/TPS seconds, or
// wall-clock time when the TPS is SyncWithFPS); when decoding falls behind,
// frames are skipped so that the picture stays in time.
package ebitenvideo
