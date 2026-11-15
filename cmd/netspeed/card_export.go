package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"netspeed/internal/config"
	"netspeed/internal/engine"
)

type cardFonts struct {
	title   font.Face
	metric  font.Face
	heading font.Face
	body    font.Face
	small   font.Face
}

func loadCardFonts() (cardFonts, error) {
	regTTF, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return cardFonts{}, err
	}
	boldTTF, err := opentype.Parse(gobold.TTF)
	if err != nil {
		return cardFonts{}, err
	}

	makeFace := func(f *opentype.Font, size float64) (font.Face, error) {
		return opentype.NewFace(f, &opentype.FaceOptions{
			Size:    size,
			DPI:     72,
			Hinting: font.HintingFull,
		})
	}

	var cf cardFonts
	if cf.title, err = makeFace(boldTTF, 22); err != nil {
		return cf, err
	}
	if cf.metric, err = makeFace(boldTTF, 30); err != nil {
		return cf, err
	}
	if cf.heading, err = makeFace(boldTTF, 12); err != nil {
		return cf, err
	}
	if cf.body, err = makeFace(regTTF, 13); err != nil {
		return cf, err
	}
	if cf.small, err = makeFace(regTTF, 11); err != nil {
		return cf, err
	}
	return cf, nil
}

func (f *cardFonts) close() {
	for _, face := range []font.Face{f.title, f.metric, f.heading, f.body, f.small} {
		if face != nil {
			_ = face.Close()
		}
	}
}

func gradeAccent(pal AppPalette, grade string) color.NRGBA {
	switch grade {
	case "A+", "A":
		return pal.Ping
	case "B":
		return pal.Download
	default:
		return pal.Danger
	}
}

// ExportResultCardPNG renders a shareable summary card PNG for the given test result
// using the current Dark/Light palette and returns the saved file path.
func ExportResultCardPNG(res engine.Result, unitName string, pal AppPalette, hideIP bool) (string, error) {
	fonts, err := loadCardFonts()
	if err != nil {
		return "", err
	}
	defer fonts.close()

	const (
		w = 840
		h = 460
	)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fillSolid(img, img.Bounds(), pal.Background)

	// Top gradient bar from Download (cyan) to Upload (violet).
	for x := 0; x < w; x++ {
		t := float32(x) / float32(w-1)
		c := blendColor(pal.Download, pal.Upload, t)
		for y := 0; y < 4; y++ {
			img.SetRGBA(x, y, color.RGBA{R: c.R, G: c.G, B: c.B, A: 255})
		}
	}

	// Header section.
	drawTextLine(img, fonts.title, 26, 46, "NetSpeed", pal.TextPrimary)
	trig := strings.ToUpper(res.Trigger)
	if trig == "" {
		trig = "MANUAL"
	}
	meta := fmt.Sprintf("%s  ·  %s  ·  %s",
		res.Timestamp.Local().Format("2006-01-02 15:04"),
		strings.ToUpper(string(res.Provider)),
		trig,
	)
	drawTextLine(img, fonts.body, 26, 68, meta, pal.TextSecondary)

	if res.BufferbloatGrade != "" {
		gAccent := gradeAccent(pal, res.BufferbloatGrade)
		badgeTxt := "Grade " + res.BufferbloatGrade
		tw := measureTextWidth(fonts.heading, badgeTxt)
		bw := tw + 24
		bx := w - 26 - bw
		by := 28
		drawRoundedCard(img, image.Rect(bx, by, bx+bw, by+30), 8, withAlpha(gAccent, 32), withAlpha(gAccent, 160))
		drawTextLine(img, fonts.heading, bx+12, by+20, badgeTxt, gAccent)
	}

	// Metric cards row.
	const (
		marginX = 26
		gapX    = 14
		cardsY  = 92
		cardsH  = 258
	)
	cardW := (w - 2*marginX - 2*gapX) / 3

	// Card 1: Ping, Jitter, Packet Loss, Loaded Ping.
	c1 := image.Rect(marginX, cardsY, marginX+cardW, cardsY+cardsH)
	drawRoundedCard(img, c1, 12, pal.Surface, pal.Border)
	drawTextLine(img, fonts.heading, c1.Min.X+18, c1.Min.Y+28, "PING & JITTER", pal.Ping)
	drawValueWithUnit(img, fonts.metric, fonts.body, c1.Min.X+18, c1.Min.Y+74, fmt.Sprintf("%.1f", res.PingMS), "ms", pal.TextPrimary, pal.TextSecondary)
	drawTextLine(img, fonts.body, c1.Min.X+18, c1.Min.Y+116, fmt.Sprintf("Jitter: %.2f ms", res.JitterMS), pal.TextSecondary)
	drawTextLine(img, fonts.body, c1.Min.X+18, c1.Min.Y+140, fmt.Sprintf("Packet Loss: %.1f%%", res.PacketLossPct), pal.TextSecondary)
	if res.DownloadPingMS > 0 || res.UploadPingMS > 0 {
		drawTextLine(img, fonts.small, c1.Min.X+18, c1.Min.Y+174, fmt.Sprintf("Loaded DL: %.0f ms", res.DownloadPingMS), pal.TextMuted)
		drawTextLine(img, fonts.small, c1.Min.X+18, c1.Min.Y+194, fmt.Sprintf("Loaded UL: %.0f ms", res.UploadPingMS), pal.TextMuted)
	} else {
		drawTextLine(img, fonts.small, c1.Min.X+18, c1.Min.Y+174, "Loaded Ping: —", pal.TextMuted)
	}
	if res.BufferbloatGrade != "" {
		drawTextLine(img, fonts.small, c1.Min.X+18, c1.Min.Y+224, fmt.Sprintf("Bufferbloat Grade: %s", res.BufferbloatGrade), gradeAccent(pal, res.BufferbloatGrade))
	}

	// Card 2: Download.
	c2 := image.Rect(c1.Max.X+gapX, cardsY, c1.Max.X+gapX+cardW, cardsY+cardsH)
	drawTransferMetricCard(img, fonts, pal, c2, "DOWNLOAD", pal.Download, res.DownloadMbps, unitName, res.DownloadBytes, res.DownloadSamples)

	// Card 3: Upload.
	c3 := image.Rect(c2.Max.X+gapX, cardsY, w-marginX, cardsY+cardsH)
	drawTransferMetricCard(img, fonts, pal, c3, "UPLOAD", pal.Upload, res.UploadMbps, unitName, res.UploadBytes, res.UploadSamples)

	// Footer card.
	footer := image.Rect(marginX, 366, w-marginX, h-24)
	drawRoundedCard(img, footer, 12, pal.Surface, pal.Border)
	ipStr := res.Client.IP
	if ipStr == "" {
		ipStr = "—"
	} else if hideIP {
		ipStr = "•••.•••.•••.•••"
	}
	ispStr := res.Client.ISP
	if ispStr == "" {
		ispStr = "Network"
	}
	drawTextLine(img, fonts.body, footer.Min.X+18, footer.Min.Y+26, fmt.Sprintf("%s  ·  %s", ispStr, ipStr), pal.TextPrimary)
	srvStr := fmt.Sprintf("Server: %s", res.Server.Name)
	if res.Server.DistanceKM > 0 {
		srvStr = fmt.Sprintf("Server: %s [%.0f km]", res.Server.Name, res.Server.DistanceKM)
	}
	drawTextLine(img, fonts.small, footer.Min.X+18, footer.Min.Y+48, srvStr, pal.TextSecondary)

	durTxt := fmt.Sprintf("%.1fs test", float64(res.DurationMS)/1000.0)
	dw := measureTextWidth(fonts.small, durTxt)
	drawTextLine(img, fonts.small, footer.Max.X-18-dw, footer.Min.Y+48, durTxt, pal.TextMuted)

	// Write PNG output under %AppData%\NetSpeed\cards.
	outDir := filepath.Join(config.AppDir(), "cards")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	outPath := filepath.Join(outDir, fmt.Sprintf("netspeed-%s.png", res.Timestamp.Local().Format("20060102-150405")))
	f, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	encodeErr := png.Encode(f, img)
	closeErr := f.Close()
	if encodeErr != nil {
		return "", encodeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return outPath, nil
}

func drawTransferMetricCard(
	img *image.RGBA,
	fonts cardFonts,
	pal AppPalette,
	r image.Rectangle,
	title string,
	accent color.NRGBA,
	mbps float64,
	unitName string,
	bytes int64,
	samples []float64,
) {
	drawRoundedCard(img, r, 12, pal.Surface, pal.Border)
	drawTextLine(img, fonts.heading, r.Min.X+18, r.Min.Y+28, title, accent)
	if bytes > 0 {
		mbTxt := fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
		tw := measureTextWidth(fonts.small, mbTxt)
		drawTextLine(img, fonts.small, r.Max.X-18-tw, r.Min.Y+28, mbTxt, pal.TextMuted)
	}
	val, uLabel := engine.FormatSpeed(mbps, unitName)
	valStr := "—"
	if mbps > 0 {
		valStr = fmt.Sprintf("%.2f", val)
	}
	drawValueWithUnit(img, fonts.metric, fonts.body, r.Min.X+18, r.Min.Y+74, valStr, uLabel, pal.TextPrimary, pal.TextSecondary)

	sparkRect := image.Rect(r.Min.X+18, r.Min.Y+140, r.Max.X-18, r.Max.Y-22)
	drawCardSparkline(img, pal, sparkRect, samples, accent)
}

func drawValueWithUnit(img *image.RGBA, valFace, unitFace font.Face, x, y int, val, unitStr string, valCol, unitCol color.NRGBA) {
	drawTextLine(img, valFace, x, y, val, valCol)
	vw := measureTextWidth(valFace, val)
	drawTextLine(img, unitFace, x+vw+8, y, unitStr, unitCol)
}

func drawTextLine(img *image.RGBA, face font.Face, x, y int, txt string, c color.NRGBA) {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(txt)
}

func measureTextWidth(face font.Face, txt string) int {
	return font.MeasureString(face, txt).Ceil()
}

func fillSolid(img *image.RGBA, rect image.Rectangle, c color.NRGBA) {
	b := rect.Intersect(img.Bounds())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.Set(x, y, c)
		}
	}
}

func blendPixel(img *image.RGBA, x, y int, c color.NRGBA) {
	if !image.Pt(x, y).In(img.Bounds()) || c.A == 0 {
		return
	}
	if c.A == 255 {
		img.SetRGBA(x, y, color.RGBA{R: c.R, G: c.G, B: c.B, A: 255})
		return
	}
	bg := img.RGBAAt(x, y)
	alpha := float32(c.A) / 255.0
	inv := 1.0 - alpha
	out := color.RGBA{
		R: uint8(float32(c.R)*alpha + float32(bg.R)*inv),
		G: uint8(float32(c.G)*alpha + float32(bg.G)*inv),
		B: uint8(float32(c.B)*alpha + float32(bg.B)*inv),
		A: 255,
	}
	img.SetRGBA(x, y, out)
}

func drawRoundedCard(img *image.RGBA, rect image.Rectangle, radius int, fill, border color.NRGBA) {
	rf := float64(radius)
	x0, y0 := float64(rect.Min.X), float64(rect.Min.Y)
	x1, y1 := float64(rect.Max.X-1), float64(rect.Max.Y-1)

	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			fx, fy := float64(x), float64(y)
			cx := math.Max(x0+rf, math.Min(fx, x1-rf))
			cy := math.Max(y0+rf, math.Min(fy, y1-rf))
			dist := math.Hypot(fx-cx, fy-cy)
			if dist <= rf {
				if dist >= rf-1.1 {
					blendPixel(img, x, y, border)
				} else {
					blendPixel(img, x, y, fill)
				}
			}
		}
	}
}

func drawCardSparkline(img *image.RGBA, pal AppPalette, rect image.Rectangle, samples []float64, accent color.NRGBA) {
	for x := rect.Min.X; x <= rect.Max.X; x++ {
		blendPixel(img, x, rect.Max.Y, pal.Border)
	}
	if len(samples) < 2 || rect.Dx() <= 4 || rect.Dy() <= 4 {
		return
	}
	maxV := 1.0
	for _, s := range samples {
		if s > maxV {
			maxV = s
		}
	}
	maxV *= 1.12

	w := float64(rect.Dx())
	h := float64(rect.Dy())
	fillCol := withAlpha(accent, 42)
	pts := make([]image.Point, len(samples))
	for i, s := range samples {
		px := rect.Min.X + int(math.Round(float64(i)/float64(len(samples)-1)*w))
		py := rect.Min.Y + int(math.Round(h*(1.0-s/maxV)))
		if py < rect.Min.Y {
			py = rect.Min.Y
		}
		if py > rect.Max.Y {
			py = rect.Max.Y
		}
		pts[i] = image.Pt(px, py)
	}

	// Fill area beneath line segments.
	for i := 0; i < len(pts)-1; i++ {
		p0, p1 := pts[i], pts[i+1]
		dx := p1.X - p0.X
		if dx <= 0 {
			continue
		}
		for x := p0.X; x <= p1.X; x++ {
			t := float64(x-p0.X) / float64(dx)
			yTop := int(math.Round(float64(p0.Y) + t*float64(p1.Y-p0.Y)))
			for y := yTop; y < rect.Max.Y; y++ {
				blendPixel(img, x, y, fillCol)
			}
		}
	}
	// Draw crisp 2px line stroke.
	for i := 0; i < len(pts)-1; i++ {
		drawThickLine(img, pts[i], pts[i+1], accent, 1.3)
	}
}

func drawThickLine(img *image.RGBA, a, b image.Point, c color.NRGBA, radius float64) {
	minX := min(a.X, b.X) - int(math.Ceil(radius)) - 1
	maxX := max(a.X, b.X) + int(math.Ceil(radius)) + 1
	minY := min(a.Y, b.Y) - int(math.Ceil(radius)) - 1
	maxY := max(a.Y, b.Y) + int(math.Ceil(radius)) + 1

	abx, aby := float64(b.X-a.X), float64(b.Y-a.Y)
	abLenSq := abx*abx + aby*aby

	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			var dist float64
			if abLenSq == 0 {
				dist = math.Hypot(float64(x-a.X), float64(y-a.Y))
			} else {
				t := ((float64(x-a.X) * abx) + (float64(y-a.Y) * aby)) / abLenSq
				if t < 0 {
					t = 0
				} else if t > 1 {
					t = 1
				}
				px := float64(a.X) + t*abx
				py := float64(a.Y) + t*aby
				dist = math.Hypot(float64(x)-px, float64(y)-py)
			}
			if dist <= radius {
				blendPixel(img, x, y, c)
			}
		}
	}
}
