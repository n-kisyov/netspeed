package main

import (
	"bytes"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"netspeed/internal/engine"
)

func TestWebAppStateConfigHistoryAndCardEndpoints(t *testing.T) {
	t.Setenv("NETSPEED_DATA_DIR", t.TempDir())

	app := newWebApp(":0", "", false)
	defer app.close()
	handler := app.routes()

	// 1. Initial GET / renders embedded HTML.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status=%d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "NetSpeed Web Dashboard") {
		t.Fatalf("GET / missing expected title")
	}

	// 2. Initial GET /api/state has nil last_session_result.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/state status=%d, want 200", rec.Code)
	}
	var snap stateSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode /api/state failed: %v", err)
	}
	if snap.LastSessionResult != nil {
		t.Fatalf("expected nil LastSessionResult at startup")
	}

	// 3. Session-only PNG card guard returns 409 before any test has run.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/card", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("GET /api/card without session result status=%d, want 409", rec.Code)
	}

	// 4. POST /api/config updates theme, unit, schedule, and custom URL.
	patchBody := `{"theme":"light","unit":"MB/s","schedule_enabled":true,"schedule_interval_minutes":15,"custom_url":"https://speed.cloudflare.com/__down?bytes=50000000","apply_custom_url":true}`
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(patchBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/config status=%d, body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode updated state: %v", err)
	}
	if snap.Config.Theme != "light" || snap.Config.Unit != "MB/s" || !snap.Config.ScheduleEnabled || snap.Config.ScheduleIntervalMinutes != 15 {
		t.Fatalf("unexpected updated config: %+v", snap.Config)
	}
	if snap.Config.Provider != "custom" || len(snap.Config.SavedCustomURLs) == 0 {
		t.Fatalf("expected custom provider + saved URL: %+v", snap.Config)
	}
	if snap.NextRunUnixMS == 0 {
		t.Fatalf("expected non-zero next_run_unix_ms when schedule enabled")
	}

	// 5. Invalid custom URL returns 400 and surfaces error.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(`{"custom_url":"","apply_custom_url":true}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid custom URL status=%d, want 400", rec.Code)
	}

	// 6. Persist a history entry, test /api/history, /api/card?id=..., CSV export, and DELETE.
	sampleRes := engine.Result{
		ID:               "web-res-1",
		Timestamp:        time.Now(),
		Trigger:          "manual",
		Provider:         engine.ProviderCloudflare,
		Client:           engine.ClientInfo{IP: "203.0.113.10", ISP: "PiFiber"},
		Server:           engine.Server{Name: "Cloudflare Edge — Sofia", DistanceKM: 10},
		PingMS:           9.4,
		JitterMS:         1.1,
		PacketLossPct:    0.0,
		BufferbloatGrade: "A+",
		DownloadPingMS:   12.0,
		UploadPingMS:     13.5,
		DownloadMbps:     420.5,
		UploadMbps:       180.2,
		DownloadBytes:    250 * 1024 * 1024,
		UploadBytes:      110 * 1024 * 1024,
		DownloadSamples:  []float64{100, 320, 415, 422},
		UploadSamples:    []float64{60, 140, 178, 182},
		DurationMS:       10000,
	}
	if err := app.store.Append(sampleRes); err != nil {
		t.Fatalf("store.Append failed: %v", err)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/history", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/history status=%d", rec.Code)
	}
	var histResp struct {
		Items []engine.Result `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &histResp); err != nil || len(histResp.Items) != 1 {
		t.Fatalf("unexpected /api/history response: err=%v len=%d", err, len(histResp.Items))
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/card?id=web-res-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/card?id=web-res-1 status=%d, body=%s", rec.Code, rec.Body.String())
	}
	cfgPNG, err := png.DecodeConfig(bytes.NewReader(rec.Body.Bytes()))
	if err != nil || cfgPNG.Width != 840 || cfgPNG.Height != 460 {
		t.Fatalf("invalid card PNG response: err=%v cfg=%+v", err, cfgPNG)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/history/export.csv", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "bufferbloat_grade") {
		t.Fatalf("unexpected CSV export response: code=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/history?id=web-res-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/history status=%d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &histResp); err != nil || len(histResp.Items) != 0 {
		t.Fatalf("expected 0 items after delete, got %d", len(histResp.Items))
	}
}
