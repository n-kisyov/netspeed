package main

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"time"

	"gioui.org/app"
	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"netspeed/internal/config"
	"netspeed/internal/engine"
	"netspeed/internal/scheduler"
)

const (
	screenshotWidth  = 1120
	screenshotHeight = 780
	screenshotScale  = 1.15
)

// generateScreenshots renders deterministic GUI and export card screenshots into
// outDir without mutating the user's AppData config or spawning a tray icon.
func generateScreenshots(outDir string) error {
	if outDir == "" {
		return fmt.Errorf("output directory must not be empty")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	win, err := headless.NewWindow(screenshotWidth, screenshotHeight)
	if err != nil {
		return fmt.Errorf("init headless GPU window: %w", err)
	}
	defer win.Release()

	now := time.Date(2026, 11, 15, 14, 30, 0, 0, time.Local)
	baseClient := engine.ClientInfo{
		IP:      "212.104.117.138",
		ISP:     "A1 Bulgaria",
		City:    "Sofia",
		Country: "Bulgaria",
	}
	sofiaOokla := engine.Server{
		ID:         "10521",
		Name:       "Sofia",
		Sponsor:    "Melbicom",
		City:       "Sofia",
		Country:    "Bulgaria",
		Host:       "sof-speed.melbicom.net:8080",
		DistanceKM: 12.4,
		LatencyMS:  8.4,
		Provider:   engine.ProviderOokla,
	}
	cfSofia := engine.Server{
		ID:        "cf-SOF",
		Name:      "Cloudflare Edge — Sofia (SOF)",
		Sponsor:   "Cloudflare",
		City:      "Sofia",
		Country:   "BG",
		Host:      "speed.cloudflare.com",
		LatencyMS: 14.2,
		Provider:  engine.ProviderCloudflare,
	}

	dlDone := sampleCurve(40, 445, 502, 16)
	ulDone := sampleCurve(40, 212, 248, 11)

	doneResult := engine.Result{
		ID:               "res-20261115-1430",
		Timestamp:        now,
		Trigger:          "manual",
		Provider:         engine.ProviderOokla,
		Client:           baseClient,
		Server:           sofiaOokla,
		PingMS:           8.4,
		JitterMS:         1.6,
		DownloadPingMS:   11.2,
		UploadPingMS:     14.8,
		PacketLossPct:    0.0,
		BufferbloatGrade: "A+",
		DownloadMbps:     486.42,
		UploadMbps:       241.85,
		DownloadBytes:    608000000,
		UploadBytes:      302000000,
		DownloadSamples:  dlDone,
		UploadSamples:    ulDone,
		DurationMS:       18400,
	}

	sched := scheduler.New(nil)
	defer sched.Stop()
	sched.Configure(true, 60*time.Minute)

	// 1. Dark Dashboard (Completed Grade A+ test)
	darkCfg := config.Default()
	darkCfg.Theme = "dark"
	darkCfg.Provider = "ookla"
	darkApp := newScreenshotApp(darkCfg, sched)
	darkApp.activeTab = 0
	darkApp.phase = engine.PhaseDone
	darkApp.targetMbps = doneResult.DownloadMbps
	darkApp.displayMbps = doneResult.DownloadMbps
	darkApp.progress = 1.0
	darkApp.pingMS = doneResult.PingMS
	darkApp.jitterMS = doneResult.JitterMS
	darkApp.packetLossPct = doneResult.PacketLossPct
	darkApp.bufferbloatGrade = doneResult.BufferbloatGrade
	darkApp.downloadPingMS = doneResult.DownloadPingMS
	darkApp.uploadPingMS = doneResult.UploadPingMS
	darkApp.downloadMbps = doneResult.DownloadMbps
	darkApp.uploadMbps = doneResult.UploadMbps
	darkApp.downloadBytes = doneResult.DownloadBytes
	darkApp.uploadBytes = doneResult.UploadBytes
	darkApp.dlSamples = dlDone
	darkApp.ulSamples = ulDone
	darkApp.clientInfo = baseClient
	darkApp.selectedServer = sofiaOokla
	darkApp.lastSessionResult = &doneResult
	darkApp.statusMsg = "Completed (manual) in 18.4s"
	if err := renderAppScreenshot(win, darkApp, filepath.Join(outDir, "01-dashboard-dark.png")); err != nil {
		return err
	}

	// 2. Light Dashboard (Active Download with Auto Schedule active)
	lightCfg := config.Default()
	lightCfg.Theme = "light"
	lightCfg.Provider = "cloudflare"
	lightCfg.ScheduleEnabled = true
	lightCfg.ScheduleIntervalMinutes = 60
	lightApp := newScreenshotApp(lightCfg, sched)
	lightApp.activeTab = 0
	lightApp.running = true
	lightApp.phase = engine.PhaseDownload
	lightApp.targetMbps = 386.40
	lightApp.displayMbps = 386.40
	lightApp.progress = 0.64
	lightApp.pingMS = 14.2
	lightApp.jitterMS = 2.3
	lightApp.packetLossPct = 0.0
	lightApp.downloadPingMS = 21.5
	lightApp.loadedPingMS = 21.5
	lightApp.downloadMbps = 386.40
	lightApp.downloadBytes = 312000000
	lightApp.dlSamples = sampleCurve(28, 290, 405, 18)
	lightApp.clientInfo = baseClient
	lightApp.selectedServer = cfSofia
	lightApp.statusMsg = "Measuring multi-stream download throughput..."
	if err := renderAppScreenshot(win, lightApp, filepath.Join(outDir, "02-dashboard-light.png")); err != nil {
		return err
	}

	// 3. Servers & Custom URL View
	srvCfg := config.Default()
	srvCfg.Theme = "dark"
	srvCfg.Provider = "custom"
	srvCfg.CustomURL = "https://speed.cloudflare.com/__down?bytes=50000000"
	srvCfg.PreferredServerID = "10521"
	srvCfg.SavedCustomURLs = []string{
		"https://speed.cloudflare.com/__down?bytes=50000000",
		"https://nbg1-speed.hetzner.com/100MB.bin",
		"http://192.168.1.10:8080/100mb.bin",
	}
	srvApp := newScreenshotApp(srvCfg, sched)
	srvApp.activeTab = 1
	srvApp.ui.customURLEditor.SetText(srvCfg.CustomURL)
	srvApp.clientInfo = baseClient
	srvApp.servers = []engine.Server{
		sofiaOokla,
		{
			ID:         "19402",
			Name:       "Sofia",
			Sponsor:    "A1 Bulgaria EAD",
			City:       "Sofia",
			Country:    "Bulgaria",
			Host:       "speedtest.a1.bg:8080",
			DistanceKM: 14.8,
			LatencyMS:  9.1,
			Provider:   engine.ProviderOokla,
		},
		{
			ID:         "31147",
			Name:       "Sofia",
			Sponsor:    "Telepoint",
			City:       "Sofia",
			Country:    "Bulgaria",
			Host:       "lg.telepoint.bg:8080",
			DistanceKM: 15.2,
			LatencyMS:  10.3,
			Provider:   engine.ProviderOokla,
		},
		{
			ID:         "25891",
			Name:       "Plovdiv",
			Sponsor:    "Cooolbox",
			City:       "Plovdiv",
			Country:    "Bulgaria",
			Host:       "speedtest.cooolbox.bg:8080",
			DistanceKM: 132.0,
			LatencyMS:  13.6,
			Provider:   engine.ProviderOokla,
		},
		cfSofia,
		{
			ID:         "41092",
			Name:       "Bucharest",
			Sponsor:    "Voxility",
			City:       "Bucharest",
			Country:    "Romania",
			Host:       "speed.voxility.net:8080",
			DistanceKM: 296.5,
			LatencyMS:  19.8,
			Provider:   engine.ProviderOokla,
		},
	}
	srvApp.selectedServer = sofiaOokla
	srvApp.ensureSavedURLButtonsLocked()
	srvApp.ensureServerButtonsLocked()
	if err := renderAppScreenshot(win, srvApp, filepath.Join(outDir, "03-servers-custom-url.png")); err != nil {
		return err
	}

	// 4. Schedule, Startup & Tray Notifications View
	schedCfg := config.Default()
	schedCfg.Theme = "dark"
	schedCfg.ScheduleEnabled = true
	schedCfg.ScheduleIntervalMinutes = 60
	schedCfg.CloseToTray = true
	schedCfg.TrayNotifications = true
	schedCfg.AlertMinDownloadMbps = 100
	schedCfg.Threads = 8
	schedCfg.DurationSeconds = 10
	schedApp := newScreenshotApp(schedCfg, sched)
	schedApp.activeTab = 2
	schedApp.startupEnabled = true
	schedApp.ui.customMinEditor.SetText("45")
	schedApp.lastScheduledSummary = "472.8 Mbps down / 238.1 Mbps up / 9.1 ms ping at 14:00"
	if err := renderAppScreenshot(win, schedApp, filepath.Join(outDir, "04-schedule-system-tray.png")); err != nil {
		return err
	}

	// 5. History & Multi-Metric Trend Graph View
	histCfg := config.Default()
	histCfg.Theme = "dark"
	histApp := newScreenshotApp(histCfg, sched)
	histApp.activeTab = 3
	histApp.historyItems = buildSampleHistory(now, baseClient, sofiaOokla, cfSofia)
	histApp.ensureHistoryButtonsLocked()
	if err := renderAppScreenshot(win, histApp, filepath.Join(outDir, "05-history-trends.png")); err != nil {
		return err
	}

	// 6. Exported Shareable Result Card PNG (isolated temp AppData)
	if err := generateCardScreenshot(doneResult, darkApp.pal, filepath.Join(outDir, "06-result-card-export.png")); err != nil {
		return err
	}

	return nil
}

func newScreenshotApp(cfg config.Config, sched *scheduler.Scheduler) *App {
	var pal AppPalette
	if cfg.Theme == "light" {
		pal = LightPalette()
	} else {
		pal = DarkPalette()
	}
	a := &App{
		cfg:   cfg,
		pal:   pal,
		th:    newMaterialTheme(pal),
		sched: sched,
		phase: engine.PhaseIdle,
	}
	a.ui.customURLEditor.SingleLine = true
	a.ui.customURLEditor.Submit = true
	a.ui.customMinEditor.SingleLine = true
	a.ui.customMinEditor.Submit = true
	a.ui.serverList.Axis = layout.Vertical
	a.ui.historyList.Axis = layout.Vertical
	return a
}

func (a *App) ensureServerButtonsLocked() {
	if len(a.ui.serverBtns) < len(a.servers) {
		a.ui.serverBtns = make([]widget.Clickable, len(a.servers))
	}
}

func renderAppScreenshot(win *headless.Window, a *App, outPath string) error {
	size := win.Size()
	var ops op.Ops
	for i := 0; i < 2; i++ {
		fe := app.FrameEvent{
			Now: time.Now().Add(time.Duration(i) * 16 * time.Millisecond),
			Metric: unit.Metric{
				PxPerDp: screenshotScale,
				PxPerSp: screenshotScale,
			},
			Size: size,
		}
		gtx := app.NewContext(&ops, fe)
		a.layoutFrame(gtx)
		if err := win.Frame(&ops); err != nil {
			return fmt.Errorf("render frame for %s: %w", filepath.Base(outPath), err)
		}
	}

	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		return fmt.Errorf("capture screenshot %s: %w", filepath.Base(outPath), err)
	}
	return writePNGFile(outPath, img)
}

func writePNGFile(outPath string, img image.Image) error {
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return fmt.Errorf("encode %s: %w", outPath, err)
	}
	return nil
}

func generateCardScreenshot(res engine.Result, pal AppPalette, outPath string) error {
	tmpRoot, err := os.MkdirTemp("", "netspeed-ss-*")
	if err != nil {
		return fmt.Errorf("create temp AppData: %w", err)
	}
	defer os.RemoveAll(tmpRoot)

	prevAppData := os.Getenv("APPDATA")
	if err := os.Setenv("APPDATA", tmpRoot); err != nil {
		return fmt.Errorf("set temp APPDATA: %w", err)
	}
	defer func() {
		_ = os.Setenv("APPDATA", prevAppData)
	}()

	cardPath, err := ExportResultCardPNG(res, "Mbps", pal, false)
	if err != nil {
		return fmt.Errorf("export result card PNG: %w", err)
	}
	data, err := os.ReadFile(cardPath)
	if err != nil {
		return fmt.Errorf("read generated card PNG: %w", err)
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("write card screenshot: %w", err)
	}
	return nil
}

func sampleCurve(n int, low, high, ripple float64) []float64 {
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		t := float64(i) / math.Max(1, float64(n-1))
		ramp := 1 - math.Exp(-t*4.5)
		base := low + (high-low)*ramp
		wave := math.Sin(float64(i)*0.75)*ripple*0.6 + math.Cos(float64(i)*1.3)*ripple*0.4
		out[i] = math.Max(10, base+wave)
	}
	return out
}

func buildSampleHistory(now time.Time, client engine.ClientInfo, ooklaSrv, cfSrv engine.Server) []engine.Result {
	specs := []struct {
		ago     time.Duration
		trigger string
		prov    engine.ProviderType
		srv     engine.Server
		dl      float64
		ul      float64
		ping    float64
		jitter  float64
		loss    float64
		grade   string
	}{
		{0, "manual", engine.ProviderOokla, ooklaSrv, 486.42, 241.85, 8.4, 1.6, 0.0, "A+"},
		{1 * time.Hour, "scheduled", engine.ProviderOokla, ooklaSrv, 472.80, 238.10, 9.1, 1.9, 0.0, "A"},
		{2 * time.Hour, "scheduled", engine.ProviderOokla, ooklaSrv, 491.15, 244.62, 8.2, 1.4, 0.0, "A+"},
		{3 * time.Hour, "manual", engine.ProviderCloudflare, cfSrv, 412.60, 198.40, 14.2, 2.3, 0.0, "B"},
		{4 * time.Hour, "scheduled", engine.ProviderOokla, ooklaSrv, 458.90, 229.75, 10.4, 2.1, 0.0, "A"},
		{6 * time.Hour, "scheduled", engine.ProviderOokla, ooklaSrv, 398.25, 206.30, 16.8, 3.8, 0.2, "B"},
		{9 * time.Hour, "scheduled", engine.ProviderOokla, ooklaSrv, 504.10, 251.20, 7.9, 1.2, 0.0, "A+"},
		{14 * time.Hour, "manual", engine.ProviderCustom, engine.Server{
			ID:        "custom-hetzner",
			Name:      "nbg1-speed.hetzner.com",
			Sponsor:   "Custom HTTP Endpoint",
			LatencyMS: 24.5,
			Provider:  engine.ProviderCustom,
		}, 436.70, 0, 24.5, 2.9, 0.0, "A"},
	}

	out := make([]engine.Result, 0, len(specs))
	for i, s := range specs {
		out = append(out, engine.Result{
			ID:               fmt.Sprintf("hist-%d", i+1),
			Timestamp:        now.Add(-s.ago),
			Trigger:          s.trigger,
			Provider:         s.prov,
			Client:           client,
			Server:           s.srv,
			PingMS:           s.ping,
			JitterMS:         s.jitter,
			PacketLossPct:    s.loss,
			BufferbloatGrade: s.grade,
			DownloadMbps:     s.dl,
			UploadMbps:       s.ul,
			DurationMS:       18200,
		})
	}
	return out
}
