//go:build windows

package main

import (
	"image/color"

	"netspeed/internal/card"
	"netspeed/internal/engine"
)

func toCardPalette(pal AppPalette) card.Palette {
	return card.Palette{
		Dark:          pal.Dark,
		Background:    pal.Background,
		Surface:       pal.Surface,
		Elevated:      pal.Elevated,
		Border:        pal.Border,
		Track:         pal.Track,
		TextPrimary:   pal.TextPrimary,
		TextSecondary: pal.TextSecondary,
		TextMuted:     pal.TextMuted,
		Download:      pal.Download,
		Upload:        pal.Upload,
		Ping:          pal.Ping,
		Danger:        pal.Danger,
		ButtonText:    pal.ButtonText,
	}
}

func gradeAccent(pal AppPalette, grade string) color.NRGBA {
	return card.GradeAccent(toCardPalette(pal), grade)
}

// ExportResultCardPNG renders a shareable summary card PNG for the given test result
// using the current Dark/Light palette and returns the saved file path.
func ExportResultCardPNG(res engine.Result, unitName string, pal AppPalette, hideIP bool) (string, error) {
	return card.ExportResultCardPNG(res, unitName, toCardPalette(pal), hideIP)
}
