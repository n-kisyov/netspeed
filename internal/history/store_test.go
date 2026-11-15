package history

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netspeed/internal/engine"
)

func TestHistoryStoreCRUDAndExport(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "history.json"))

	// Empty initial load should return nil slice, nil err.
	items, err := store.Load()
	if err != nil {
		t.Fatalf("Load empty failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}

	samples := make([]float64, 150)
	for i := range samples {
		samples[i] = float64(i)
	}

	r1 := engine.Result{
		ID:              "1",
		Timestamp:       time.Now().Add(-time.Minute),
		Trigger:         "manual",
		Provider:        engine.ProviderOokla,
		Server:          engine.Server{Name: "TestISP (City)", City: "City", Country: "US"},
		PingMS:          12.4,
		JitterMS:        1.2,
		DownloadMbps:    350.5,
		UploadMbps:      120.2,
		DownloadSamples: samples,
	}
	r2 := engine.Result{
		ID:           "2",
		Timestamp:    time.Now(),
		Trigger:      "scheduled",
		Provider:     engine.ProviderCloudflare,
		Server:       engine.Server{Name: "Cloudflare Edge", City: "Edge", Country: "Global"},
		PingMS:       9.1,
		JitterMS:     0.8,
		DownloadMbps: 410.0,
		UploadMbps:   145.6,
	}

	if err := store.Append(r1); err != nil {
		t.Fatalf("Append r1 failed: %v", err)
	}
	if err := store.Append(r2); err != nil {
		t.Fatalf("Append r2 failed: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(loaded))
	}
	// Newest entry first.
	if loaded[0].ID != "2" || loaded[1].ID != "1" {
		t.Fatalf("unexpected ordering: %s, %s", loaded[0].ID, loaded[1].ID)
	}
	if len(loaded[1].DownloadSamples) != 60 {
		t.Fatalf("expected downsampled 60 points, got %d", len(loaded[1].DownloadSamples))
	}

	var csvBuf bytes.Buffer
	if err := store.ExportCSV(&csvBuf); err != nil {
		t.Fatalf("ExportCSV failed: %v", err)
	}
	csvStr := csvBuf.String()
	if !strings.Contains(csvStr, "timestamp,trigger,provider") || !strings.Contains(csvStr, "410.00") {
		t.Fatalf("unexpected CSV output:\n%s", csvStr)
	}

	var jsonBuf bytes.Buffer
	if err := store.ExportJSON(&jsonBuf); err != nil {
		t.Fatalf("ExportJSON failed: %v", err)
	}
	if !strings.Contains(jsonBuf.String(), `"trigger": "scheduled"`) {
		t.Fatalf("unexpected JSON output:\n%s", jsonBuf.String())
	}

	if err := store.DeleteByID("2"); err != nil {
		t.Fatalf("DeleteByID failed: %v", err)
	}
	afterDel, err := store.Load()
	if err != nil {
		t.Fatalf("Load after DeleteByID failed: %v", err)
	}
	if len(afterDel) != 1 || afterDel[0].ID != "1" {
		t.Fatalf("expected only entry '1' remaining after DeleteByID, got %+v", afterDel)
	}

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}
	afterClear, err := store.Load()
	if err != nil {
		t.Fatalf("Load after clear failed: %v", err)
	}
	if len(afterClear) != 0 {
		t.Fatalf("expected 0 items after clear, got %d", len(afterClear))
	}
}
