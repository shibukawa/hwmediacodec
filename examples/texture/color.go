package main

import "image/color"

func colorRGB(r, g, b uint8) color.RGBA { return color.RGBA{R: r, G: g, B: b, A: 255} }
