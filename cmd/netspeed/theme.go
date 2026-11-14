package main

import (
	"image/color"

	"gioui.org/font/gofont"
	"gioui.org/text"
	"gioui.org/widget/material"
)

// AppPalette holds semantic UI colors for minimalist Dark and Light themes.
type AppPalette struct {
	Dark          bool
	Background    color.NRGBA
	Surface       color.NRGBA
	Elevated      color.NRGBA
	Border        color.NRGBA
	Track         color.NRGBA
	TextPrimary   color.NRGBA
	TextSecondary color.NRGBA
	TextMuted     color.NRGBA
	Download      color.NRGBA
	Upload        color.NRGBA
	Ping          color.NRGBA
	Danger        color.NRGBA
	ButtonText    color.NRGBA
}

func hexColor(r, g, b uint8) color.NRGBA {
	return color.NRGBA{R: r, G: g, B: b, A: 255}
}

func withAlpha(c color.NRGBA, a uint8) color.NRGBA {
	c.A = a
	return c
}

func blendColor(a, b color.NRGBA, t float32) color.NRGBA {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	return color.NRGBA{
		R: uint8(float32(a.R)*(1-t) + float32(b.R)*t),
		G: uint8(float32(a.G)*(1-t) + float32(b.G)*t),
		B: uint8(float32(a.B)*(1-t) + float32(b.B)*t),
		A: uint8(float32(a.A)*(1-t) + float32(b.A)*t),
	}
}

func DarkPalette() AppPalette {
	return AppPalette{
		Dark:          true,
		Background:    hexColor(0x0B, 0x0F, 0x17),
		Surface:       hexColor(0x13, 0x1C, 0x2E),
		Elevated:      hexColor(0x1B, 0x27, 0x40),
		Border:        hexColor(0x23, 0x32, 0x4F),
		Track:         hexColor(0x1E, 0x29, 0x3B),
		TextPrimary:   hexColor(0xF8, 0xFA, 0xFC),
		TextSecondary: hexColor(0x94, 0xA3, 0xB8),
		TextMuted:     hexColor(0x64, 0x74, 0x8B),
		Download:      hexColor(0x38, 0xBD, 0xF8),
		Upload:        hexColor(0xA7, 0x8B, 0xFA),
		Ping:          hexColor(0x34, 0xD3, 0x99),
		Danger:        hexColor(0xF8, 0x71, 0x71),
		ButtonText:    hexColor(0x0B, 0x0F, 0x17),
	}
}

func LightPalette() AppPalette {
	return AppPalette{
		Dark:          false,
		Background:    hexColor(0xF8, 0xFA, 0xFC),
		Surface:       hexColor(0xFF, 0xFF, 0xFF),
		Elevated:      hexColor(0xF1, 0xF5, 0xF9),
		Border:        hexColor(0xE2, 0xE8, 0xF0),
		Track:         hexColor(0xE2, 0xE8, 0xF0),
		TextPrimary:   hexColor(0x0F, 0x17, 0x2A),
		TextSecondary: hexColor(0x47, 0x55, 0x69),
		TextMuted:     hexColor(0x94, 0xA3, 0xB8),
		Download:      hexColor(0x02, 0x84, 0xC7),
		Upload:        hexColor(0x7C, 0x3A, 0xED),
		Ping:          hexColor(0x05, 0x96, 0x69),
		Danger:        hexColor(0xDC, 0x26, 0x26),
		ButtonText:    hexColor(0xFF, 0xFF, 0xFF),
	}
}

func newMaterialTheme(pal AppPalette) *material.Theme {
	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	applyMaterialPalette(th, pal)
	return th
}

func applyMaterialPalette(th *material.Theme, pal AppPalette) {
	th.Palette = material.Palette{
		Bg:         pal.Background,
		Fg:         pal.TextPrimary,
		ContrastBg: pal.Download,
		ContrastFg: pal.ButtonText,
	}
}
