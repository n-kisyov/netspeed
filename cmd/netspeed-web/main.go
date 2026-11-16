package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"netspeed/internal/card"
	"netspeed/internal/config"
	"netspeed/internal/engine"
	"netspeed/internal/history"
	"netspeed/internal/scheduler"
)

//go:embed web/*
var embeddedWebFS embed.FS

type alertEvent struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"` // "info" or "warning"
	Title     string `json:"title"`
	Body      string `json:"body"`
	Timestamp int64  `json:"timestamp"`
}

type stateSnapshot struct {
	Running              bool               `json:"running"`
	Phase                engine.Phase       `json:"phase"`
	Progress             float64            `json:"progress"`
	CurrentMbps          float64            `json:"current_mbps"`
	PingMS               float64            `json:"ping_ms"`
	JitterMS             float64            `json:"jitter_ms"`
	PacketLossPct        float64            `json:"packet_loss_pct"`
	LoadedPingMS         float64            `json:"loaded_ping_ms"`
	DownloadMbps         float64            `json:"download_mbps"`
	UploadMbps           float64            `json:"upload_mbps"`
	DownloadPingMS       float64            `json:"download_ping_ms"`
	UploadPingMS         float64            `json:"upload_ping_ms"`
	DownloadBytes        int64              `json:"download_bytes"`
	UploadBytes          int64              `json:"upload_bytes"`
	DownloadSamples      []float64          `json:"dl_samples"`
	UploadSamples        []float64          `json:"ul_samples"`
	BufferbloatGrade     string             `json:"bufferbloat_grade"`
	StatusMsg            string             `json:"status_msg"`
	Client               engine.ClientInfo  `json:"client"`
	Server               engine.Server      `json:"server"`
	Config               config.Config      `json:"config"`
	Servers              []engine.Server    `json:"servers"`
	ServersLoading       bool               `json:"servers_loading"`
	ServersErr           string             `json:"servers_err"`
	CustomURLErr         string             `json:"custom_url_err"`
	LastSessionResult    *engine.Result     `json:"last_session_result"`
	ClipboardSummary     string             `json:"clipboard_summary"`
	LastScheduledSummary string             `json:"last_scheduled_summary"`
	LastAlert            *alertEvent        `json:"last_alert,omitempty"`
	NextRunUnixMS        int64              `json:"next_run_unix_ms"`
	ServerTimeUnixMS     int64              `json:"server_time_unix_ms"`
	StartupEnabled       bool               `json:"startup_enabled"`
	StartupSupported     bool               `json:"startup_supported"`
	SystemdUnitPreview   string             `json:"systemd_unit_preview"`
	HistoryCount         int                `json:"history_count"`
	Platform             string             `json:"platform"`
	DataDir              string             `json:"data_dir"`
}

// WebApp coordinates the speed test engine, scheduler, history store, config, and SSE subscribers.
type WebApp struct {
	mu         sync.Mutex
	listenAddr string
	dataDir    string
	cfg        config.Config
	eng        *engine.Engine
	store      *history.Store
	sched      *scheduler.Scheduler

	running           bool
	cancelTest        context.CancelFunc
	phase             engine.Phase
	progress          float64
	currentMbps       float64
	pingMS            float64
	jitterMS          float64
	packetLossPct     float64
	loadedPingMS      float64
	downloadMbps      float64
	uploadMbps        float64
	downloadPingMS    float64
	uploadPingMS      float64
	downloadBytes     int64
	uploadBytes       int64
	dlSamples         []float64
	ulSamples         []float64
	bufferbloatGrade  string
	statusMsg         string
	clientInfo        engine.ClientInfo
	selectedServer    engine.Server
	lastSessionResult *engine.Result

	servers              []engine.Server
	serversLoading       bool
	serversErr           string
	customURLErr         string
	startupEnabled       bool
	lastScheduledSummary string
	alertSeq             int64
	lastAlert            *alertEvent
	historyCount         int

	sseMu      sync.Mutex
	sseClients map[chan []byte]struct{}
	lastPush   time.Time
}

func newWebApp(listenAddr, dataDir string, discoverOnStart bool) *WebApp {
	cfg := config.Load()
	store := history.DefaultStore()
	histItems, _ := store.Load()

	a := &WebApp{
		listenAddr:     listenAddr,
		dataDir:        dataDir,
		cfg:            cfg,
		eng:            engine.New(),
		store:          store,
		phase:          engine.PhaseIdle,
		statusMsg:      "Ready to test",
		startupEnabled: isAutostartEnabled(),
		historyCount:   len(histItems),
		sseClients:     make(map[chan []byte]struct{}),
	}

	if cfg.Provider == "custom" && cfg.CustomURL != "" {
		a.selectedServer = engine.Server{
			ID:       "custom-url",
			Name:     cfg.CustomURL,
			Sponsor:  "Custom URL",
			URL:      cfg.CustomURL,
			Provider: engine.ProviderCustom,
		}
	}

	a.sched = scheduler.New(func() {
		a.startTest("scheduled")
	})
	if cfg.ScheduleEnabled {
		a.sched.Configure(true, time.Duration(cfg.ScheduleIntervalMinutes)*time.Minute)
	}

	if discoverOnStart {
		a.refreshServersAsync()
	}
	return a
}

func (a *WebApp) close() {
	a.mu.Lock()
	cancel := a.cancelTest
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if a.sched != nil {
		a.sched.Stop()
	}
}

func (a *WebApp) snapshotLocked() stateSnapshot {
	var nextRunMS int64
	if a.sched != nil {
		if nr := a.sched.NextRun(); !nr.IsZero() {
			nextRunMS = nr.UnixMilli()
		}
	}

	var sessionResCopy *engine.Result
	clipSummary := ""
	if a.lastSessionResult != nil {
		cp := *a.lastSessionResult
		cp.DownloadSamples = append([]float64(nil), a.lastSessionResult.DownloadSamples...)
		cp.UploadSamples = append([]float64(nil), a.lastSessionResult.UploadSamples...)
		sessionResCopy = &cp
		clipSummary = card.FormatResultSummary(cp, a.cfg.Unit, false)
	}

	var alertCopy *alertEvent
	if a.lastAlert != nil {
		cp := *a.lastAlert
		alertCopy = &cp
	}

	return stateSnapshot{
		Running:              a.running,
		Phase:                a.phase,
		Progress:             a.progress,
		CurrentMbps:          a.currentMbps,
		PingMS:               a.pingMS,
		JitterMS:             a.jitterMS,
		PacketLossPct:        a.packetLossPct,
		LoadedPingMS:         a.loadedPingMS,
		DownloadMbps:         a.downloadMbps,
		UploadMbps:           a.uploadMbps,
		DownloadPingMS:       a.downloadPingMS,
		UploadPingMS:         a.uploadPingMS,
		DownloadBytes:        a.downloadBytes,
		UploadBytes:          a.uploadBytes,
		DownloadSamples:      append([]float64(nil), a.dlSamples...),
		UploadSamples:        append([]float64(nil), a.ulSamples...),
		BufferbloatGrade:     a.bufferbloatGrade,
		StatusMsg:            a.statusMsg,
		Client:               a.clientInfo,
		Server:               a.selectedServer,
		Config:               a.cfg,
		Servers:              append([]engine.Server(nil), a.servers...),
		ServersLoading:       a.serversLoading,
		ServersErr:           a.serversErr,
		CustomURLErr:         a.customURLErr,
		LastSessionResult:    sessionResCopy,
		ClipboardSummary:     clipSummary,
		LastScheduledSummary: a.lastScheduledSummary,
		LastAlert:            alertCopy,
		NextRunUnixMS:        nextRunMS,
		ServerTimeUnixMS:     time.Now().UnixMilli(),
		StartupEnabled:       a.startupEnabled,
		StartupSupported:     autostartSupported(),
		SystemdUnitPreview:   systemdUnitPreview(a.listenAddr),
		HistoryCount:         a.historyCount,
		Platform:             runtime.GOOS + "/" + runtime.GOARCH,
		DataDir:              config.AppDir(),
	}
}

func (a *WebApp) snapshot() stateSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshotLocked()
}

func (a *WebApp) broadcast(force bool) {
	a.sseMu.Lock()
	if len(a.sseClients) == 0 {
		a.sseMu.Unlock()
		return
	}
	if !force && time.Since(a.lastPush) < 65*time.Millisecond {
		a.sseMu.Unlock()
		return
	}
	a.lastPush = time.Now()
	a.sseMu.Unlock()

	snap := a.snapshot()
	payload, err := json.Marshal(snap)
	if err != nil {
		return
	}

	a.sseMu.Lock()
	for ch := range a.sseClients {
		select {
		case ch <- payload:
		default:
			// Drop stale queued frame if subscriber cannot keep up; non-blocking broadcast.
		}
	}
	a.sseMu.Unlock()
}

func (a *WebApp) refreshServersAsync() {
	a.mu.Lock()
	if a.serversLoading {
		a.mu.Unlock()
		return
	}
	prov := engine.ProviderType(a.cfg.Provider)
	prefID := a.cfg.PreferredServerID
	a.serversLoading = true
	a.serversErr = ""
	a.mu.Unlock()
	a.broadcast(true)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()

		discProv := prov
		if discProv == engine.ProviderCustom || discProv == "" {
			discProv = engine.ProviderAuto
		}
		client, srvs, err := a.eng.ListServers(ctx, discProv)

		a.mu.Lock()
		a.serversLoading = false
		if err != nil {
			a.serversErr = err.Error()
		} else {
			a.servers = srvs
			if client.IP != "" || client.ISP != "" {
				a.clientInfo = client
			}
			if a.cfg.Provider != "custom" {
				if len(srvs) > 0 {
					chosen := srvs[0]
					if prefID != "" {
						for _, s := range srvs {
							if s.ID == prefID {
								chosen = s
								break
							}
						}
					}
					a.selectedServer = chosen
				} else {
					a.selectedServer = engine.Server{}
				}
			}
		}
		a.mu.Unlock()
		a.broadcast(true)
	}()
}

func (a *WebApp) startTest(trigger string) bool {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.running = true
	a.cancelTest = cancel
	a.phase = engine.PhaseInit
	a.progress = 0
	a.currentMbps = 0
	a.pingMS = 0
	a.jitterMS = 0
	a.packetLossPct = 0
	a.loadedPingMS = 0
	a.downloadMbps = 0
	a.uploadMbps = 0
	a.downloadPingMS = 0
	a.uploadPingMS = 0
	a.downloadBytes = 0
	a.uploadBytes = 0
	a.dlSamples = nil
	a.ulSamples = nil
	a.bufferbloatGrade = ""
	a.customURLErr = ""
	a.statusMsg = "Initializing speed test..."

	cfgCopy := a.cfg
	prefServer := a.selectedServer
	a.mu.Unlock()
	a.broadcast(true)

	go func() {
		defer cancel()

		testCfg := engine.TestConfig{
			Provider:  engine.ProviderType(cfgCopy.Provider),
			ServerID:  cfgCopy.PreferredServerID,
			CustomURL: cfgCopy.CustomURL,
			Threads:   cfgCopy.Threads,
			Duration:  time.Duration(cfgCopy.DurationSeconds) * time.Second,
			Trigger:   trigger,
		}
		if testCfg.Provider != engine.ProviderCustom {
			testCfg.CustomURL = ""
		}
		if testCfg.ServerID == "" && prefServer.ID != "" && prefServer.Provider == testCfg.Provider {
			testCfg.ServerID = prefServer.ID
		}

		res, err := a.eng.Run(ctx, testCfg, func(ev engine.ProgressEvent) {
			a.mu.Lock()
			prevPhase := a.phase
			a.phase = ev.Phase
			a.progress = ev.Progress
			if ev.Client.IP != "" || ev.Client.ISP != "" {
				a.clientInfo = ev.Client
			}
			if ev.Server.Name != "" {
				a.selectedServer = ev.Server
			}
			if ev.PingMS > 0 {
				a.pingMS = ev.PingMS
			}
			if ev.JitterMS > 0 {
				a.jitterMS = ev.JitterMS
			}
			if ev.PacketLossPct > 0 || ev.Phase == engine.PhasePing {
				a.packetLossPct = ev.PacketLossPct
			}
			if ev.LoadedPingMS > 0 {
				a.loadedPingMS = ev.LoadedPingMS
			}
			if ev.Message != "" {
				a.statusMsg = ev.Message
			}
			switch ev.Phase {
			case engine.PhaseDownload:
				a.currentMbps = ev.CurrentMbps
				a.downloadMbps = ev.CurrentMbps
				a.downloadBytes = ev.BytesTransferred
				a.dlSamples = append([]float64(nil), ev.Samples...)
				if ev.LoadedPingMS > 0 {
					a.downloadPingMS = ev.LoadedPingMS
				}
			case engine.PhaseUpload:
				a.currentMbps = ev.CurrentMbps
				a.uploadMbps = ev.CurrentMbps
				a.uploadBytes = ev.BytesTransferred
				a.ulSamples = append([]float64(nil), ev.Samples...)
				if ev.LoadedPingMS > 0 {
					a.uploadPingMS = ev.LoadedPingMS
				}
			case engine.PhaseDone:
				a.currentMbps = 0
				a.loadedPingMS = 0
				if ev.Result != nil {
					a.downloadMbps = ev.Result.DownloadMbps
					a.uploadMbps = ev.Result.UploadMbps
					a.downloadBytes = ev.Result.DownloadBytes
					a.uploadBytes = ev.Result.UploadBytes
					a.pingMS = ev.Result.PingMS
					a.jitterMS = ev.Result.JitterMS
					a.packetLossPct = ev.Result.PacketLossPct
					a.downloadPingMS = ev.Result.DownloadPingMS
					a.uploadPingMS = ev.Result.UploadPingMS
					a.bufferbloatGrade = ev.Result.BufferbloatGrade
				}
			}
			forcePush := prevPhase != ev.Phase || ev.Phase == engine.PhasePing || ev.Phase == engine.PhaseDone || ev.Phase == engine.PhaseError
			a.mu.Unlock()
			a.broadcast(forcePush)
		})

		a.mu.Lock()
		a.running = false
		a.cancelTest = nil
		if err != nil {
			if errors.Is(err, context.Canceled) {
				a.phase = engine.PhaseIdle
				a.currentMbps = 0
				a.statusMsg = "Speed test stopped"
			} else {
				a.phase = engine.PhaseError
				a.currentMbps = 0
				a.statusMsg = "Error: " + err.Error()
			}
		} else {
			resCopy := res
			a.lastSessionResult = &resCopy
			a.phase = engine.PhaseDone
			a.bufferbloatGrade = res.BufferbloatGrade
			a.statusMsg = fmt.Sprintf("Completed (%s) in %.1fs", res.Trigger, float64(res.DurationMS)/1000.0)
			if trigger == "scheduled" {
				dlV, dlU := engine.FormatSpeed(res.DownloadMbps, a.cfg.Unit)
				ulV, ulU := engine.FormatSpeed(res.UploadMbps, a.cfg.Unit)
				a.lastScheduledSummary = fmt.Sprintf("%.1f %s down / %.1f %s up / %.1f ms ping (%s)",
					dlV, dlU, ulV, ulU, res.PingMS, res.Timestamp.Local().Format("15:04"))
				if a.cfg.TrayNotifications {
					a.alertSeq++
					if a.cfg.AlertMinDownloadMbps > 0 && res.DownloadMbps < a.cfg.AlertMinDownloadMbps {
						a.lastAlert = &alertEvent{
							ID:        a.alertSeq,
							Kind:      "warning",
							Title:     "NetSpeed Low Speed Alert",
							Body:      fmt.Sprintf("Download dropped to %.1f Mbps (threshold %.0f Mbps) on %s", res.DownloadMbps, a.cfg.AlertMinDownloadMbps, res.Server.Name),
							Timestamp: time.Now().UnixMilli(),
						}
					} else {
						a.lastAlert = &alertEvent{
							ID:        a.alertSeq,
							Kind:      "info",
							Title:     "NetSpeed Scheduled Test Complete",
							Body:      fmt.Sprintf("Download %.1f %s · Upload %.1f %s · Ping %.1f ms · Grade %s", dlV, dlU, ulV, ulU, res.PingMS, res.BufferbloatGrade),
							Timestamp: time.Now().UnixMilli(),
						}
					}
				}
			}
		}
		a.mu.Unlock()

		if err == nil {
			_ = a.store.Append(res)
			if items, herr := a.store.Load(); herr == nil {
				a.mu.Lock()
				a.historyCount = len(items)
				a.mu.Unlock()
			}
		}
		a.broadcast(true)
	}()
	return true
}

func (a *WebApp) stopCurrentTest() {
	a.mu.Lock()
	cancel := a.cancelTest
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type configPatch struct {
	Theme                   *string  `json:"theme"`
	Unit                    *string  `json:"unit"`
	Provider                *string  `json:"provider"`
	PreferredServerID       *string  `json:"preferred_server_id"`
	CustomURL               *string  `json:"custom_url"`
	ApplyCustomURL          *bool    `json:"apply_custom_url"`
	Threads                 *int     `json:"threads"`
	DurationSeconds         *int     `json:"duration_seconds"`
	HideIP                  *bool    `json:"hide_ip"`
	ScheduleEnabled         *bool    `json:"schedule_enabled"`
	ScheduleIntervalMinutes *int     `json:"schedule_interval_minutes"`
	TrayNotifications       *bool    `json:"tray_notifications"`
	AlertMinDownloadMbps    *float64 `json:"alert_min_download_mbps"`
	StartupEnabled          *bool    `json:"startup_enabled"`
}

func (a *WebApp) applyConfigPatch(patch configPatch) error {
	var (
		needServerRefresh bool
		scheduleChanged   bool
		schedEnabled      bool
		schedMinutes      int
		startupErr        error
	)

	a.mu.Lock()
	if patch.Theme != nil {
		if *patch.Theme == "light" {
			a.cfg.Theme = "light"
		} else {
			a.cfg.Theme = "dark"
		}
	}
	if patch.Unit != nil {
		switch *patch.Unit {
		case "Mbps", "MB/s", "Kbps":
			a.cfg.Unit = *patch.Unit
		}
	}
	if patch.HideIP != nil {
		a.cfg.HideIP = *patch.HideIP
	}
	if patch.Threads != nil {
		t := *patch.Threads
		if t >= 1 && t <= 32 {
			a.cfg.Threads = t
		}
	}
	if patch.DurationSeconds != nil {
		d := *patch.DurationSeconds
		if d >= 1 && d <= 60 {
			a.cfg.DurationSeconds = d
		}
	}
	if patch.TrayNotifications != nil {
		a.cfg.TrayNotifications = *patch.TrayNotifications
	}
	if patch.AlertMinDownloadMbps != nil {
		v := *patch.AlertMinDownloadMbps
		if v < 0 {
			v = 0
		}
		a.cfg.AlertMinDownloadMbps = v
	}

	if patch.ApplyCustomURL != nil && *patch.ApplyCustomURL {
		raw := ""
		if patch.CustomURL != nil {
			raw = *patch.CustomURL
		}
		norm, err := engine.NormalizeCustomURL(raw)
		if err != nil {
			a.customURLErr = err.Error()
			a.mu.Unlock()
			a.broadcast(true)
			return err
		}
		a.customURLErr = ""
		a.cfg.Provider = string(engine.ProviderCustom)
		a.cfg.AddSavedCustomURL(norm)
		a.cfg.PreferredServerID = ""
		a.selectedServer = engine.Server{
			ID:       "custom-url",
			Name:     norm,
			Sponsor:  "Custom URL",
			URL:      norm,
			Provider: engine.ProviderCustom,
		}
		a.statusMsg = "Custom endpoint configured: " + norm
	} else if patch.CustomURL != nil && patch.Provider == nil {
		norm, err := engine.NormalizeCustomURL(*patch.CustomURL)
		if err != nil {
			a.customURLErr = err.Error()
			a.mu.Unlock()
			a.broadcast(true)
			return err
		}
		a.customURLErr = ""
		a.cfg.AddSavedCustomURL(norm)
	}

	if patch.Provider != nil {
		p := strings.ToLower(strings.TrimSpace(*patch.Provider))
		switch p {
		case "auto", "ookla", "cloudflare", "custom":
			prevProv := a.cfg.Provider
			a.cfg.Provider = p
			if p == "custom" {
				if a.cfg.CustomURL != "" {
					a.selectedServer = engine.Server{
						ID:       "custom-url",
						Name:     a.cfg.CustomURL,
						Sponsor:  "Custom URL",
						URL:      a.cfg.CustomURL,
						Provider: engine.ProviderCustom,
					}
				} else {
					a.selectedServer = engine.Server{Name: "Enter a Custom Server URL", Provider: engine.ProviderCustom}
				}
			} else if prevProv != p {
				a.cfg.PreferredServerID = ""
				a.selectedServer = engine.Server{}
				needServerRefresh = true
			}
		}
	}

	if patch.PreferredServerID != nil {
		sid := strings.TrimSpace(*patch.PreferredServerID)
		a.cfg.PreferredServerID = sid
		for _, s := range a.servers {
			if s.ID == sid {
				a.cfg.Provider = string(s.Provider)
				a.selectedServer = s
				break
			}
		}
	}

	if patch.ScheduleEnabled != nil {
		a.cfg.ScheduleEnabled = *patch.ScheduleEnabled
		scheduleChanged = true
	}
	if patch.ScheduleIntervalMinutes != nil {
		mins := *patch.ScheduleIntervalMinutes
		if mins < 1 {
			mins = 1
		}
		if mins > 10080 {
			mins = 10080
		}
		a.cfg.ScheduleIntervalMinutes = mins
		if patch.ScheduleEnabled == nil {
			a.cfg.ScheduleEnabled = true
		}
		scheduleChanged = true
	}
	schedEnabled = a.cfg.ScheduleEnabled
	schedMinutes = a.cfg.ScheduleIntervalMinutes
	cfgToSave := a.cfg
	addr := a.listenAddr
	dataDir := a.dataDir
	a.mu.Unlock()

	if scheduleChanged && a.sched != nil {
		a.sched.Configure(schedEnabled, time.Duration(schedMinutes)*time.Minute)
	}

	if patch.StartupEnabled != nil {
		if err := setAutostartEnabled(*patch.StartupEnabled, addr, dataDir); err != nil {
			startupErr = err
		} else {
			a.mu.Lock()
			a.startupEnabled = *patch.StartupEnabled
			a.mu.Unlock()
		}
	}

	_ = config.Save(cfgToSave)
	if needServerRefresh {
		a.refreshServersAsync()
	} else {
		a.broadcast(true)
	}
	return startupErr
}

func (a *WebApp) routes() http.Handler {
	mux := http.NewServeMux()
	webRoot, err := fs.Sub(embeddedWebFS, "web")
	if err != nil {
		log.Fatalf("failed opening embedded web fs: %v", err)
	}
	fileServer := http.FileServer(http.FS(webRoot))

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			data, err := fs.ReadFile(webRoot, "index.html")
			if err != nil {
				http.Error(w, "missing index.html", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(data)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		fileServer.ServeHTTP(w, r)
	})

	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.snapshot())
	})

	mux.HandleFunc("GET /api/events", a.handleSSE)

	mux.HandleFunc("POST /api/test/start", func(w http.ResponseWriter, r *http.Request) {
		if !a.startTest("manual") {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "a speed test is already in progress",
				"state": a.snapshot(),
			})
			return
		}
		writeJSON(w, http.StatusAccepted, a.snapshot())
	})

	mux.HandleFunc("POST /api/test/stop", func(w http.ResponseWriter, r *http.Request) {
		a.stopCurrentTest()
		writeJSON(w, http.StatusOK, a.snapshot())
	})

	mux.HandleFunc("GET /api/servers", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("refresh") == "1" {
			a.refreshServersAsync()
		}
		writeJSON(w, http.StatusOK, a.snapshot())
	})

	mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
		var patch configPatch
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload: " + err.Error()})
			return
		}
		if err := a.applyConfigPatch(patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": err.Error(),
				"state": a.snapshot(),
			})
			return
		}
		writeJSON(w, http.StatusOK, a.snapshot())
	})

	mux.HandleFunc("GET /api/history", func(w http.ResponseWriter, r *http.Request) {
		items, err := a.store.Load()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if items == nil {
			items = []engine.Result{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	})

	mux.HandleFunc("DELETE /api/history", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("all") == "1" {
			if err := a.store.Clear(); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
		} else if id := strings.TrimSpace(q.Get("id")); id != "" {
			if err := a.store.DeleteByID(id); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "provide ?id=<id> or ?all=1"})
			return
		}

		items, _ := a.store.Load()
		if items == nil {
			items = []engine.Result{}
		}
		a.mu.Lock()
		a.historyCount = len(items)
		a.mu.Unlock()
		a.broadcast(true)
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	})

	mux.HandleFunc("GET /api/history/export.csv", func(w http.ResponseWriter, r *http.Request) {
		expDir := filepath.Join(config.AppDir(), "exports")
		_ = os.MkdirAll(expDir, 0o755)
		expPath := filepath.Join(expDir, fmt.Sprintf("netspeed-history-%s.csv", time.Now().Format("20060102-150405")))
		if f, err := os.Create(expPath); err == nil {
			_ = a.store.ExportCSV(f)
			_ = f.Close()
		}

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="netspeed-history.csv"`)
		if err := a.store.ExportCSV(w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	mux.HandleFunc("GET /api/history/export.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="netspeed-history.json"`)
		if err := a.store.ExportJSON(w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	mux.HandleFunc("GET /api/card", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		unitName := a.cfg.Unit
		hideIP := a.cfg.HideIP
		theme := a.cfg.Theme
		sessionRes := a.lastSessionResult
		a.mu.Unlock()

		var targetRes *engine.Result
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id != "" {
			items, err := a.store.Load()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			for i := range items {
				if items[i].ID == id {
					targetRes = &items[i]
					break
				}
			}
			if targetRes == nil {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "history entry not found"})
				return
			}
		} else {
			if sessionRes == nil {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "run a speed test first or provide ?id="})
				return
			}
			cp := *sessionRes
			targetRes = &cp
		}

		pal := card.DarkPalette()
		if theme == "light" {
			pal = card.LightPalette()
		}

		outPath, err := card.ExportResultCardPNG(*targetRes, unitName, pal, hideIP)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		pngBytes, err := os.ReadFile(outPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		fname := filepath.Base(outPath)
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-NetSpeed-Card-Path", outPath)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fname))
		w.Header().Set("Content-Length", strconv.Itoa(len(pngBytes)))
		_, _ = w.Write(pngBytes)
	})

	return mux
}

func (a *WebApp) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan []byte, 16)
	a.sseMu.Lock()
	a.sseClients[ch] = struct{}{}
	a.sseMu.Unlock()

	defer func() {
		a.sseMu.Lock()
		delete(a.sseClients, ch)
		a.sseMu.Unlock()
	}()

	if initData, err := json.Marshal(a.snapshot()); err == nil {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", initData)
		flusher.Flush()
	}

	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-ch:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case <-keepAlive.C:
			_, _ = w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func runSmokeTest() error {
	if strings.TrimSpace(os.Getenv("NETSPEED_DATA_DIR")) == "" {
		tmpDir, err := os.MkdirTemp("", "netspeed-web-smoke-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmpDir)
		_ = os.Setenv("NETSPEED_DATA_DIR", tmpDir)
	}

	app := newWebApp(":0", config.AppDir(), false)
	defer app.close()

	ts := httptest.NewServer(app.routes())
	defer ts.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	for _, path := range []string{"/", "/api/state", "/api/history"} {
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			return fmt.Errorf("GET %s failed: %w", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET %s returned status %d", path, resp.StatusCode)
		}
	}

	resp, err := client.Post(ts.URL+"/api/config", "application/json", strings.NewReader(`{"theme":"light"}`))
	if err != nil {
		return fmt.Errorf("POST /api/config failed: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST /api/config returned status %d", resp.StatusCode)
	}
	return nil
}

func printListenHints(addr string) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "8080"
	}
	fmt.Printf("NetSpeed Web Server listening on %s\n", addr)
	fmt.Printf("  Local:   http://localhost:%s\n", port)
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if (iface.Flags&net.FlagUp) == 0 || (iface.Flags&net.FlagLoopback) != 0 {
				continue
			}
			addrs, _ := iface.Addrs()
			for _, a := range addrs {
				ipNet, ok := a.(*net.IPNet)
				if !ok || ipNet.IP == nil || ipNet.IP.To4() == nil {
					continue
				}
				fmt.Printf("  LAN:     http://%s:%s\n", ipNet.IP.String(), port)
			}
		}
	}
}

func main() {
	addr := flag.String("addr", envOr("NETSPEED_ADDR", ":8080"), "HTTP listen address (e.g. :8080 or 127.0.0.1:8080)")
	dataDir := flag.String("data-dir", envOr("NETSPEED_DATA_DIR", ""), "Override data directory for config, history, and exported cards")
	smokeTest := flag.Bool("smoke-test", false, "Run local HTTP handler smoke checks and exit")
	flag.Parse()

	if *dataDir != "" {
		_ = os.Setenv("NETSPEED_DATA_DIR", *dataDir)
	}

	if *smokeTest {
		if err := runSmokeTest(); err != nil {
			fmt.Fprintf(os.Stderr, "smoke test failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("netspeed-web smoke test passed.")
		return
	}

	application := newWebApp(*addr, *dataDir, true)
	defer application.close()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           application.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	printListenHints(*addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}
