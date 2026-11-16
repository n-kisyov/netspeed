package card

import (
	"bytes"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"netspeed/internal/engine"
)

func TestRenderAndExportResultCardPNG(t *testing.T) {
	t.Setenv("NETSPEED_DATA_DIR", t.TempDir())

	res := engine.Result{
		ID:               "card-test-1",
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

	txtCRLF := FormatResultSummary(res, "Mbps", true)
	if !strings.Contains(txtCRLF, "\r\n") {
		t.Fatalf("expected CRLF line endings when crlf=true")
	}
	txtLF := FormatResultSummary(res, "Mbps", false)
	if strings.Contains(txtLF, "\r\n") {
		t.Fatalf("expected LF-only line endings when crlf=false")
	}
	if !strings.Contains(txtLF, "Bufferbloat Grade: A+") || !strings.Contains(txtLF, "Loss: 0.0%") {
		t.Fatalf("unexpected summary formatting:\n%s", txtLF)
	}

	raw, err := RenderPNG(res, "Mbps", LightPalette(), false)
	if err != nil {
		t.Fatalf("RenderPNG light failed: %v", err)
	}
	cfgLight, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("invalid light PNG bytes: %v", err)
	}
	if cfgLight.Width != 840 || cfgLight.Height != 460 {
		t.Fatalf("unexpected PNG dimensions: %dx%d", cfgLight.Width, cfgLight.Height)
	}

	outPath, err := ExportResultCardPNG(res, "Mbps", DarkPalette(), true)
	if err != nil {
		t.Fatalf("ExportResultCardPNG dark failed: %v", err)
	}
	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("failed opening PNG output %s: %v", outPath, err)
	}
	defer f.Close()
	cfgDark, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatalf("invalid dark PNG file: %v", err)
	}
	if cfgDark.Width != 840 || cfgDark.Height != 460 {
		t.Fatalf("unexpected exported PNG dimensions: %dx%d", cfgDark.Width, cfgDark.Height)
	}
}
