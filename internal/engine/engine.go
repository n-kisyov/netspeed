package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// ErrTestInProgress is returned when a speed test is already running on the Engine.
var ErrTestInProgress = errors.New("a speed test is already in progress")

// Engine coordinates provider discovery, latency probing, download/upload execution, and metrics aggregation.
type Engine struct {
	httpClient *http.Client
	running    atomic.Bool
}

// New creates a new Engine with tuned HTTP connection pooling.
func New() *Engine {
	return &Engine{
		httpClient: newSpeedHTTPClient(15 * time.Second),
	}
}

// IsRunning reports whether a speed test is currently active.
func (e *Engine) IsRunning() bool {
	return e.running.Load()
}

// ListServers discovers available servers for the given provider.
// For ProviderAuto, both Ookla and Cloudflare servers are gathered concurrently.
func (e *Engine) ListServers(ctx context.Context, provider ProviderType) (ClientInfo, []Server, error) {
	switch provider {
	case ProviderOokla:
		p := NewOoklaProvider(e.httpClient)
		client, err := p.DiscoverClient(ctx)
		if err != nil {
			client = ClientInfo{}
		}
		srvs, err := p.DiscoverServers(ctx, client)
		return client, srvs, err
	case ProviderCloudflare:
		p := NewCloudflareProvider(e.httpClient)
		client, _ := p.DiscoverClient(ctx)
		srvs, err := p.DiscoverServers(ctx, client)
		return client, srvs, err
	default:
		// Auto: query Ookla and Cloudflare concurrently.
		ookla := NewOoklaProvider(e.httpClient)
		cf := NewCloudflareProvider(e.httpClient)

		var wg sync.WaitGroup
		var ooklaClient, cfClient ClientInfo
		var ooklaSrvs, cfSrvs []Server
		var ooklaErr, cfErr error

		wg.Add(2)
		go func() {
			defer wg.Done()
			cCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			ooklaClient, _ = ookla.DiscoverClient(cCtx)
			ooklaSrvs, ooklaErr = ookla.DiscoverServers(cCtx, ooklaClient)
		}()
		go func() {
			defer wg.Done()
			cCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			cfClient, _ = cf.DiscoverClient(cCtx)
			cfSrvs, cfErr = cf.DiscoverServers(cCtx, cfClient)
		}()
		wg.Wait()

		client := ooklaClient
		if client.IP == "" {
			client = cfClient
		}
		if client.ISP == "" && cfClient.ISP != "" {
			client.ISP = cfClient.ISP
		}

		combined := make([]Server, 0, len(ooklaSrvs)+len(cfSrvs))
		combined = append(combined, ooklaSrvs...)
		combined = append(combined, cfSrvs...)

		if len(combined) == 0 {
			if ooklaErr != nil {
				return client, nil, ooklaErr
			}
			return client, nil, cfErr
		}
		return client, combined, nil
	}
}

func emit(cb ProgressCallback, ev ProgressEvent) {
	if cb != nil {
		cb(ev)
	}
}

// Run executes a full speed test (Discovery -> Ping/Jitter -> Download -> Upload) and returns the final Result.
func (e *Engine) Run(ctx context.Context, cfg TestConfig, cb ProgressCallback) (Result, error) {
	if !e.running.CompareAndSwap(false, true) {
		return Result{}, ErrTestInProgress
	}
	defer e.running.Store(false)

	startWall := time.Now()
	if cfg.Threads <= 0 {
		cfg.Threads = 8
	}
	if cfg.Duration <= 0 {
		cfg.Duration = 10 * time.Second
	}
	if cfg.Trigger == "" {
		cfg.Trigger = "manual"
	}
	if cfg.Provider == "" {
		cfg.Provider = ProviderAuto
	}

	base := ProgressEvent{
		Phase:   PhaseInit,
		Message: "Discovering client and optimal server...",
	}
	emit(cb, base)

	provider, clientInfo, targetServer, fallbackMsg, err := e.resolveTarget(ctx, cfg)
	if err != nil {
		base.Phase = PhaseError
		base.Err = err
		base.Message = err.Error()
		emit(cb, base)
		return Result{}, err
	}

	base.Client = clientInfo
	base.Server = targetServer
	if fallbackMsg != "" {
		base.Message = fallbackMsg
	} else {
		base.Message = fmt.Sprintf("Connected to %s", targetServer.Name)
	}

	// Phase 1: Idle Ping & Jitter
	base.Phase = PhasePing
	base.Progress = 0
	emit(cb, base)

	pingMS, jitterMS, err := runPingPhase(ctx, provider, targetServer, 12)
	if err != nil {
		// In auto mode, if Ookla server fails ping mid-flight, try falling back to Cloudflare.
		if cfg.Provider == ProviderAuto && provider.Type() != ProviderCloudflare && cfg.ServerID == "" {
			cf := NewCloudflareProvider(e.httpClient)
			if cfSrvs, cfErr := cf.DiscoverServers(ctx, clientInfo); cfErr == nil && len(cfSrvs) > 0 {
				provider = cf
				targetServer = cfSrvs[0]
				base.Server = targetServer
				base.Message = "Switched to Cloudflare Edge after primary server timeout"
				pingMS, jitterMS, err = runPingPhase(ctx, provider, targetServer, 12)
			}
		}
	}
	if err != nil {
		base.Phase = PhaseError
		base.Err = err
		base.Message = err.Error()
		emit(cb, base)
		return Result{}, err
	}

	targetServer.LatencyMS = pingMS
	base.Server = targetServer
	base.PingMS = pingMS
	base.JitterMS = jitterMS
	base.Progress = 1
	base.Message = fmt.Sprintf("Ping %.2f ms · Jitter %.2f ms", pingMS, jitterMS)
	emit(cb, base)

	var dlOut, ulOut transferOutcome

	// Phase 2: Download
	if !cfg.SkipDownload {
		base.Message = "Testing download speed..."
		dlOut, err = runTransferPhase(ctx, e.httpClient, provider, targetServer, PhaseDownload, cfg.Threads, cfg.Duration, base, cb)
		if err != nil {
			base.Phase = PhaseError
			base.Err = err
			base.Message = err.Error()
			emit(cb, base)
			return Result{}, err
		}
		base.DownloadMbps = dlOut.Mbps
		base.DownloadPingMS = dlOut.LoadedPingMS
	}

	// Phase 3: Upload
	if !cfg.SkipUpload {
		base.Message = "Testing upload speed..."
		ulOut, err = runTransferPhase(ctx, e.httpClient, provider, targetServer, PhaseUpload, cfg.Threads, cfg.Duration, base, cb)
		if err != nil {
			base.Phase = PhaseError
			base.Err = err
			base.Message = err.Error()
			emit(cb, base)
			return Result{}, err
		}
		base.UploadMbps = ulOut.Mbps
		base.UploadPingMS = ulOut.LoadedPingMS
	}

	res := Result{
		ID:              fmt.Sprintf("%d", time.Now().UnixNano()),
		Timestamp:       time.Now(),
		Trigger:         cfg.Trigger,
		Provider:        provider.Type(),
		Client:          clientInfo,
		Server:          targetServer,
		PingMS:          pingMS,
		JitterMS:        jitterMS,
		DownloadPingMS:  dlOut.LoadedPingMS,
		UploadPingMS:    ulOut.LoadedPingMS,
		DownloadMbps:    dlOut.Mbps,
		UploadMbps:      ulOut.Mbps,
		DownloadBytes:   dlOut.Bytes,
		UploadBytes:     ulOut.Bytes,
		DownloadSamples: dlOut.Samples,
		UploadSamples:   ulOut.Samples,
		DurationMS:      time.Since(startWall).Milliseconds(),
	}

	doneEv := base
	doneEv.Phase = PhaseDone
	doneEv.Progress = 1
	doneEv.CurrentMbps = 0
	doneEv.Result = &res
	doneEv.Message = "Speed test complete"
	emit(cb, doneEv)

	return res, nil
}

func (e *Engine) resolveTarget(ctx context.Context, cfg TestConfig) (Provider, ClientInfo, Server, string, error) {
	if cfg.CustomURL != "" || cfg.Provider == ProviderCustom {
		cp, err := NewCustomProvider(e.httpClient, cfg.CustomURL)
		if err != nil {
			return nil, ClientInfo{}, Server{}, "", err
		}
		clientInfo, _ := cp.DiscoverClient(ctx)
		srvs, err := cp.DiscoverServers(ctx, clientInfo)
		if err != nil {
			return nil, ClientInfo{}, Server{}, "", err
		}
		return cp, clientInfo, srvs[0], "", nil
	}

	if cfg.Provider == ProviderCloudflare || cfg.ServerID == "cloudflare-edge" {
		cf := NewCloudflareProvider(e.httpClient)
		clientInfo, _ := cf.DiscoverClient(ctx)
		srvs, err := cf.DiscoverServers(ctx, clientInfo)
		if err != nil {
			return nil, ClientInfo{}, Server{}, "", err
		}
		return cf, clientInfo, srvs[0], "", nil
	}

	// Ookla or Auto
	ookla := NewOoklaProvider(e.httpClient)
	clientInfo, clientErr := ookla.DiscoverClient(ctx)
	srvs, srvErr := ookla.DiscoverServers(ctx, clientInfo)

	if srvErr == nil && len(srvs) > 0 {
		selected := srvs[0]
		if cfg.ServerID != "" {
			found := false
			for _, s := range srvs {
				if s.ID == cfg.ServerID {
					selected = s
					found = true
					break
				}
			}
			if !found {
				return nil, ClientInfo{}, Server{}, "", fmt.Errorf("server ID %q not found in available server list", cfg.ServerID)
			}
		} else {
			// Pick the first responsive server (lowest latency).
			for _, s := range srvs {
				if s.LatencyMS > 0 && s.LatencyMS < 9000 {
					selected = s
					break
				}
			}
		}
		return ookla, clientInfo, selected, "", nil
	}

	if cfg.Provider == ProviderOokla {
		if srvErr != nil {
			return nil, ClientInfo{}, Server{}, "", srvErr
		}
		return nil, ClientInfo{}, Server{}, "", clientErr
	}

	// ProviderAuto fallback to Cloudflare.
	cf := NewCloudflareProvider(e.httpClient)
	cfClient, _ := cf.DiscoverClient(ctx)
	if clientInfo.IP == "" {
		clientInfo = cfClient
	}
	cfSrvs, cfErr := cf.DiscoverServers(ctx, clientInfo)
	if cfErr != nil {
		return nil, ClientInfo{}, Server{}, "", fmt.Errorf("ookla failed (%v) and cloudflare fallback failed (%v)", srvErr, cfErr)
	}
	return cf, clientInfo, cfSrvs[0], "Ookla unavailable; using Cloudflare Edge", nil
}
