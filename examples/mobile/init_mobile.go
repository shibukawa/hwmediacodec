//go:build android || ios

package mobile

import "github.com/hajimehoshi/ebiten/v2/mobile"

// On a phone or tablet the game starts as soon as the bound library is
// loaded; the native view controller (ios/) only has to show it.
func init() {
	mobile.SetGame(newGame())
}
