package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestComputePingAndJitter(t *testing.T) {
	rtts := []time.Duration{
		10 * time.Millisecond,
		14 * time.Millisecond,
		12 * time.Millisecond,
		16 * time.Millisecond,
	}
	minMS, avgMS, jitterMS := ComputePingAndJitter(rtts)
	if minMS != 10 {
		t.Fatalf("expected minMS=10, got %v", minMS)
	}
	if avgMS != 13 {
		t.Fatalf("expected avgMS=13, got %v", avgMS)
	}
	// Diffs: |14-10|=4, |12-14|=2, |16-12|=4 -> avg = 10/3 = 3.33
	if jitterMS != 3.33 {
		t.Fatalf("expected jitterMS=3.33, got %v", jitterMS)
	}
}

func TestComputeTrimmedMeanMbps(t *testing.T) {
	// First 15% of 20 samples = 3 samples discarded (slow start), leaving 17 samples.
	// Remaining 17 samples sorted: lowest 10% (1) and highest 5% (0) discarded -> middle 16 samples.
	samples := []float64{
		5, 10, 20, // slow start ramp-up (3)
		50, 95, 98, 100, 100, 101, 102, 100, 99, 101, 100, 100, 102, 99, 101, 100, 250,
	}
	got := ComputeTrimmedMeanMbps(samples)
	if got < 98 || got > 112 {
		t.Fatalf("expected trimmed mean near 100 Mbps, got %v", got)
	}
}

func TestFormatSpeed(t *testing.T) {
	val, label := FormatSpeed(100, "Mbps")
	if val != 100 || label != "Mbps" {
		t.Fatalf("unexpected Mbps format: %v %s", val, label)
	}
	val, label = FormatSpeed(100, "MB/s")
	if val != 12.5 || label != "MB/s" {
		t.Fatalf("unexpected MB/s format: %v %s", val, label)
	}
	val, label = FormatSpeed(12.5, "Kbps")
	if val != 12500 || label != "Kbps" {
		t.Fatalf("unexpected Kbps format: %v %s", val, label)
	}
}

func TestNormalizeCustomURL(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"speed.cloudflare.com", "https://speed.cloudflare.com", false},
		{"localhost:8080/test", "http://localhost:8080/test", false},
		{"192.168.1.10:8080/upload.php", "http://192.168.1.10:8080/upload.php", false},
		{"https://myhost.example.com/speedtest/upload.php", "https://myhost.example.com/speedtest/upload.php", false},
		{"   ", "", true},
		{"ftp://invalid.example.com", "", true},
	}

	for _, tc := range tests {
		got, err := NormalizeCustomURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeCustomURL(%q) expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeCustomURL(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeCustomURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEngineRunCustomHTTPServer(t *testing.T) {
	chunk := strings.Repeat("A", 64*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/latency.txt"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("test=test\n"))
		case strings.HasPrefix(r.URL.Path, "/download"):
			w.WriteHeader(http.StatusOK)
			for i := 0; i < 64; i++ {
				if _, err := w.Write([]byte(chunk)); err != nil {
					return
				}
			}
		case strings.HasPrefix(r.URL.Path, "/upload.php"):
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("size=ok"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	eng := New()
	var phases []Phase
	res, err := eng.Run(context.Background(), TestConfig{
		Provider:  ProviderCustom,
		CustomURL: srv.URL + "/upload.php",
		Threads:   2,
		Duration:  400 * time.Millisecond,
		Trigger:   "manual",
	}, func(ev ProgressEvent) {
		phases = append(phases, ev.Phase)
	})
	if err != nil {
		t.Fatalf("Engine.Run failed: %v", err)
	}
	if res.DownloadBytes == 0 {
		t.Fatalf("expected non-zero DownloadBytes")
	}
	if res.UploadBytes == 0 {
		t.Fatalf("expected non-zero UploadBytes")
	}
	if res.DownloadMbps <= 0 || res.UploadMbps <= 0 {
		t.Fatalf("expected positive Mbps, got DL=%v UL=%v", res.DownloadMbps, res.UploadMbps)
	}
	if len(phases) == 0 || phases[len(phases)-1] != PhaseDone {
		t.Fatalf("expected final phase PhaseDone, got %v", phases)
	}
}
