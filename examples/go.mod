module github.com/shibukawa/hwmediacodec/examples

go 1.27.0

replace github.com/shibukawa/hwmediacodec => ../

require (
	github.com/Eyevinn/mp4ff v0.59.0
	github.com/hajimehoshi/ebiten/v2 v2.10.4
	github.com/shibukawa/hwmediacodec v0.0.0
	github.com/shibukawa/hwmediacodec/ebitenvideo v0.0.0
)

require (
	github.com/ebitengine/gomobile v0.0.0-20260820040257-d11f821a26a6 // indirect
	github.com/ebitengine/hideconsole v1.0.0 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

replace github.com/shibukawa/hwmediacodec/ebitenvideo => ../ebitenvideo
