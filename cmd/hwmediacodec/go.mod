module github.com/shibukawa/hwmediacodec/cmd/hwmediacodec

go 1.27.0

toolchain go1.27.2

// The core module carries this command up to v0.3.2: when releasing, tag
// the core module without it first and require that version here, so that
// the two do not both provide the package.
require (
	github.com/hajimehoshi/ebiten/v2 v2.10.4
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c
	github.com/shibukawa/hwmediacodec v0.3.2
	github.com/shibukawa/hwmediacodec/ebitenvideo v0.3.1
	github.com/shibukawa/hwmediacodec/net/webrtc v0.3.1
	golang.org/x/image v0.45.0
)

require (
	github.com/Eyevinn/mp4ff v0.59.0 // indirect
	github.com/ebitengine/gomobile v0.0.0-20260820040257-d11f821a26a6 // indirect
	github.com/ebitengine/hideconsole v1.0.0 // indirect
	github.com/ebitengine/oto/v3 v3.5.0 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/jfreymuth/pulse v0.1.3 // indirect
	github.com/srwiley/rasterx v0.0.0-20220730225603-2ab79fcdd4ef // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)
