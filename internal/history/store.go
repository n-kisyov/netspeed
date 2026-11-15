package history

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"netspeed/internal/config"
	"netspeed/internal/engine"
)

const maxHistoryEntries = 500

// Store persists speed test results in JSON format shared between GUI and CLI.
type Store struct {
	path string
	mu   sync.Mutex
}

// DefaultStore opens the default history store in %AppData%\NetSpeed\history.json.
func DefaultStore() *Store {
	return NewStore(filepath.Join(config.AppDir(), "history.json"))
}

// NewStore opens a history store at an explicit file path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Path returns the backing history file path.
func (s *Store) Path() string {
	return s.path
}

// Load reads all stored test results, ordered newest-first.
func (s *Store) Load() ([]engine.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() ([]engine.Result, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var items []engine.Result
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// Append prepends a new result to history, caps at 500 items, and saves atomically.
func (s *Store) Append(res engine.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	items, _ := s.loadLocked()

	// Compact high-frequency samples before persisting to keep history.json lightweight.
	compact := res
	compact.DownloadSamples = downsample(res.DownloadSamples, 60)
	compact.UploadSamples = downsample(res.UploadSamples, 60)

	updated := make([]engine.Result, 0, len(items)+1)
	updated = append(updated, compact)
	updated = append(updated, items...)
	if len(updated) > maxHistoryEntries {
		updated = updated[:maxHistoryEntries]
	}

	return s.saveLocked(updated)
}

// DeleteByID removes the entry with matching ID and persists the updated history.
func (s *Store) DeleteByID(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.loadLocked()
	if err != nil {
		return err
	}
	filtered := make([]engine.Result, 0, len(items))
	for _, r := range items {
		if r.ID != id {
			filtered = append(filtered, r)
		}
	}
	return s.saveLocked(filtered)
}

// Clear removes all saved history entries.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked([]engine.Result{})
}

func (s *Store) saveLocked(items []engine.Result) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".hist-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		_ = os.Remove(s.path)
		if err2 := os.Rename(tmpName, s.path); err2 != nil {
			_ = os.Remove(tmpName)
			return err2
		}
	}
	return nil
}

// ExportCSV writes all history items in CSV format to w.
func (s *Store) ExportCSV(w io.Writer) error {
	items, err := s.Load()
	if err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	header := []string{
		"timestamp", "trigger", "provider", "server_name", "server_city", "server_country",
		"ip", "isp", "ping_ms", "jitter_ms", "packet_loss_pct", "dl_ping_ms", "ul_ping_ms", "bufferbloat_grade",
		"download_mbps", "upload_mbps", "download_bytes", "upload_bytes", "duration_ms",
	}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, r := range items {
		row := []string{
			r.Timestamp.Format(time.RFC3339),
			r.Trigger,
			string(r.Provider),
			r.Server.Name,
			r.Server.City,
			r.Server.Country,
			r.Client.IP,
			r.Client.ISP,
			fmt.Sprintf("%.2f", r.PingMS),
			fmt.Sprintf("%.2f", r.JitterMS),
			fmt.Sprintf("%.2f", r.PacketLossPct),
			fmt.Sprintf("%.2f", r.DownloadPingMS),
			fmt.Sprintf("%.2f", r.UploadPingMS),
			r.BufferbloatGrade,
			fmt.Sprintf("%.2f", r.DownloadMbps),
			fmt.Sprintf("%.2f", r.UploadMbps),
			fmt.Sprintf("%d", r.DownloadBytes),
			fmt.Sprintf("%d", r.UploadBytes),
			fmt.Sprintf("%d", r.DurationMS),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// ExportJSON writes all history items in indented JSON format to w.
func (s *Store) ExportJSON(w io.Writer) error {
	items, err := s.Load()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(items)
}

func downsample(in []float64, maxPoints int) []float64 {
	if len(in) <= maxPoints || maxPoints <= 0 {
		cp := make([]float64, len(in))
		copy(cp, in)
		return cp
	}
	out := make([]float64, 0, maxPoints)
	step := float64(len(in)-1) / float64(maxPoints-1)
	for i := 0; i < maxPoints; i++ {
		idx := int(float64(i)*step + 0.5)
		if idx >= len(in) {
			idx = len(in) - 1
		}
		out = append(out, in[idx])
	}
	return out
}
