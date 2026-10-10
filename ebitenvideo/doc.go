// Package ebitenvideo plays hardware-decoded video in Ebitengine.
//
// It is a separate Go module (github.com/shibukawa/hwmediacodec/ebitenvideo)
// so that the core hwmediacodec module stays free of the Ebitengine
// dependency. The decoding and the timing are the core module's playback
// package; a Player here drives one from the game loop and keeps the frame
// that is due at the current playback position in an *ebiten.Image:
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
// NewPlayer reads a raw Annex-B elementary stream, which carries no
// timestamps, so the frame rate is a parameter. NewPlayerFromSource takes
// a hwmediacodec.PacketReader instead: anything that hands out access
// units with presentation times, such as the MP4 demuxer
// (mediacontainer/mp4, VideoTrack.PacketSource). A reader that is a
// hwmediacodec.PacketSeeker lets the player loop, report its Length and
// Seek; an io.ReadSeeker given to NewPlayer gets that too, by scanning the
// stream once for keyframes.
//
// Playback advances by one tick per Update (1/TPS seconds, or wall-clock
// time when the TPS is SyncWithFPS); when decoding falls behind, frames
// are skipped so that the picture stays in time. Seek restarts decoding at
// the keyframe before the target and drops the frames up to it, so the
// next picture shown is the one at the target.
package ebitenvideo
