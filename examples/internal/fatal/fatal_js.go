//go:build js

package fatal

import (
	"image/color"
	"log"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
)

const width, height = 640, 360

type screen struct{ text string }

func (s *screen) Update() error { return nil }

func (s *screen) Draw(dst *ebiten.Image) {
	dst.Fill(color.RGBA{18, 18, 22, 255})
	ebitenutil.DebugPrintAt(dst, s.text, 16, 16)
}

func (s *screen) Layout(int, int) (int, int) { return width, height }

// Exit shows err in the page and keeps the program alive to show it.
func Exit(err error) {
	log.Print(err)
	// The debug font is 6 pixels wide.
	text := wrap("This browser cannot run the sample:\n\n"+err.Error(), (width-32)/6)
	if runErr := ebiten.RunGame(&screen{text: text}); runErr != nil {
		log.Print(runErr)
	}
}
