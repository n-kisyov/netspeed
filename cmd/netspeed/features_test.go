package main

import (
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"netspeed/internal/engine"
)

func TestNewAppStartsWithoutSessionResult(t *testing.T) {
	t.Setenv("AppData", t.TempDir())
	a := newApp(false)
	defer a.sched.Stop()
	defer a.tray.Close()

	if a.lastSessionResult != nil {
		t.Fatalf("expected lastSessionResult to be nil at startup")
	}
	if _, ok := a.buildClipboardSummary(); ok {
		t.Fatalf("expected buildClipboardSummary to return ok=false before a test runs")
	}
}

func TestFormatResultSummaryAndPNGExport(t *testing.T) {
	t.Setenv("AppData", t.TempDir())

	res := engine.Result{
		ID:               "test-1",
		Timestamp:        time.Now(),
		Trigger:          "manual",
		Provider:         engine.ProviderCloudflare,
		Client:           engine.ClientInfo{IP: "203.0.113.5", ISP: "TestFiber"},
		Server:           engine.Server{Name: "Cloudflare Edge — Test (TST)", DistanceKM: 12},
		PingMS:           11.2,
		JitterMS:         1.4,
		PacketLossPct:    0.0,
		BufferbloatGrade: "A+",
		DownloadPingMS:   14.1,
		UploadPingMS:     15.0,
		DownloadMbps:     512.4,
		UploadMbps:       245.8,
		DownloadBytes:    320 * 1024 * 1024,
		UploadBytes:      140 * 1024 * 1024,
		DownloadSamples:  []float64{120, 340, 480, 515, 510, 512},
		UploadSamples:    []float64{80, 190, 235, 248, 245},
		DurationMS:       10400,
	}

	txt := formatResultSummary(res, "Mbps")
	if !strings.Contains(txt, "\r\n") {
		t.Fatalf("expected Windows CRLF line endings in clipboard summary")
	}
	if !strings.Contains(txt, "Bufferbloat Grade: A+") || !strings.Contains(txt, "Loss: 0.0%") {
		t.Fatalf("unexpected summary formatting:\n%s", txt)
	}

	outPath, err := ExportResultCardPNG(res, "Mbps", DarkPalette(), true)
	if err != nil {
		t.Fatalf("ExportResultCardPNG failed: %v", err)
	}
	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("failed opening PNG output %s: %v", outPath, err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatalf("invalid PNG output: %v", err)
	}
	if cfg.Width != 840 || cfg.Height != 460 {
		t.Fatalf("unexpected PNG dimensions: %dx%d", cfg.Width, cfg.Height)
	}
}
