package main

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"netspeed/internal/config"
	"netspeed/internal/engine"
)

func (a *App) layoutFrame(gtx layout.Context) layout.Dimensions {
	// Handle UI button/input events first before taking render snapshot.
	a.handleUIEvents(gtx)

	a.mu.Lock()
	pal := a.pal
	th := a.th
	activeTab := a.activeTab
	running := a.running
	// Smooth needle/speed interpolation toward targetMbps.
	diff := a.targetMbps - a.displayMbps
	if math.Abs(diff) < 0.08 {
		a.displayMbps = a.targetMbps
	} else {
		a.displayMbps += diff * 0.22
	}
	animating := running || math.Abs(a.targetMbps-a.displayMbps) > 0.08
	toastActive := a.toastMsg != "" && time.Now().Before(a.toastUntil)
	toastMsg := a.toastMsg
	a.mu.Unlock()

	if animating {
		gtx.Execute(op.InvalidateCmd{})
	}

	// Fill full window background.
	paint.FillShape(gtx.Ops, pal.Background, clip.Rect{Max: gtx.Constraints.Max}.Op())

	return layout.UniformInset(unit.Dp(18)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return a.layoutHeader(gtx, th, pal)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				switch activeTab {
				case 1:
					return a.layoutServersView(gtx, th, pal)
				case 2:
					return a.layoutScheduleTrayView(gtx, th, pal)
				case 3:
					return a.layoutHistoryView(gtx, th, pal)
				default:
					return a.layoutDashboard(gtx, th, pal)
				}
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !toastActive {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(12), toastMsg)
						lbl.Color = pal.Ping
						lbl.Font.Weight = font.SemiBold
						return lbl.Layout(gtx)
					})
				})
			}),
		)
	})
}

func (a *App) handleUIEvents(gtx layout.Context) {
	// Top bar navigation tabs.
	tabNames := 4
	for i := 0; i < tabNames; i++ {
		if a.ui.tabBtns[i].Clicked(gtx) {
			a.mu.Lock()
			a.activeTab = i
			a.mu.Unlock()
			if i == 1 {
				a.mu.Lock()
				empty := len(a.servers) == 0 && !a.serversLoading
				a.mu.Unlock()
				if empty {
					a.refreshServersAsync()
				}
			} else if i == 3 {
				a.reloadHistory()
			}
		}
	}

	if a.ui.unitBtn.Clicked(gtx) {
		a.mu.Lock()
		switch a.cfg.Unit {
		case "Mbps":
			a.cfg.Unit = "MB/s"
		case "MB/s":
			a.cfg.Unit = "Kbps"
		default:
			a.cfg.Unit = "Mbps"
		}
		_ = config.Save(a.cfg)
		a.mu.Unlock()
	}

	if a.ui.themeBtn.Clicked(gtx) {
		a.toggleTheme()
	}

	if a.ui.trayBtn.Clicked(gtx) {
		a.hideToTray()
	}

	if a.ui.goStopBtn.Clicked(gtx) {
		a.mu.Lock()
		running := a.running
		a.mu.Unlock()
		if running {
			a.stopCurrentTest()
		} else {
			a.startTest("manual")
		}
	}

	if a.ui.ipToggleBtn.Clicked(gtx) {
		a.mu.Lock()
		a.cfg.HideIP = !a.cfg.HideIP
		_ = config.Save(a.cfg)
		a.mu.Unlock()
	}

	if a.ui.copyResultBtn.Clicked(gtx) {
		summary := a.buildClipboardSummary()
		gtx.Execute(clipboard.WriteCmd{
			Type: "text/plain",
			Data: io.NopCloser(strings.NewReader(summary)),
		})
		a.showToast("Result copied to clipboard")
	}

	// Tab 1: Providers & Custom URL
	providers := []string{"auto", "ookla", "cloudflare", "custom"}
	for i, p := range providers {
		if a.ui.provBtns[i].Clicked(gtx) {
			a.mu.Lock()
			a.cfg.Provider = p
			if p != "custom" {
				a.cfg.PreferredServerID = ""
			}
			_ = config.Save(a.cfg)
			a.mu.Unlock()
			if p != "custom" {
				a.refreshServersAsync()
			}
		}
	}

	if a.ui.refreshServersBtn.Clicked(gtx) {
		a.refreshServersAsync()
	}

	for {
		ev, ok := a.ui.customURLEditor.Update(gtx)
		if !ok {
			break
		}
		if _, submitted := ev.(widget.SubmitEvent); submitted {
			a.applyCustomURL(a.ui.customURLEditor.Text())
		}
	}
	if a.ui.useCustomURLBtn.Clicked(gtx) {
		a.applyCustomURL(a.ui.customURLEditor.Text())
	}

	a.mu.Lock()
	savedCount := len(a.cfg.SavedCustomURLs)
	a.mu.Unlock()
	for i := 0; i < savedCount && i < len(a.ui.savedURLBtns); i++ {
		if a.ui.savedURLBtns[i].Clicked(gtx) {
			a.mu.Lock()
			u := a.cfg.SavedCustomURLs[i]
			a.mu.Unlock()
			a.ui.customURLEditor.SetText(u)
			a.applyCustomURL(u)
		}
	}

	a.mu.Lock()
	srvCount := len(a.servers)
	a.mu.Unlock()
	for i := 0; i < srvCount && i < len(a.ui.serverBtns); i++ {
		if a.ui.serverBtns[i].Clicked(gtx) {
			a.mu.Lock()
			srv := a.servers[i]
			a.cfg.Provider = string(srv.Provider)
			a.cfg.PreferredServerID = srv.ID
			a.selectedServer = srv
			_ = config.Save(a.cfg)
			a.activeTab = 0
			a.mu.Unlock()
			a.showToast("Selected server: " + srv.Name)
		}
	}

	// Tab 2: Schedule & Tray
	if a.ui.schedToggleBtn.Clicked(gtx) {
		a.toggleSchedule()
	}

	presets := []int{15, 30, 60, 360, 1440}
	for i, mins := range presets {
		if a.ui.schedIntervalBtns[i].Clicked(gtx) {
			a.setScheduleInterval(mins, true)
		}
	}

	if a.ui.applyCustomMinBtn.Clicked(gtx) {
		if m, err := strconv.Atoi(strings.TrimSpace(a.ui.customMinEditor.Text())); err == nil && m >= 1 {
			a.setScheduleInterval(m, true)
			a.showToast(fmt.Sprintf("Schedule interval set to %d min", m))
		} else {
			a.showToast("Enter a valid interval in minutes (>= 1)")
		}
	}

	if a.ui.closeToTrayBtn.Clicked(gtx) {
		a.mu.Lock()
		a.cfg.CloseToTray = !a.cfg.CloseToTray
		_ = config.Save(a.cfg)
		a.mu.Unlock()
	}
	if a.ui.hideNowBtn.Clicked(gtx) {
		a.hideToTray()
	}

	threadChoices := []int{4, 8, 16, 32}
	for i, tCount := range threadChoices {
		if a.ui.threadBtns[i].Clicked(gtx) {
			a.mu.Lock()
			a.cfg.Threads = tCount
			_ = config.Save(a.cfg)
			a.mu.Unlock()
		}
	}

	durChoices := []int{5, 10, 15}
	for i, dSec := range durChoices {
		if a.ui.durBtns[i].Clicked(gtx) {
			a.mu.Lock()
			a.cfg.DurationSeconds = dSec
			_ = config.Save(a.cfg)
			a.mu.Unlock()
		}
	}

	// Tab 3: History
	if a.ui.exportCSVBtn.Clicked(gtx) {
		outPath := filepath.Join(config.AppDir(), "netspeed-history.csv")
		f, err := os.Create(outPath)
		if err == nil {
			err = a.store.ExportCSV(f)
			_ = f.Close()
		}
		if err != nil {
			a.showToast("Export failed: " + err.Error())
		} else {
			a.showToast("Exported CSV to " + outPath)
		}
	}
	if a.ui.clearHistoryBtn.Clicked(gtx) {
		_ = a.store.Clear()
		a.reloadHistory()
		a.showToast("Test history cleared")
	}
}

func (a *App) applyCustomURL(raw string) {
	norm, err := engine.NormalizeCustomURL(raw)
	if err != nil {
		a.mu.Lock()
		a.customURLErr = err.Error()
		a.mu.Unlock()
		return
	}
	a.mu.Lock()
	a.customURLErr = ""
	a.cfg.Provider = string(engine.ProviderCustom)
	a.cfg.AddSavedCustomURL(norm)
	a.ensureSavedURLButtonsLocked()
	_ = config.Save(a.cfg)
	a.selectedServer = engine.Server{
		ID:       "custom-url",
		Name:     "Custom: " + norm,
		Provider: engine.ProviderCustom,
		URL:      norm,
	}
	a.activeTab = 0
	a.mu.Unlock()
	a.showToast("Custom URL active: " + norm)
}

func (a *App) layoutHeader(gtx layout.Context, th *material.Theme, pal AppPalette) layout.Dimensions {
	a.mu.Lock()
	activeTab := a.activeTab
	unitLabel := a.cfg.Unit
	dark := a.cfg.Theme == "dark"
	schedEnabled := a.cfg.ScheduleEnabled
	schedMins := a.cfg.ScheduleIntervalMinutes
	a.mu.Unlock()

	themeLabel := "Dark"
	if !dark {
		themeLabel = "Light"
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := material.Label(th, unit.Sp(20), "NetSpeed")
							lbl.Color = pal.TextPrimary
							lbl.Font.Weight = font.Bold
							return lbl.Layout(gtx)
						}),
						layout.Rigid(layout.Spacer{Width: unit.Dp(10)}.Layout),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if !schedEnabled {
								return layout.Dimensions{}
							}
							badge := fmt.Sprintf("AUTO %dm", schedMins)
							macro := op.Record(gtx.Ops)
							dims := layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								lbl := material.Label(th, unit.Sp(10), badge)
								lbl.Color = pal.Ping
								lbl.Font.Weight = font.Bold
								return lbl.Layout(gtx)
							})
							call := macro.Stop()
							rr := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(unit.Dp(6)))
							paint.FillShape(gtx.Ops, withAlpha(pal.Ping, 28), rr.Op(gtx.Ops))
							call.Add(gtx.Ops)
							return dims
						}),
					)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{} }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return PillButton(gtx, th, pal, &a.ui.unitBtn, unitLabel, false, pal.Download)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return PillButton(gtx, th, pal, &a.ui.themeBtn, "Theme: "+themeLabel, false, pal.Upload)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return PillButton(gtx, th, pal, &a.ui.trayBtn, "Hide to Tray", false, pal.TextSecondary)
				}),
			)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			tabs := []string{"Speed Test", "Servers & URL", "Schedule & Tray", "History"}
			children := make([]layout.FlexChild, 0, len(tabs)*2)
			for i, name := range tabs {
				idx := i
				title := name
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return PillButton(gtx, th, pal, &a.ui.tabBtns[idx], title, activeTab == idx, pal.Download)
				}))
				if idx < len(tabs)-1 {
					children = append(children, layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout))
				}
			}
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, children...)
		}),
	)
}

func (a *App) layoutDashboard(gtx layout.Context, th *material.Theme, pal AppPalette) layout.Dimensions {
	a.mu.Lock()
	gs := GaugeState{
		Phase:        a.phase,
		DisplayMbps:  a.displayMbps,
		Unit:         a.cfg.Unit,
		Progress:     a.progress,
		PingMS:       a.pingMS,
		LoadedPingMS: a.loadedPingMS,
		StatusText:   a.statusMsg,
		Running:      a.running,
	}
	unitName := a.cfg.Unit
	pingMS := a.pingMS
	jitterMS := a.jitterMS
	dlMbps := a.downloadMbps
	ulMbps := a.uploadMbps
	dlPing := a.downloadPingMS
	ulPing := a.uploadPingMS
	dlBytes := a.downloadBytes
	ulBytes := a.uploadBytes
	dlSamples := append([]float64(nil), a.dlSamples...)
	ulSamples := append([]float64(nil), a.ulSamples...)
	client := a.clientInfo
	srv := a.selectedServer
	hideIP := a.cfg.HideIP
	prov := a.cfg.Provider
	customURL := a.cfg.CustomURL
	statusMsg := a.statusMsg
	a.mu.Unlock()

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return DrawSpeedGauge(gtx, th, pal, gs, &a.ui.goStopBtn)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						msg := statusMsg
						if msg == "" {
							msg = "Ready to measure latency, download, and upload"
						}
						lbl := material.Label(th, unit.Sp(12), msg)
						lbl.Color = pal.TextSecondary
						return lbl.Layout(gtx)
					}),
				)
			})
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
				// Card 1: Ping & Jitter
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return Card(gtx, pal, unit.Dp(12), unit.Dp(14), func(gtx layout.Context) layout.Dimensions {
						pingTxt := "—"
						if pingMS > 0 {
							pingTxt = fmt.Sprintf("%.1f", pingMS)
						}
						sub := "Jitter: —"
						if jitterMS > 0 {
							sub = fmt.Sprintf("Jitter: %.1f ms", jitterMS)
						}
						loaded := "Loaded DL / UL: —"
						if dlPing > 0 || ulPing > 0 {
							loaded = fmt.Sprintf("Loaded: %.0f / %.0f ms", dlPing, ulPing)
						}
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								lbl := material.Label(th, unit.Sp(11), "PING & JITTER")
								lbl.Color = pal.Ping
								lbl.Font.Weight = font.Bold
								return lbl.Layout(gtx)
							}),
							layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layoutMetricRow(gtx, th, pal, pingTxt, "ms")
							}),
							layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								lbl := material.Label(th, unit.Sp(11), sub)
								lbl.Color = pal.TextSecondary
								return lbl.Layout(gtx)
							}),
							layout.Rigid(layout.Spacer{Height: unit.Dp(3)}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								lbl := material.Label(th, unit.Sp(11), loaded)
								lbl.Color = pal.TextMuted
								return lbl.Layout(gtx)
							}),
						)
					})
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
				// Card 2: Download
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layoutTransferCard(gtx, th, pal, "DOWNLOAD", pal.Download, dlMbps, unitName, dlBytes, dlSamples)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
				// Card 3: Upload
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layoutTransferCard(gtx, th, pal, "UPLOAD", pal.Upload, ulMbps, unitName, ulBytes, ulSamples)
				}),
			)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		// Footer bar: ISP/IP + Active Server + Copy Result
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return Card(gtx, pal, unit.Dp(10), unit.Dp(12), func(gtx layout.Context) layout.Dimensions {
				ipStr := client.IP
				if ipStr == "" {
					ipStr = "Auto-detect on run"
				} else if hideIP {
					ipStr = "•••.•••.•••.•••"
				}
				ispStr := client.ISP
				if ispStr == "" {
					ispStr = "Network"
				}

				srvStr := "Auto-select optimal server (" + strings.ToUpper(prov) + ")"
				if prov == "custom" && customURL != "" {
					srvStr = "Custom: " + customURL
				} else if srv.Name != "" {
					srvStr = srv.Name
					if srv.DistanceKM > 0 {
						srvStr = fmt.Sprintf("%s [%.0f km]", srv.Name, srv.DistanceKM)
					}
				}

				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return a.ui.ipToggleBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									lbl := material.Label(th, unit.Sp(12), fmt.Sprintf("%s  ·  %s", ispStr, ipStr))
									lbl.Color = pal.TextPrimary
									lbl.Font.Weight = font.SemiBold
									return lbl.Layout(gtx)
								}),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									lbl := material.Label(th, unit.Sp(11), srvStr)
									lbl.Color = pal.TextSecondary
									return lbl.Layout(gtx)
								}),
							)
						})
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return PillButton(gtx, th, pal, &a.ui.copyResultBtn, "Copy Result", false, pal.Download)
					}),
				)
			})
		}),
	)
}

func layoutMetricRow(gtx layout.Context, th *material.Theme, pal AppPalette, valStr, unitStr string) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Baseline}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(24), valStr)
			lbl.Color = pal.TextPrimary
			lbl.Font.Weight = font.Bold
			return lbl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(5)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(12), unitStr)
			lbl.Color = pal.TextSecondary
			return lbl.Layout(gtx)
		}),
	)
}

func layoutTransferCard(gtx layout.Context, th *material.Theme, pal AppPalette, title string, accent color.NRGBA, mbps float64, unitName string, bytes int64, samples []float64) layout.Dimensions {
	return Card(gtx, pal, unit.Dp(12), unit.Dp(14), func(gtx layout.Context) layout.Dimensions {
		valStr := "—"
		_, uLabel := engine.FormatSpeed(mbps, unitName)
		if mbps > 0 {
			v, ul := engine.FormatSpeed(mbps, unitName)
			valStr = fmt.Sprintf("%.2f", v)
			uLabel = ul
		}
		mbStr := ""
		if bytes > 0 {
			mbStr = fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
		}

		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(11), title)
						lbl.Color = accent
						lbl.Font.Weight = font.Bold
						return lbl.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(10), mbStr)
						lbl.Color = pal.TextMuted
						return lbl.Layout(gtx)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layoutMetricRow(gtx, th, pal, valStr, uLabel)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return DrawSparkline(gtx, pal, samples, accent, unit.Dp(34))
			}),
		)
	})
}

func (a *App) layoutServersView(gtx layout.Context, th *material.Theme, pal AppPalette) layout.Dimensions {
	a.mu.Lock()
	prov := a.cfg.Provider
	selectedID := a.cfg.PreferredServerID
	servers := append([]engine.Server(nil), a.servers...)
	loading := a.serversLoading
	srvErr := a.serversErr
	savedURLs := append([]string(nil), a.cfg.SavedCustomURLs...)
	customErr := a.customURLErr
	a.mu.Unlock()

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		// Provider Selector Card
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return Card(gtx, pal, unit.Dp(12), unit.Dp(14), func(gtx layout.Context) layout.Dimensions {
				labels := []struct {
					key   string
					title string
				}{
					{"auto", "Auto (Ookla + Fallback)"},
					{"ookla", "Ookla (Speedtest.net)"},
					{"cloudflare", "Cloudflare Edge"},
					{"custom", "Custom Server URL"},
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(12), "TEST PROVIDER")
						lbl.Color = pal.TextSecondary
						lbl.Font.Weight = font.Bold
						return lbl.Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						children := make([]layout.FlexChild, 0, len(labels)*2)
						for i, item := range labels {
							idx := i
							p := item
							children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return PillButton(gtx, th, pal, &a.ui.provBtns[idx], p.title, prov == p.key, pal.Download)
							}))
							if idx < len(labels)-1 {
								children = append(children, layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout))
							}
						}
						return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, children...)
					}),
				)
			})
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),

		// Custom Server URL Card
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return Card(gtx, pal, unit.Dp(12), unit.Dp(14), func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(12), "CUSTOM SERVER URL (HTTP / HTTPS / OOKLA HOST)")
						lbl.Color = pal.TextSecondary
						lbl.Font.Weight = font.Bold
						return lbl.Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return layoutEditorBox(gtx, th, pal, &a.ui.customURLEditor, "e.g. https://speed.cloudflare.com or http://host:8080/speedtest/upload.php")
							}),
							layout.Rigid(layout.Spacer{Width: unit.Dp(10)}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return SolidButton(gtx, th, pal, &a.ui.useCustomURLBtn, "Use Custom URL", pal.Download)
							}),
						)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if customErr == "" {
							return layout.Dimensions{}
						}
						return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							lbl := material.Label(th, unit.Sp(11), customErr)
							lbl.Color = pal.Danger
							return lbl.Layout(gtx)
						})
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if len(savedURLs) == 0 {
							return layout.Dimensions{}
						}
						return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							maxShow := len(savedURLs)
							if maxShow > 3 {
								maxShow = 3
							}
							children := []layout.FlexChild{
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									lbl := material.Label(th, unit.Sp(11), "Recent:")
									lbl.Color = pal.TextMuted
									return layout.Inset{Top: unit.Dp(6), Right: unit.Dp(8)}.Layout(gtx, lbl.Layout)
								}),
							}
							for i := 0; i < maxShow && i < len(a.ui.savedURLBtns); i++ {
								idx := i
								urlStr := savedURLs[idx]
								if len(urlStr) > 36 {
									urlStr = urlStr[:33] + "..."
								}
								children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return PillButton(gtx, th, pal, &a.ui.savedURLBtns[idx], urlStr, false, pal.Upload)
								}))
								children = append(children, layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout))
							}
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
						})
					}),
				)
			})
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),

		// Discovered Servers List
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return Card(gtx, pal, unit.Dp(12), unit.Dp(14), func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								lbl := material.Label(th, unit.Sp(12), "NEARBY DISCOVERED SERVERS")
								lbl.Color = pal.TextSecondary
								lbl.Font.Weight = font.Bold
								return lbl.Layout(gtx)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								txt := "Refresh"
								if loading {
									txt = "Refreshing..."
								}
								return PillButton(gtx, th, pal, &a.ui.refreshServersBtn, txt, false, pal.Download)
							}),
						)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						if loading && len(servers) == 0 {
							return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								lbl := material.Label(th, unit.Sp(13), "Discovering and probing nearby servers...")
								lbl.Color = pal.TextSecondary
								return lbl.Layout(gtx)
							})
						}
						if srvErr != "" && len(servers) == 0 {
							return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								lbl := material.Label(th, unit.Sp(12), srvErr)
								lbl.Color = pal.Danger
								return lbl.Layout(gtx)
							})
						}
						if len(servers) == 0 {
							return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								lbl := material.Label(th, unit.Sp(12), "Click Refresh to probe servers")
								lbl.Color = pal.TextMuted
								return lbl.Layout(gtx)
							})
						}
						return material.List(th, &a.ui.serverList).Layout(gtx, len(servers), func(gtx layout.Context, i int) layout.Dimensions {
							if i >= len(a.ui.serverBtns) {
								return layout.Dimensions{}
							}
							srv := servers[i]
							isSelected := srv.ID == selectedID
							return layout.Inset{Bottom: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return a.ui.serverBtns[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									bg := pal.Background
									border := pal.Border
									if isSelected {
										bg = withAlpha(pal.Download, 24)
										border = pal.Download
									} else if a.ui.serverBtns[i].Hovered() {
										bg = pal.Elevated
									}
									macro := op.Record(gtx.Ops)
									dims := layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10), Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
										latStr := "—"
										if srv.LatencyMS > 0 && srv.LatencyMS < 9000 {
											latStr = fmt.Sprintf("%.1f ms", srv.LatencyMS)
										}
										distStr := ""
										if srv.DistanceKM > 0 {
											distStr = fmt.Sprintf("%.0f km · ", srv.DistanceKM)
										}
										return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
											layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
												return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
													layout.Rigid(func(gtx layout.Context) layout.Dimensions {
														lbl := material.Label(th, unit.Sp(13), srv.Sponsor)
														lbl.Color = pal.TextPrimary
														lbl.Font.Weight = font.SemiBold
														return lbl.Layout(gtx)
													}),
													layout.Rigid(func(gtx layout.Context) layout.Dimensions {
														loc := fmt.Sprintf("%s%s, %s · %s", distStr, srv.City, srv.Country, strings.ToUpper(string(srv.Provider)))
														lbl := material.Label(th, unit.Sp(11), loc)
														lbl.Color = pal.TextSecondary
														return lbl.Layout(gtx)
													}),
												)
											}),
											layout.Rigid(func(gtx layout.Context) layout.Dimensions {
												lbl := material.Label(th, unit.Sp(12), latStr)
												lbl.Color = pal.Ping
												lbl.Font.Weight = font.Bold
												return lbl.Layout(gtx)
											}),
										)
									})
									call := macro.Stop()
									rr := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(unit.Dp(8)))
									paint.FillShape(gtx.Ops, bg, rr.Op(gtx.Ops))
									paint.FillShape(gtx.Ops, border, clip.Stroke{Path: rr.Path(gtx.Ops), Width: 1}.Op())
									call.Add(gtx.Ops)
									return dims
								})
							})
						})
					}),
				)
			})
		}),
	)
}

func (a *App) layoutScheduleTrayView(gtx layout.Context, th *material.Theme, pal AppPalette) layout.Dimensions {
	a.mu.Lock()
	cfg := a.cfg
	nextRun := a.sched.NextRun()
	lastSched := a.lastScheduledSummary
	a.mu.Unlock()

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		// Scheduled Background Tests Card
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return Card(gtx, pal, unit.Dp(12), unit.Dp(16), func(gtx layout.Context) layout.Dimensions {
				schedLabel := "OFF — Manual only"
				if cfg.ScheduleEnabled {
					schedLabel = "ON — Running in background"
				}
				nextText := "Disabled"
				if cfg.ScheduleEnabled && !nextRun.IsZero() {
					nextText = nextRun.Local().Format("2006-01-02 15:04:05")
				}

				presets := []struct {
					mins  int
					label string
				}{
					{15, "15m"},
					{30, "30m"},
					{60, "1h"},
					{360, "6h"},
					{1440, "24h"},
				}

				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										lbl := material.Label(th, unit.Sp(14), "Scheduled Background Tests")
										lbl.Color = pal.TextPrimary
										lbl.Font.Weight = font.Bold
										return lbl.Layout(gtx)
									}),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										lbl := material.Label(th, unit.Sp(11), "Runs automatically even when minimized to the Windows 11 system tray")
										lbl.Color = pal.TextSecondary
										return lbl.Layout(gtx)
									}),
								)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return PillButton(gtx, th, pal, &a.ui.schedToggleBtn, schedLabel, cfg.ScheduleEnabled, pal.Ping)
							}),
						)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						children := make([]layout.FlexChild, 0, len(presets)*2+4)
						for i, p := range presets {
							idx := i
							item := p
							children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return PillButton(gtx, th, pal, &a.ui.schedIntervalBtns[idx], item.label, cfg.ScheduleIntervalMinutes == item.mins, pal.Download)
							}))
							children = append(children, layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout))
						}
						children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return layoutEditorBox(gtx, th, pal, &a.ui.customMinEditor, "Custom mins")
						}))
						children = append(children, layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout))
						children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return PillButton(gtx, th, pal, &a.ui.applyCustomMinBtn, "Set", false, pal.Download)
						}))
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(12), fmt.Sprintf("Next Run: %s", nextText))
						lbl.Color = pal.TextSecondary
						return lbl.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if lastSched == "" {
							return layout.Dimensions{}
						}
						lbl := material.Label(th, unit.Sp(11), "Last Scheduled Result: "+lastSched)
						lbl.Color = pal.TextMuted
						return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, lbl.Layout)
					}),
				)
			})
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),

		// System Tray & Engine Settings Card
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return Card(gtx, pal, unit.Dp(12), unit.Dp(16), func(gtx layout.Context) layout.Dimensions {
				trayText := "Close Button: Exit App"
				if cfg.CloseToTray {
					trayText = "Close Button: Minimize to System Tray"
				}
				threads := []int{4, 8, 16, 32}
				durs := []int{5, 10, 15}

				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(14), "System Tray & Engine Tuning")
						lbl.Color = pal.TextPrimary
						lbl.Font.Weight = font.Bold
						return lbl.Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return PillButton(gtx, th, pal, &a.ui.closeToTrayBtn, trayText, cfg.CloseToTray, pal.Ping)
							}),
							layout.Rigid(layout.Spacer{Width: unit.Dp(10)}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return PillButton(gtx, th, pal, &a.ui.hideNowBtn, "Minimize to Tray Now", false, pal.Download)
							}),
						)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						children := []layout.FlexChild{
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								lbl := material.Label(th, unit.Sp(12), "Streams:")
								lbl.Color = pal.TextSecondary
								return layout.Inset{Right: unit.Dp(10)}.Layout(gtx, lbl.Layout)
							}),
						}
						for i, tc := range threads {
							idx := i
							val := tc
							children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return PillButton(gtx, th, pal, &a.ui.threadBtns[idx], fmt.Sprintf("%d", val), cfg.Threads == val, pal.Download)
							}))
							children = append(children, layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout))
						}
						children = append(children, layout.Rigid(layout.Spacer{Width: unit.Dp(16)}.Layout))
						children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := material.Label(th, unit.Sp(12), "Phase Duration:")
							lbl.Color = pal.TextSecondary
							return layout.Inset{Right: unit.Dp(10)}.Layout(gtx, lbl.Layout)
						}))
						for i, ds := range durs {
							idx := i
							val := ds
							children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return PillButton(gtx, th, pal, &a.ui.durBtns[idx], fmt.Sprintf("%ds", val), cfg.DurationSeconds == val, pal.Upload)
							}))
							children = append(children, layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout))
						}
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
					}),
				)
			})
		}),
	)
}

func (a *App) layoutHistoryView(gtx layout.Context, th *material.Theme, pal AppPalette) layout.Dimensions {
	a.mu.Lock()
	items := append([]engine.Result(nil), a.historyItems...)
	unitName := a.cfg.Unit
	a.mu.Unlock()

	return Card(gtx, pal, unit.Dp(12), unit.Dp(14), func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(14), fmt.Sprintf("Saved Test History (%d runs)", len(items)))
						lbl.Color = pal.TextPrimary
						lbl.Font.Weight = font.Bold
						return lbl.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return PillButton(gtx, th, pal, &a.ui.exportCSVBtn, "Export CSV", false, pal.Download)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return PillButton(gtx, th, pal, &a.ui.clearHistoryBtn, "Clear History", false, pal.Danger)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				if len(items) == 0 {
					return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(13), "No speed tests recorded yet. Run a test from Dashboard or CLI.")
						lbl.Color = pal.TextMuted
						return lbl.Layout(gtx)
					})
				}
				return material.List(th, &a.ui.historyList).Layout(gtx, len(items), func(gtx layout.Context, i int) layout.Dimensions {
					r := items[i]
					dlV, dlU := engine.FormatSpeed(r.DownloadMbps, unitName)
					ulV, ulU := engine.FormatSpeed(r.UploadMbps, unitName)

					return layout.Inset{Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						macro := op.Record(gtx.Ops)
						dims := layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10), Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
								layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
									return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
										layout.Rigid(func(gtx layout.Context) layout.Dimensions {
											trig := strings.ToUpper(r.Trigger)
											if trig == "" {
												trig = "MANUAL"
											}
											head := fmt.Sprintf("%s  ·  %s  ·  %s", r.Timestamp.Local().Format("2006-01-02 15:04"), trig, strings.ToUpper(string(r.Provider)))
											lbl := material.Label(th, unit.Sp(12), head)
											lbl.Color = pal.TextPrimary
											lbl.Font.Weight = font.SemiBold
											return lbl.Layout(gtx)
										}),
										layout.Rigid(func(gtx layout.Context) layout.Dimensions {
											lbl := material.Label(th, unit.Sp(11), r.Server.Name)
											lbl.Color = pal.TextSecondary
											return lbl.Layout(gtx)
										}),
									)
								}),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									lbl := material.Label(th, unit.Sp(12), fmt.Sprintf("Ping %.1f ms", r.PingMS))
									lbl.Color = pal.Ping
									lbl.Font.Weight = font.SemiBold
									return layout.Inset{Right: unit.Dp(14)}.Layout(gtx, lbl.Layout)
								}),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									lbl := material.Label(th, unit.Sp(12), fmt.Sprintf("↓ %.1f %s", dlV, dlU))
									lbl.Color = pal.Download
									lbl.Font.Weight = font.Bold
									return layout.Inset{Right: unit.Dp(14)}.Layout(gtx, lbl.Layout)
								}),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									lbl := material.Label(th, unit.Sp(12), fmt.Sprintf("↑ %.1f %s", ulV, ulU))
									lbl.Color = pal.Upload
									lbl.Font.Weight = font.Bold
									return lbl.Layout(gtx)
								}),
							)
						})
						call := macro.Stop()
						rr := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(unit.Dp(8)))
						paint.FillShape(gtx.Ops, pal.Background, rr.Op(gtx.Ops))
						paint.FillShape(gtx.Ops, pal.Border, clip.Stroke{Path: rr.Path(gtx.Ops), Width: 1}.Op())
						call.Add(gtx.Ops)
						return dims
					})
				})
			}),
		)
	})
}

func layoutEditorBox(gtx layout.Context, th *material.Theme, pal AppPalette, ed *widget.Editor, hint string) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		es := material.Editor(th, ed, hint)
		es.Color = pal.TextPrimary
		es.HintColor = pal.TextMuted
		es.SelectionColor = withAlpha(pal.Download, 70)
		es.TextSize = unit.Sp(12)
		return es.Layout(gtx)
	})
	call := macro.Stop()

	rr := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(unit.Dp(8)))
	paint.FillShape(gtx.Ops, pal.Background, rr.Op(gtx.Ops))
	paint.FillShape(gtx.Ops, pal.Border, clip.Stroke{Path: rr.Path(gtx.Ops), Width: 1}.Op())
	call.Add(gtx.Ops)
	return dims
}
