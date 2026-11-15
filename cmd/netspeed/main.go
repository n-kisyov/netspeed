package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"netspeed/internal/config"
	"netspeed/internal/engine"
	"netspeed/internal/history"
	"netspeed/internal/scheduler"
)

type uiControls struct {
	tabBtns       [4]widget.Clickable
	unitBtn       widget.Clickable
	themeBtn      widget.Clickable
	trayBtn       widget.Clickable
	goStopBtn     widget.Clickable
	ipToggleBtn   widget.Clickable
	copyResultBtn widget.Clickable
	saveCardBtn   widget.Clickable

	provBtns          [4]widget.Clickable
	refreshServersBtn widget.Clickable
	useCustomURLBtn   widget.Clickable
	customURLEditor   widget.Editor
	savedURLBtns      []widget.Clickable
	serverBtns        []widget.Clickable
	serverList        widget.List

	schedToggleBtn    widget.Clickable
	schedIntervalBtns [5]widget.Clickable
	applyCustomMinBtn widget.Clickable
	customMinEditor   widget.Editor
	closeToTrayBtn    widget.Clickable
	hideNowBtn        widget.Clickable
	startupToggleBtn  widget.Clickable
	notifyToggleBtn   widget.Clickable
	alertThreshBtns   [5]widget.Clickable
	threadBtns        [4]widget.Clickable
	durBtns           [3]widget.Clickable

	exportCSVBtn    widget.Clickable
	clearHistoryBtn widget.Clickable
	histCopyBtns    []widget.Clickable
	histDeleteBtns  []widget.Clickable
	historyList     widget.List
}

// App manages shared state, background engine/scheduler, Win32 tray, and Gio window rendering.
type App struct {
	mu    sync.Mutex
	win   *app.Window
	hwnd  uintptr
	cfg   config.Config
	pal   AppPalette
	th    *material.Theme
	eng   *engine.Engine
	store *history.Store
	sched *scheduler.Scheduler
	tray  *TrayController

	ui        uiControls
	activeTab int

	running        bool
	cancelTest     context.CancelFunc
	forceQuit      bool
	startHidden    bool
	appliedFirst   bool
	smokeTest      bool
	framesRendered atomic.Int64
	phase         engine.Phase
	targetMbps    float64
	displayMbps   float64
	progress      float64
	pingMS            float64
	jitterMS          float64
	packetLossPct     float64
	bufferbloatGrade  string
	loadedPingMS      float64
	downloadMbps      float64
	uploadMbps        float64
	downloadPingMS    float64
	uploadPingMS      float64
	downloadBytes     int64
	uploadBytes       int64
	dlSamples         []float64
	ulSamples         []float64
	clientInfo        engine.ClientInfo
	selectedServer    engine.Server
	lastSessionResult *engine.Result
	statusMsg         string

	lastScheduledSummary string
	startupEnabled       bool

	servers        []engine.Server
	serversLoading bool
	serversErr     string
	customURLErr   string

	historyItems []engine.Result
	toastMsg     string
	toastUntil   time.Time
}

func newApp(startMinimizedFlag bool) *App {
	cfg := config.Load()
	pal := DarkPalette()
	if cfg.Theme == "light" {
		pal = LightPalette()
	}

	store := history.DefaultStore()
	histItems, _ := store.Load()

	a := &App{
		cfg:            cfg,
		pal:            pal,
		th:             newMaterialTheme(pal),
		eng:            engine.New(),
		store:          store,
		phase:          engine.PhaseIdle,
		historyItems:   histItems,
		startHidden:    startMinimizedFlag || cfg.StartMinimized,
		startupEnabled: IsStartupEnabled(),
	}

	a.ui.customURLEditor.SingleLine = true
	a.ui.customURLEditor.Submit = true
	a.ui.customURLEditor.SetText(cfg.CustomURL)

	a.ui.customMinEditor.SingleLine = true
	a.ui.customMinEditor.Submit = true
	a.ui.customMinEditor.Filter = "0123456789"
	a.ui.customMinEditor.SetText(strconv.Itoa(cfg.ScheduleIntervalMinutes))

	a.ui.serverList.Axis = layout.Vertical
	a.ui.historyList.Axis = layout.Vertical
	a.ensureSavedURLButtonsLocked()
	a.ensureHistoryButtonsLocked()

	if cfg.Provider == string(engine.ProviderCustom) && cfg.CustomURL != "" {
		a.selectedServer = engine.Server{
			ID:       "custom-url",
			Name:     "Custom: " + cfg.CustomURL,
			Provider: engine.ProviderCustom,
			URL:      cfg.CustomURL,
		}
	} else if len(histItems) > 0 {
		last := histItems[0]
		a.clientInfo = last.Client
		if last.Server.Name != "" && (cfg.PreferredServerID == "" || cfg.PreferredServerID == last.Server.ID) {
			a.selectedServer = last.Server
		}
	}

	a.sched = scheduler.New(func() {
		a.startTest("scheduled")
	})
	a.sched.Configure(cfg.ScheduleEnabled, time.Duration(cfg.ScheduleIntervalMinutes)*time.Minute)

	a.tray = NewTrayController(TrayHooks{
		GetState: func() TrayStateSnapshot {
			a.mu.Lock()
			defer a.mu.Unlock()
			return TrayStateSnapshot{
				ScheduleEnabled: a.cfg.ScheduleEnabled,
				DarkTheme:       a.cfg.Theme == "dark",
				RunningTest:     a.running,
			}
		},
		OnOpen:        a.showFromTray,
		OnRunTest:     func() { a.startTest("manual") },
		OnToggleSched: a.toggleSchedule,
		OnToggleTheme: a.toggleTheme,
		OnExit:        a.quitApp,
	})

	return a
}

func (a *App) ensureSavedURLButtonsLocked() {
	if len(a.ui.savedURLBtns) < len(a.cfg.SavedCustomURLs)+2 {
		a.ui.savedURLBtns = make([]widget.Clickable, len(a.cfg.SavedCustomURLs)+2)
	}
}

func (a *App) ensureHistoryButtonsLocked() {
	n := len(a.historyItems)
	if len(a.ui.histCopyBtns) < n {
		a.ui.histCopyBtns = make([]widget.Clickable, n)
	}
	if len(a.ui.histDeleteBtns) < n {
		a.ui.histDeleteBtns = make([]widget.Clickable, n)
	}
}

func (a *App) toggleTheme() {
	a.mu.Lock()
	if a.cfg.Theme == "dark" {
		a.cfg.Theme = "light"
		a.pal = LightPalette()
	} else {
		a.cfg.Theme = "dark"
		a.pal = DarkPalette()
	}
	applyMaterialPalette(a.th, a.pal)
	_ = config.Save(a.cfg)
	dark := a.cfg.Theme == "dark"
	hwnd := a.hwnd
	win := a.win
	a.mu.Unlock()

	if hwnd != 0 {
		go ApplyWindowTheme(hwnd, dark)
	}
	if win != nil {
		win.Invalidate()
	}
}

func (a *App) toggleSchedule() {
	a.mu.Lock()
	a.cfg.ScheduleEnabled = !a.cfg.ScheduleEnabled
	enabled := a.cfg.ScheduleEnabled
	mins := a.cfg.ScheduleIntervalMinutes
	_ = config.Save(a.cfg)
	win := a.win
	a.mu.Unlock()

	a.sched.Configure(enabled, time.Duration(mins)*time.Minute)
	if enabled {
		a.showToast(fmt.Sprintf("Scheduled tests enabled (every %d min)", mins))
	} else {
		a.showToast("Scheduled tests disabled")
	}
	if win != nil {
		win.Invalidate()
	}
}

func (a *App) setScheduleInterval(minutes int, enable bool) {
	if minutes < 1 {
		minutes = 1
	}
	a.mu.Lock()
	a.cfg.ScheduleIntervalMinutes = minutes
	if enable {
		a.cfg.ScheduleEnabled = true
	}
	enabled := a.cfg.ScheduleEnabled
	_ = config.Save(a.cfg)
	win := a.win
	a.mu.Unlock()

	a.ui.customMinEditor.SetText(strconv.Itoa(minutes))
	a.sched.Configure(enabled, time.Duration(minutes)*time.Minute)
	if win != nil {
		win.Invalidate()
	}
}

func (a *App) hideToTray() {
	a.mu.Lock()
	hwnd := a.hwnd
	a.mu.Unlock()
	if hwnd != 0 {
		go HideWindowToTray(hwnd)
	}
}

func (a *App) showFromTray() {
	a.mu.Lock()
	hwnd := a.hwnd
	win := a.win
	a.mu.Unlock()
	if hwnd != 0 {
		go ShowWindowFromTray(hwnd)
	}
	if win != nil {
		win.Perform(system.ActionRaise)
		win.Invalidate()
	}
}

func (a *App) quitApp() {
	a.mu.Lock()
	a.forceQuit = true
	cancel := a.cancelTest
	win := a.win
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	a.sched.Stop()
	a.tray.Close()
	if win != nil {
		win.Perform(system.ActionClose)
	}
	time.AfterFunc(350*time.Millisecond, func() {
		os.Exit(0)
	})
}

func (a *App) showToast(msg string) {
	a.mu.Lock()
	a.toastMsg = msg
	a.toastUntil = time.Now().Add(3 * time.Second)
	win := a.win
	a.mu.Unlock()
	if win != nil {
		win.Invalidate()
		time.AfterFunc(3100*time.Millisecond, win.Invalidate)
	}
}

func (a *App) reloadHistory() {
	items, err := a.store.Load()
	if err != nil {
		return
	}
	a.mu.Lock()
	a.historyItems = items
	a.ensureHistoryButtonsLocked()
	win := a.win
	a.mu.Unlock()
	if win != nil {
		win.Invalidate()
	}
}

func (a *App) refreshServersAsync() {
	a.mu.Lock()
	if a.serversLoading {
		a.mu.Unlock()
		return
	}
	a.serversLoading = true
	a.serversErr = ""
	prov := engine.ProviderType(a.cfg.Provider)
	if prov == engine.ProviderCustom {
		prov = engine.ProviderAuto
	}
	win := a.win
	a.mu.Unlock()

	if win != nil {
		win.Invalidate()
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		client, servers, err := a.eng.ListServers(ctx, prov)

		a.mu.Lock()
		a.serversLoading = false
		if err != nil {
			a.serversErr = err.Error()
		} else {
			a.servers = servers
			a.ui.serverBtns = make([]widget.Clickable, len(servers))
			if client.IP != "" {
				a.clientInfo = client
			}
			if a.cfg.PreferredServerID != "" {
				for _, s := range servers {
					if s.ID == a.cfg.PreferredServerID {
						a.selectedServer = s
						break
					}
				}
			}
			if a.cfg.Provider != string(engine.ProviderCustom) && len(servers) > 0 {
				mismatch := a.selectedServer.Name == "" ||
					(a.cfg.Provider != "auto" && string(a.selectedServer.Provider) != a.cfg.Provider)
				if mismatch {
					a.selectedServer = servers[0]
				}
			}
		}
		w := a.win
		a.mu.Unlock()
		if w != nil {
			w.Invalidate()
		}
	}()
}

func (a *App) stopCurrentTest() {
	a.mu.Lock()
	cancel := a.cancelTest
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) startTest(trigger string) {
	a.mu.Lock()
	if a.running || a.eng.IsRunning() {
		a.mu.Unlock()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.cancelTest = cancel
	a.running = true
	a.phase = engine.PhaseInit
	a.targetMbps = 0
	a.displayMbps = 0
	a.progress = 0
	a.pingMS = 0
	a.jitterMS = 0
	a.packetLossPct = 0
	a.bufferbloatGrade = ""
	a.loadedPingMS = 0
	a.downloadMbps = 0
	a.uploadMbps = 0
	a.downloadPingMS = 0
	a.uploadPingMS = 0
	a.downloadBytes = 0
	a.uploadBytes = 0
	a.dlSamples = nil
	a.ulSamples = nil
	a.statusMsg = "Starting speed test..."

	testCfg := engine.TestConfig{
		Provider:  engine.ProviderType(a.cfg.Provider),
		ServerID:  a.cfg.PreferredServerID,
		CustomURL: a.cfg.CustomURL,
		Threads:   a.cfg.Threads,
		Duration:  time.Duration(a.cfg.DurationSeconds) * time.Second,
		Trigger:   trigger,
	}
	if testCfg.Provider != engine.ProviderCustom {
		testCfg.CustomURL = ""
	}
	win := a.win
	a.mu.Unlock()

	if win != nil {
		win.Invalidate()
	}
	a.tray.UpdateTooltip("NetSpeed — Running test...")

	go func() {
		defer cancel()
		res, err := a.eng.Run(ctx, testCfg, func(ev engine.ProgressEvent) {
			a.mu.Lock()
			a.phase = ev.Phase
			a.progress = ev.Progress
			if ev.Client.IP != "" {
				a.clientInfo = ev.Client
			}
			if ev.Server.Name != "" {
				a.selectedServer = ev.Server
			}
			if ev.PingMS > 0 {
				a.pingMS = ev.PingMS
				a.jitterMS = ev.JitterMS
			}
			if ev.Phase == engine.PhasePing && ev.Progress >= 1 {
				a.packetLossPct = ev.PacketLossPct
			}
			if ev.LoadedPingMS > 0 {
				a.loadedPingMS = ev.LoadedPingMS
			}
			if ev.DownloadPingMS > 0 {
				a.downloadPingMS = ev.DownloadPingMS
			}
			if ev.UploadPingMS > 0 {
				a.uploadPingMS = ev.UploadPingMS
			}
			switch ev.Phase {
			case engine.PhaseDownload:
				a.targetMbps = ev.CurrentMbps
				a.downloadMbps = ev.CurrentMbps
				a.downloadBytes = ev.BytesTransferred
				a.dlSamples = ev.Samples
			case engine.PhaseUpload:
				a.targetMbps = ev.CurrentMbps
				a.uploadMbps = ev.CurrentMbps
				a.uploadBytes = ev.BytesTransferred
				a.ulSamples = ev.Samples
			case engine.PhasePing, engine.PhaseInit:
				a.targetMbps = 0
			case engine.PhaseDone:
				a.targetMbps = 0
				if ev.Result != nil {
					a.downloadMbps = ev.Result.DownloadMbps
					a.uploadMbps = ev.Result.UploadMbps
					a.downloadBytes = ev.Result.DownloadBytes
					a.uploadBytes = ev.Result.UploadBytes
					a.downloadPingMS = ev.Result.DownloadPingMS
					a.uploadPingMS = ev.Result.UploadPingMS
					a.packetLossPct = ev.Result.PacketLossPct
					a.bufferbloatGrade = ev.Result.BufferbloatGrade
				}
			}
			if ev.Message != "" {
				a.statusMsg = ev.Message
			}
			w := a.win
			a.mu.Unlock()
			if w != nil {
				w.Invalidate()
			}
		})

		a.mu.Lock()
		a.running = false
		a.cancelTest = nil
		a.targetMbps = 0
		if err != nil {
			if ctx.Err() != nil {
				a.phase = engine.PhaseIdle
				a.statusMsg = "Speed test stopped"
			} else {
				a.phase = engine.PhaseError
				a.statusMsg = "Error: " + err.Error()
			}
		} else {
			a.phase = engine.PhaseDone
			resCopy := res
			a.lastSessionResult = &resCopy
			a.packetLossPct = res.PacketLossPct
			a.bufferbloatGrade = res.BufferbloatGrade
			a.statusMsg = fmt.Sprintf("Completed (%s) in %.1fs", res.Trigger, float64(res.DurationMS)/1000.0)
			if trigger == "scheduled" {
				a.lastScheduledSummary = fmt.Sprintf("%s · ↓ %.1f Mbps · ↑ %.1f Mbps · %.1f ms",
					res.Timestamp.Local().Format("15:04"), res.DownloadMbps, res.UploadMbps, res.PingMS)
			}
		}
		unitName := a.cfg.Unit
		trayNotify := a.cfg.TrayNotifications
		alertMin := a.cfg.AlertMinDownloadMbps
		w := a.win
		a.mu.Unlock()

		if err == nil {
			_ = a.store.Append(res)
			a.reloadHistory()
			dlV, dlU := engine.FormatSpeed(res.DownloadMbps, unitName)
			ulV, ulU := engine.FormatSpeed(res.UploadMbps, unitName)
			a.tray.UpdateTooltip(fmt.Sprintf("NetSpeed — ↓ %.1f %s | ↑ %.1f %s | %.0f ms", dlV, dlU, ulV, ulU, res.PingMS))
			if trayNotify {
				if alertMin > 0 && res.DownloadMbps < alertMin {
					a.tray.ShowNotification(
						"NetSpeed — Low Speed Alert",
						fmt.Sprintf("Download %.1f Mbps is below %.0f Mbps threshold (Ping %.0f ms)", res.DownloadMbps, alertMin, res.PingMS),
						true,
					)
				} else if trigger == "scheduled" {
					gradeTxt := res.BufferbloatGrade
					if gradeTxt == "" {
						gradeTxt = "N/A"
					}
					a.tray.ShowNotification(
						"NetSpeed — Scheduled Test Complete",
						fmt.Sprintf("DL %.1f %s | UL %.1f %s | Ping %.0f ms (Grade %s)", dlV, dlU, ulV, ulU, res.PingMS, gradeTxt),
						false,
					)
				}
			}
		} else {
			a.tray.UpdateTooltip("NetSpeed — Ready")
		}

		if w != nil {
			w.Invalidate()
		}
	}()
}

func formatResultSummary(r engine.Result, unitName string) string {
	dlV, dlU := engine.FormatSpeed(r.DownloadMbps, unitName)
	ulV, ulU := engine.FormatSpeed(r.UploadMbps, unitName)
	trig := r.Trigger
	if trig == "" {
		trig = "manual"
	}
	grade := r.BufferbloatGrade
	if grade == "" {
		grade = "—"
	}
	return fmt.Sprintf(
		"NetSpeed Result (%s)\r\nTime: %s\r\nProvider: %s\r\nServer: %s\r\nIdle Ping: %.2f ms (Jitter: %.2f ms, Loss: %.1f%%)\r\nBufferbloat Grade: %s\r\nDownload: %.2f %s (Loaded Ping: %.1f ms)\r\nUpload: %.2f %s (Loaded Ping: %.1f ms)\r\n",
		strings.ToUpper(trig),
		r.Timestamp.Local().Format("2006-01-02 15:04:05"),
		strings.ToUpper(string(r.Provider)),
		r.Server.Name,
		r.PingMS, r.JitterMS, r.PacketLossPct,
		grade,
		dlV, dlU, r.DownloadPingMS,
		ulV, ulU, r.UploadPingMS,
	)
}

func (a *App) buildClipboardSummary() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastSessionResult == nil {
		return "", false
	}
	return formatResultSummary(*a.lastSessionResult, a.cfg.Unit), true
}

func (a *App) runWindowLoop() error {
	w := new(app.Window)
	w.Option(
		app.Title("NetSpeed"),
		app.Size(unit.Dp(880), unit.Dp(660)),
		app.MinSize(unit.Dp(740), unit.Dp(560)),
	)

	a.mu.Lock()
	a.win = w
	a.mu.Unlock()

	a.refreshServersAsync()

	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.Win32ViewEvent:
			if e.Valid() {
				a.mu.Lock()
				a.hwnd = e.HWND
				dark := a.cfg.Theme == "dark"
				shouldHide := a.startHidden && !a.appliedFirst
				a.appliedFirst = true
				a.mu.Unlock()

				hIcon := a.tray.IconHandle()
				go func(hwnd uintptr, darkTheme bool, hide bool, icon uintptr) {
					ApplyWindowTheme(hwnd, darkTheme)
					ApplyAppIcon(hwnd, icon)
					if hide {
						HideWindowToTray(hwnd)
					}
				}(e.HWND, dark, shouldHide, hIcon)
			}
		case *app.ClosingEvent:
			a.mu.Lock()
			forceQuit := a.forceQuit
			closeToTray := a.cfg.CloseToTray
			a.mu.Unlock()
			if !forceQuit && closeToTray {
				e.Abort()
				a.hideToTray()
			}
		case app.DestroyEvent:
			a.sched.Stop()
			a.tray.Close()
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			a.layoutFrame(gtx)
			e.Frame(gtx.Ops)
			a.framesRendered.Add(1)
		}
	}
}

func main() {
	minimized := flag.Bool("minimized", false, "Start minimized to the Windows system tray")
	smokeTest := flag.Bool("smoke-test", false, "Exit cleanly after verifying frames render without freezing")
	flag.Parse()

	application := newApp(*minimized)
	application.smokeTest = *smokeTest

	if *smokeTest {
		go func() {
			deadline := time.Now().Add(6 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(300 * time.Millisecond)
				if application.framesRendered.Load() >= 3 {
					time.Sleep(1200 * time.Millisecond)
					application.sched.Stop()
					application.tray.Close()
					os.Exit(0)
				}
			}
			application.sched.Stop()
			application.tray.Close()
			os.Exit(2)
		}()
	}

	go func() {
		if err := application.runWindowLoop(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}
