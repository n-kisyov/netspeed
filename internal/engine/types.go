package engine

import (
	"context"
	"time"
)

// ProviderType identifies which backend speedtest provider to use.
type ProviderType string

const (
	ProviderAuto       ProviderType = "auto"
	ProviderOokla      ProviderType = "ookla"
	ProviderCloudflare ProviderType = "cloudflare"
	ProviderCustom     ProviderType = "custom"
)

// Phase represents the current stage of a speed test run.
type Phase string

const (
	PhaseIdle     Phase = "idle"
	PhaseInit     Phase = "init"
	PhasePing     Phase = "ping"
	PhaseDownload Phase = "download"
	PhaseUpload   Phase = "upload"
	PhaseDone     Phase = "done"
	PhaseError    Phase = "error"
)

// ClientInfo describes the local connection's public IP, ISP, and coordinates.
type ClientInfo struct {
	IP      string  `json:"ip"`
	ISP     string  `json:"isp"`
	ASN     string  `json:"asn,omitempty"`
	City    string  `json:"city,omitempty"`
	Country string  `json:"country,omitempty"`
	Lat     float64 `json:"lat,omitempty"`
	Lon     float64 `json:"lon,omitempty"`
}

// Server represents a target speed test server endpoint.
type Server struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Sponsor    string       `json:"sponsor"`
	City       string       `json:"city"`
	Country    string       `json:"country"`
	Host       string       `json:"host"`
	URL        string       `json:"url"`
	DistanceKM float64      `json:"distance_km"`
	LatencyMS  float64      `json:"latency_ms"`
	Provider   ProviderType `json:"provider"`
}

// Sample captures a point-in-time throughput measurement.
type Sample struct {
	Timestamp    time.Time `json:"timestamp"`
	Phase        Phase     `json:"phase"`
	Mbps         float64   `json:"mbps"`
	BytesTotal   int64     `json:"bytes_total"`
	Progress     float64   `json:"progress"`
	LoadedPingMS float64   `json:"loaded_ping_ms"`
}

// Result contains the complete metrics of a finished speed test.
type Result struct {
	ID              string       `json:"id"`
	Timestamp       time.Time    `json:"timestamp"`
	Trigger         string       `json:"trigger"` // "manual" or "scheduled"
	Provider        ProviderType `json:"provider"`
	Client          ClientInfo   `json:"client"`
	Server          Server       `json:"server"`
	PingMS          float64      `json:"ping_ms"`
	JitterMS        float64      `json:"jitter_ms"`
	DownloadPingMS  float64      `json:"download_ping_ms"`
	UploadPingMS    float64      `json:"upload_ping_ms"`
	PacketLossPct   float64      `json:"packet_loss_pct"`
	DownloadMbps    float64      `json:"download_mbps"`
	UploadMbps      float64      `json:"upload_mbps"`
	DownloadBytes   int64        `json:"download_bytes"`
	UploadBytes     int64        `json:"upload_bytes"`
	DownloadSamples []float64    `json:"download_samples,omitempty"`
	UploadSamples   []float64    `json:"upload_samples,omitempty"`
	DurationMS      int64        `json:"duration_ms"`
}

// TestConfig configures a single speed test execution.
type TestConfig struct {
	Provider     ProviderType
	ServerID     string
	CustomURL    string
	Threads      int
	Duration     time.Duration
	SkipDownload bool
	SkipUpload   bool
	Trigger      string // "manual" or "scheduled"
}

// ProgressEvent is emitted periodically during a test to drive GUI/CLI updates.
type ProgressEvent struct {
	Phase            Phase
	CurrentMbps      float64
	Progress         float64 // 0.0 .. 1.0 within active phase
	PingMS           float64
	JitterMS         float64
	LoadedPingMS     float64
	DownloadMbps     float64
	UploadMbps       float64
	DownloadPingMS   float64
	UploadPingMS     float64
	BytesTransferred int64
	Client           ClientInfo
	Server           Server
	Message          string
	Samples          []float64
	Result           *Result
	Err              error
}

// ProgressCallback receives real-time updates during Engine.Run.
type ProgressCallback func(ev ProgressEvent)

// Provider defines a pluggable speed test backend (Ookla, Cloudflare, Custom).
type Provider interface {
	Type() ProviderType
	DiscoverClient(ctx context.Context) (ClientInfo, error)
	DiscoverServers(ctx context.Context, client ClientInfo) ([]Server, error)
	ProbeLatency(ctx context.Context, server Server) (time.Duration, error)
	DownloadURL(server Server) string
	UploadURL(server Server) string
	FallbackDownloadURL(server Server) string
}
