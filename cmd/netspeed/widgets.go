package main

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"netspeed/internal/engine"
)

var gaugeScaleStops = []float64{0, 5, 10, 25, 50, 100, 250, 500, 1000}

// speedToFraction maps Mbps onto [0, 1] using a non-linear Speedtest-style scale.
func speedToFraction(mbps float64) float32 {
	if mbps <= 0 {
		return 0
	}
	last := gaugeScaleStops[len(gaugeScaleStops)-1]
	if mbps >= last {
		return 1
	}
	segCount := float64(len(gaugeScaleStops) - 1)
	for i := 0; i < len(gaugeScaleStops)-1; i++ {
		lo := gaugeScaleStops[i]
		hi := gaugeScaleStops[i+1]
		if mbps <= hi {
			local := (mbps - lo) / (hi - lo)
			return float32((float64(i) + local) / segCount)
		}
	}
	return 1
}

func fractionToAngleRad(frac float32) float64 {
	// 240 degree arc: starts at 150 deg (bottom-left), sweeps clockwise through 270 deg (top) to 390 deg (bottom-right).
	const startDeg = 150.0
	const sweepDeg = 240.0
	deg := startDeg + float64(frac)*sweepDeg
	return deg * math.Pi / 180.0
}

func buildArcPath(ops *op.Ops, cx, cy, r float32, fromFrac, toFrac float32, steps int) (clip.PathSpec, f32.Point, f32.Point) {
	var p clip.Path
	p.Begin(ops)
	if steps < 4 {
		steps = 4
	}
	var startPt, endPt f32.Point
	for i := 0; i <= steps; i++ {
		t := float32(i) / float32(steps)
		f := fromFrac + (toFrac-fromFrac)*t
		ang := fractionToAngleRad(f)
		pt := f32.Pt(
			cx+r*float32(math.Cos(ang)),
			cy+r*float32(math.Sin(ang)),
		)
		if i == 0 {
			p.MoveTo(pt)
			startPt = pt
		} else {
			p.LineTo(pt)
		}
		endPt = pt
	}
	return p.End(), startPt, endPt
}

func fillDot(ops *op.Ops, center f32.Point, radius float32, c color.NRGBA) {
	if radius <= 0.5 {
		return
	}
	rect := image.Rect(
		int(math.Round(float64(center.X-radius))),
		int(math.Round(float64(center.Y-radius))),
		int(math.Round(float64(center.X+radius))),
		int(math.Round(float64(center.Y+radius))),
	)
	paint.FillShape(ops, c, clip.Ellipse(rect).Op(ops))
}

// GaugeState describes the animated state rendered by DrawSpeedGauge.
type GaugeState struct {
	Phase        engine.Phase
	DisplayMbps  float64
	Unit         string
	Progress     float64
	PingMS       float64
	LoadedPingMS float64
	StatusText   string
	Running      bool
}

// DrawSpeedGauge renders the 240-degree minimalist radial speedometer gauge and central GO/STOP button.
func DrawSpeedGauge(
	gtx layout.Context,
	th *material.Theme,
	pal AppPalette,
	state GaugeState,
	actionBtn *widget.Clickable,
) layout.Dimensions {
	sizePx := gtx.Dp(unit.Dp(290))
	if maxW := gtx.Constraints.Max.X; maxW > 0 && maxW < sizePx {
		sizePx = maxW
	}
	if maxH := gtx.Constraints.Max.Y - gtx.Dp(unit.Dp(28)); maxH > 0 && maxH < sizePx {
		sizePx = maxH
	}
	if sizePx < gtx.Dp(unit.Dp(180)) {
		sizePx = gtx.Dp(unit.Dp(180))
	}

	gtx.Constraints = layout.Exact(image.Pt(sizePx, sizePx))
	return layout.Stack{Alignment: layout.Center}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			cx := float32(sizePx) / 2
			cy := float32(sizePx) / 2
			radius := float32(sizePx) * 0.41
			strokeW := float32(gtx.Dp(unit.Dp(12)))

			// Background track arc.
			trackSpec, tStart, tEnd := buildArcPath(gtx.Ops, cx, cy, radius, 0, 1, 72)
			paint.FillShape(gtx.Ops, pal.Track, clip.Stroke{Path: trackSpec, Width: strokeW}.Op())
			fillDot(gtx.Ops, tStart, strokeW/2, pal.Track)
			fillDot(gtx.Ops, tEnd, strokeW/2, pal.Track)

			// Scale tick marks around the inner perimeter.
			tickRInner := radius - float32(gtx.Dp(unit.Dp(16)))
			tickROuter := radius - float32(gtx.Dp(unit.Dp(10)))
			for i := 0; i < len(gaugeScaleStops); i++ {
				frac := float32(i) / float32(len(gaugeScaleStops)-1)
				ang := fractionToAngleRad(frac)
				cosA, sinA := float32(math.Cos(ang)), float32(math.Sin(ang))

				var tp clip.Path
				tp.Begin(gtx.Ops)
				tp.MoveTo(f32.Pt(cx+tickRInner*cosA, cy+tickRInner*sinA))
				tp.LineTo(f32.Pt(cx+tickROuter*cosA, cy+tickROuter*sinA))
				paint.FillShape(gtx.Ops, withAlpha(pal.TextMuted, 160), clip.Stroke{
					Path:  tp.End(),
					Width: float32(gtx.Dp(unit.Dp(1.5))),
				}.Op())
			}

			// Determine active accent color and fraction.
			accent := pal.Download
			if state.Phase == engine.PhaseUpload {
				accent = pal.Upload
			} else if state.Phase == engine.PhasePing || state.Phase == engine.PhaseInit {
				accent = pal.Ping
			}

			frac := speedToFraction(state.DisplayMbps)
			if state.Phase == engine.PhasePing && state.Running {
				frac = float32(state.Progress) * 0.25
			}
			if frac > 0.005 {
				// Soft outer glow behind active arc.
				glowSpec, _, _ := buildArcPath(gtx.Ops, cx, cy, radius, 0, frac, 64)
				paint.FillShape(gtx.Ops, withAlpha(accent, 42), clip.Stroke{
					Path:  glowSpec,
					Width: strokeW + float32(gtx.Dp(unit.Dp(8))),
				}.Op())

				activeSpec, aStart, aEnd := buildArcPath(gtx.Ops, cx, cy, radius, 0, frac, 64)
				paint.FillShape(gtx.Ops, accent, clip.Stroke{Path: activeSpec, Width: strokeW}.Op())
				fillDot(gtx.Ops, aStart, strokeW/2, accent)
				fillDot(gtx.Ops, aEnd, strokeW/2, accent)

				// Crisp indicator tip dot + thin needle pointer.
				ang := fractionToAngleRad(frac)
				cosA, sinA := float32(math.Cos(ang)), float32(math.Sin(ang))
				innerNeedle := radius * 0.56
				outerNeedle := radius - strokeW*0.65
				var np clip.Path
				np.Begin(gtx.Ops)
				np.MoveTo(f32.Pt(cx+innerNeedle*cosA, cy+innerNeedle*sinA))
				np.LineTo(f32.Pt(cx+outerNeedle*cosA, cy+outerNeedle*sinA))
				paint.FillShape(gtx.Ops, withAlpha(accent, 210), clip.Stroke{
					Path:  np.End(),
					Width: float32(gtx.Dp(unit.Dp(2.5))),
				}.Op())
				fillDot(gtx.Ops, aEnd, strokeW*0.36, pal.TextPrimary)
			}

			// Scale labels at key stops (0, 25, 100, 500, 1000).
			labelIndices := []int{0, 3, 5, 7, 8}
			labelR := radius + float32(gtx.Dp(unit.Dp(20)))
			for _, idx := range labelIndices {
				fracStop := float32(idx) / float32(len(gaugeScaleStops)-1)
				ang := fractionToAngleRad(fracStop)
				lx := int(cx + labelR*float32(math.Cos(ang)))
				ly := int(cy + labelR*float32(math.Sin(ang)))
				txt := fmt.Sprintf("%.0f", gaugeScaleStops[idx])
				if idx == len(gaugeScaleStops)-1 {
					txt = "1G+"
				}
				drawAnchoredText(gtx, th, txt, pal.TextMuted, unit.Sp(11), lx, ly)
			}

			return layout.Dimensions{Size: image.Pt(sizePx, sizePx)}
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			if !state.Running && (state.Phase == engine.PhaseIdle || state.Phase == engine.PhaseDone || state.Phase == engine.PhaseError) {
				// Center circular GO / RE-TEST button.
				btnSize := gtx.Dp(unit.Dp(132))
				return actionBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					rect := image.Rect(0, 0, btnSize, btnSize)
					bg := pal.Surface
					borderCol := pal.Download
					if actionBtn.Hovered() {
						bg = pal.Elevated
					}
					paint.FillShape(gtx.Ops, withAlpha(pal.Download, 24), clip.Ellipse(image.Rect(-8, -8, btnSize+8, btnSize+8)).Op(gtx.Ops))
					paint.FillShape(gtx.Ops, bg, clip.Ellipse(rect).Op(gtx.Ops))
					paint.FillShape(gtx.Ops, borderCol, clip.Stroke{
						Path:  clip.Ellipse(rect).Path(gtx.Ops),
						Width: float32(gtx.Dp(unit.Dp(2.5))),
					}.Op())

					lblText := "GO"
					subText := "START TEST"
					if state.Phase == engine.PhaseDone {
						lblText = "GO"
						subText = "TEST AGAIN"
					} else if state.Phase == engine.PhaseError {
						lblText = "RETRY"
						subText = "RUN TEST"
					}

					return func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints = layout.Exact(image.Pt(btnSize, btnSize))
						return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									lbl := material.Label(th, unit.Sp(28), lblText)
									lbl.Color = pal.TextPrimary
									lbl.Font.Weight = font.Bold
									return lbl.Layout(gtx)
								}),
								layout.Rigid(layout.Spacer{Height: unit.Dp(2)}.Layout),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									lbl := material.Label(th, unit.Sp(10), subText)
									lbl.Color = pal.Download
									lbl.Font.Weight = font.SemiBold
									return lbl.Layout(gtx)
								}),
							)
						})
					}(gtx)
				})
			}

			// Active test central speed readout + compact STOP button.
			val, unitLabel := engine.FormatSpeed(state.DisplayMbps, state.Unit)
			phaseTitle := "CONNECTING"
			phaseCol := pal.Ping
			switch state.Phase {
			case engine.PhasePing:
				phaseTitle = "PING & JITTER"
				phaseCol = pal.Ping
			case engine.PhaseDownload:
				phaseTitle = "DOWNLOAD"
				phaseCol = pal.Download
			case engine.PhaseUpload:
				phaseTitle = "UPLOAD"
				phaseCol = pal.Upload
			}

			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Label(th, unit.Sp(11), phaseTitle)
					lbl.Color = phaseCol
					lbl.Font.Weight = font.Bold
					return lbl.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					speedStr := fmt.Sprintf("%.1f", val)
					if state.Phase == engine.PhasePing || state.Phase == engine.PhaseInit {
						if state.PingMS > 0 {
							speedStr = fmt.Sprintf("%.1f", state.PingMS)
						} else {
							speedStr = "..."
						}
					}
					lbl := material.Label(th, unit.Sp(38), speedStr)
					lbl.Color = pal.TextPrimary
					lbl.Font.Weight = font.Bold
					return lbl.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					uText := unitLabel
					if state.Phase == engine.PhasePing || state.Phase == engine.PhaseInit {
						uText = "ms"
					}
					lbl := material.Label(th, unit.Sp(13), uText)
					lbl.Color = pal.TextSecondary
					return lbl.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return PillButton(gtx, th, pal, actionBtn, "STOP", true, pal.Danger)
				}),
			)
		}),
	)
}

func drawAnchoredText(gtx layout.Context, th *material.Theme, txt string, col color.NRGBA, sp unit.Sp, cx, cy int) {
	macro := op.Record(gtx.Ops)
	lbl := material.Label(th, sp, txt)
	lbl.Color = col
	lbl.Alignment = text.Middle
	c := gtx
	c.Constraints.Min = image.Point{}
	dims := lbl.Layout(c)
	call := macro.Stop()

	defer op.Offset(image.Pt(cx-dims.Size.X/2, cy-dims.Size.Y/2)).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
}

// DrawSparkline renders a minimalist GPU vector throughput curve with translucent fill.
func DrawSparkline(gtx layout.Context, pal AppPalette, samples []float64, lineCol color.NRGBA, heightDp unit.Dp) layout.Dimensions {
	w := gtx.Constraints.Max.X
	if w <= 0 {
		w = gtx.Dp(unit.Dp(180))
	}
	h := gtx.Dp(heightDp)
	if h <= 0 {
		h = gtx.Dp(unit.Dp(42))
	}

	// Subtle baseline.
	var basePath clip.Path
	basePath.Begin(gtx.Ops)
	basePath.MoveTo(f32.Pt(0, float32(h-1)))
	basePath.LineTo(f32.Pt(float32(w), float32(h-1)))
	paint.FillShape(gtx.Ops, pal.Border, clip.Stroke{Path: basePath.End(), Width: 1}.Op())

	if len(samples) < 2 {
		return layout.Dimensions{Size: image.Pt(w, h)}
	}

	maxV := 1.0
	for _, s := range samples {
		if s > maxV {
			maxV = s
		}
	}
	maxV *= 1.12

	pts := make([]f32.Point, len(samples))
	padTop := float32(gtx.Dp(unit.Dp(4)))
	usableH := float32(h) - padTop - 2
	for i, s := range samples {
		x := float32(i) / float32(len(samples)-1) * float32(w)
		y := padTop + usableH*(1.0-float32(s/maxV))
		pts[i] = f32.Pt(x, y)
	}

	// Filled area under curve.
	var area clip.Path
	area.Begin(gtx.Ops)
	area.MoveTo(f32.Pt(0, float32(h)))
	for _, pt := range pts {
		area.LineTo(pt)
	}
	area.LineTo(f32.Pt(float32(w), float32(h)))
	area.Close()
	paint.FillShape(gtx.Ops, withAlpha(lineCol, 42), clip.Outline{Path: area.End()}.Op())

	// Crisp line stroke.
	var line clip.Path
	line.Begin(gtx.Ops)
	line.MoveTo(pts[0])
	for _, pt := range pts[1:] {
		line.LineTo(pt)
	}
	paint.FillShape(gtx.Ops, lineCol, clip.Stroke{
		Path:  line.End(),
		Width: float32(gtx.Dp(unit.Dp(2))),
	}.Op())

	return layout.Dimensions{Size: image.Pt(w, h)}
}

// Card draws a minimalist rounded surface card with a 1px border.
func Card(gtx layout.Context, pal AppPalette, radiusDp, padDp unit.Dp, w layout.Widget) layout.Dimensions {
	if gtx.Constraints.Max.X > 0 && gtx.Constraints.Max.X < 1<<20 {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
	}
	pad := layout.UniformInset(padDp)
	macro := op.Record(gtx.Ops)
	dims := pad.Layout(gtx, w)
	call := macro.Stop()

	rect := image.Rectangle{Max: dims.Size}
	rr := clip.UniformRRect(rect, gtx.Dp(radiusDp))
	paint.FillShape(gtx.Ops, pal.Surface, rr.Op(gtx.Ops))
	paint.FillShape(gtx.Ops, pal.Border, clip.Stroke{
		Path:  rr.Path(gtx.Ops),
		Width: 1,
	}.Op())

	call.Add(gtx.Ops)
	return dims
}

// PillButton draws a clean rounded clickable button.
func PillButton(gtx layout.Context, th *material.Theme, pal AppPalette, btn *widget.Clickable, label string, active bool, accent color.NRGBA) layout.Dimensions {
	return PillButtonEnabled(gtx, th, pal, btn, label, active, true, accent)
}

// PillButtonEnabled draws a pill button that can be enabled or visually disabled.
func PillButtonEnabled(gtx layout.Context, th *material.Theme, pal AppPalette, btn *widget.Clickable, label string, active bool, enabled bool, accent color.NRGBA) layout.Dimensions {
	drawBody := func(gtx layout.Context, hovered bool) layout.Dimensions {
		bg := pal.Surface
		fg := pal.TextSecondary
		border := pal.Border

		if !enabled {
			bg = withAlpha(pal.Surface, 140)
			fg = withAlpha(pal.TextMuted, 130)
			border = withAlpha(pal.Border, 120)
		} else if active {
			bg = withAlpha(accent, 36)
			if !pal.Dark {
				bg = withAlpha(accent, 26)
			}
			fg = accent
			border = withAlpha(accent, 140)
		} else if hovered {
			bg = pal.Elevated
			fg = pal.TextPrimary
		}

		macro := op.Record(gtx.Ops)
		dims := layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(12), label)
			lbl.Color = fg
			lbl.Font.Weight = font.SemiBold
			return lbl.Layout(gtx)
		})
		call := macro.Stop()

		rr := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(unit.Dp(8)))
		paint.FillShape(gtx.Ops, bg, rr.Op(gtx.Ops))
		paint.FillShape(gtx.Ops, border, clip.Stroke{
			Path:  rr.Path(gtx.Ops),
			Width: 1,
		}.Op())
		call.Add(gtx.Ops)
		return dims
	}

	if !enabled {
		return drawBody(gtx, false)
	}
	return btn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return drawBody(gtx, btn.Hovered())
	})
}

// SolidButton draws a high-contrast primary action button.
func SolidButton(gtx layout.Context, th *material.Theme, pal AppPalette, btn *widget.Clickable, label string, accent color.NRGBA) layout.Dimensions {
	return btn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		bg := accent
		if btn.Hovered() {
			bg = blendColor(accent, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, 0.14)
		}
		macro := op.Record(gtx.Ops)
		dims := layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(16), Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(12), label)
			lbl.Color = pal.ButtonText
			lbl.Font.Weight = font.Bold
			return lbl.Layout(gtx)
		})
		call := macro.Stop()

		rr := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(unit.Dp(8)))
		paint.FillShape(gtx.Ops, bg, rr.Op(gtx.Ops))
		call.Add(gtx.Ops)
		return dims
	})
}

// DrawHistoryTrendChart plots Download, Upload, and Ping trends across up to the last 30 runs.
func DrawHistoryTrendChart(gtx layout.Context, th *material.Theme, pal AppPalette, items []engine.Result, unitName string, heightDp unit.Dp) layout.Dimensions {
	// items arrives newest-first; take up to 30 and reverse to chronological order.
	n := len(items)
	if n > 30 {
		n = 30
	}
	chrono := make([]engine.Result, n)
	for i := 0; i < n; i++ {
		chrono[i] = items[n-1-i]
	}

	return Card(gtx, pal, unit.Dp(12), unit.Dp(12), func(gtx layout.Context) layout.Dimensions {
		if n < 2 {
			lbl := material.Label(th, unit.Sp(12), "Run at least 2 speed tests to view Download / Upload / Ping trends.")
			lbl.Color = pal.TextMuted
			return lbl.Layout(gtx)
		}

		maxSpeed := 1.0
		maxPing := 1.0
		_, unitLbl := engine.FormatSpeed(100, unitName)
		for _, r := range chrono {
			dlV, _ := engine.FormatSpeed(r.DownloadMbps, unitName)
			ulV, _ := engine.FormatSpeed(r.UploadMbps, unitName)
			if dlV > maxSpeed {
				maxSpeed = dlV
			}
			if ulV > maxSpeed {
				maxSpeed = ulV
			}
			if r.PingMS > maxPing {
				maxPing = r.PingMS
			}
		}
		maxSpeed *= 1.12
		maxPing *= 1.25

		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(12), fmt.Sprintf("RECENT TREND (LAST %d RUNS · MAX %.0f %s)", n, maxSpeed/1.12, unitLbl))
						lbl.Color = pal.TextSecondary
						lbl.Font.Weight = font.Bold
						return lbl.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(11), "— Download")
						lbl.Color = pal.Download
						lbl.Font.Weight = font.SemiBold
						return layout.Inset{Right: unit.Dp(12)}.Layout(gtx, lbl.Layout)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(11), "— Upload")
						lbl.Color = pal.Upload
						lbl.Font.Weight = font.SemiBold
						return layout.Inset{Right: unit.Dp(12)}.Layout(gtx, lbl.Layout)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(11), "— Ping")
						lbl.Color = pal.Ping
						lbl.Font.Weight = font.SemiBold
						return lbl.Layout(gtx)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				w := gtx.Constraints.Max.X
				if w <= 0 {
					w = gtx.Dp(unit.Dp(400))
				}
				h := gtx.Dp(heightDp)
				if h <= 0 {
					h = gtx.Dp(unit.Dp(88))
				}

				// Horizontal grid lines at 25%, 50%, 75%, 100%.
				for g := 0; g <= 3; g++ {
					gy := float32(h) * float32(g) / 3.0
					if g == 3 {
						gy = float32(h - 1)
					}
					var gp clip.Path
					gp.Begin(gtx.Ops)
					gp.MoveTo(f32.Pt(0, gy))
					gp.LineTo(f32.Pt(float32(w), gy))
					paint.FillShape(gtx.Ops, withAlpha(pal.Border, 140), clip.Stroke{Path: gp.End(), Width: 1}.Op())
				}

				plotSeries := func(vals []float64, maxVal float64, col color.NRGBA, strokeWidthDp unit.Dp, fillArea bool) {
					pts := make([]f32.Point, len(vals))
					pad := float32(gtx.Dp(unit.Dp(4)))
					usable := float32(h) - pad*2
					for i, v := range vals {
						x := float32(i) / float32(len(vals)-1) * float32(w)
						y := pad + usable*float32(1.0-v/maxVal)
						pts[i] = f32.Pt(x, y)
					}
					if fillArea {
						var area clip.Path
						area.Begin(gtx.Ops)
						area.MoveTo(f32.Pt(0, float32(h)))
						for _, pt := range pts {
							area.LineTo(pt)
						}
						area.LineTo(f32.Pt(float32(w), float32(h)))
						area.Close()
						paint.FillShape(gtx.Ops, withAlpha(col, 24), clip.Outline{Path: area.End()}.Op())
					}
					var line clip.Path
					line.Begin(gtx.Ops)
					line.MoveTo(pts[0])
					for _, pt := range pts[1:] {
						line.LineTo(pt)
					}
					paint.FillShape(gtx.Ops, col, clip.Stroke{
						Path:  line.End(),
						Width: float32(gtx.Dp(strokeWidthDp)),
					}.Op())
					for _, pt := range pts {
						fillDot(gtx.Ops, pt, float32(gtx.Dp(unit.Dp(2.5))), col)
					}
				}

				dlVals := make([]float64, n)
				ulVals := make([]float64, n)
				pingVals := make([]float64, n)
				for i, r := range chrono {
					dlVals[i], _ = engine.FormatSpeed(r.DownloadMbps, unitName)
					ulVals[i], _ = engine.FormatSpeed(r.UploadMbps, unitName)
					pingVals[i] = r.PingMS
				}

				plotSeries(pingVals, maxPing, withAlpha(pal.Ping, 190), unit.Dp(1.5), false)
				plotSeries(ulVals, maxSpeed, pal.Upload, unit.Dp(2), true)
				plotSeries(dlVals, maxSpeed, pal.Download, unit.Dp(2), true)

				return layout.Dimensions{Size: image.Pt(w, h)}
			}),
		)
	})
}
