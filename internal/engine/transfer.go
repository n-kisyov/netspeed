package engine

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"
)

const desktopUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

func newSpeedHTTPClient(requestTimeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   8 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
	}
}

func newRequest(ctx context.Context, method, rawURL string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", desktopUserAgent)
	req.Header.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	req.Header.Set("Pragma", "no-cache")
	return req, nil
}

// measureHTTPRTT measures HTTP round-trip latency on a connection (excluding DNS/TLS when reused).
func measureHTTPRTT(ctx context.Context, client *http.Client, targetURL string) (time.Duration, error) {
	var wroteAt time.Time
	var firstByteAt time.Time
	var mu sync.Mutex

	trace := &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			mu.Lock()
			wroteAt = time.Now()
			mu.Unlock()
		},
		GotFirstResponseByte: func() {
			mu.Lock()
			firstByteAt = time.Now()
			mu.Unlock()
		},
	}

	reqCtx := httptrace.WithClientTrace(ctx, trace)
	req, err := newRequest(reqCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, err
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()

	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("HTTP %d from %s", resp.StatusCode, targetURL)
	}

	mu.Lock()
	defer mu.Unlock()
	if !wroteAt.IsZero() && !firstByteAt.IsZero() && firstByteAt.After(wroteAt) {
		return firstByteAt.Sub(wroteAt), nil
	}
	return time.Since(start), nil
}

// payloadStream streams pseudo-random bytes up to limit without allocating large buffers,
// reporting transferred bytes to BandwidthMeter as HTTP writes consume chunks.
type payloadStream struct {
	remaining int64
	pattern   []byte
	offset    int
	meter     *BandwidthMeter
}

func newPayloadStream(totalBytes int64, meter *BandwidthMeter) *payloadStream {
	// Pre-populate a non-compressible 64KB pattern buffer.
	const patLen = 64 * 1024
	pat := make([]byte, patLen)
	var x uint32 = 0x12345678
	for i := range pat {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		pat[i] = byte(x)
	}
	return &payloadStream{
		remaining: totalBytes,
		pattern:   pat,
		meter:     meter,
	}
}

func (p *payloadStream) Read(dst []byte) (int, error) {
	if p.remaining <= 0 {
		return 0, io.EOF
	}
	toRead := len(dst)
	if int64(toRead) > p.remaining {
		toRead = int(p.remaining)
	}
	copied := 0
	for copied < toRead {
		n := copy(dst[copied:toRead], p.pattern[p.offset:])
		copied += n
		p.offset = (p.offset + n) % len(p.pattern)
	}
	p.remaining -= int64(copied)
	if p.meter != nil {
		p.meter.AddBytes(int64(copied))
	}
	return copied, nil
}

// runPingPhase performs sequential latency probes (after 1 warmup probe for TLS/keep-alive).
func runPingPhase(ctx context.Context, p Provider, server Server, probes int) (minMS, jitterMS float64, err error) {
	if probes <= 0 {
		probes = 12
	}
	// Warmup probe to establish TLS/TCP keep-alive connection.
	_, _ = p.ProbeLatency(ctx, server)

	rtts := make([]time.Duration, 0, probes)
	var lastErr error
	for i := 0; i < probes; i++ {
		if ctx.Err() != nil {
			return 0, 0, ctx.Err()
		}
		d, err := p.ProbeLatency(ctx, server)
		if err != nil {
			lastErr = err
			continue
		}
		rtts = append(rtts, d)
	}
	if len(rtts) == 0 {
		if lastErr != nil {
			return 0, 0, lastErr
		}
		return 0, 0, errors.New("all latency probes failed")
	}
	minMS, _, jitterMS = ComputePingAndJitter(rtts)
	return minMS, jitterMS, nil
}

type transferOutcome struct {
	Mbps         float64
	Bytes        int64
	LoadedPingMS float64
	Samples      []float64
}

// runTransferPhase executes concurrent download or upload HTTP workers for cfg.Duration,
// runs loaded latency probes concurrently, and emits 50ms ProgressEvents.
func runTransferPhase(
	parentCtx context.Context,
	client *http.Client,
	p Provider,
	server Server,
	phase Phase,
	threads int,
	duration time.Duration,
	baseProgress ProgressEvent,
	cb ProgressCallback,
) (transferOutcome, error) {
	if threads < 1 {
		threads = 1
	}
	if threads > 32 {
		threads = 32
	}
	if duration <= 0 {
		duration = 10 * time.Second
	}

	phaseCtx, cancel := context.WithTimeout(parentCtx, duration)
	defer cancel()

	meter := NewBandwidthMeter()

	var loadedMu sync.Mutex
	var loadedSumMS float64
	var loadedCount int
	var lastLoadedMS atomic.Int64 // stored as microseconds

	// Background loaded latency probe (bufferbloat indicator).
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		ticker := time.NewTicker(450 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-phaseCtx.Done():
				return
			case <-ticker.C:
				probeCtx, pCancel := context.WithTimeout(phaseCtx, 2*time.Second)
				d, err := p.ProbeLatency(probeCtx, server)
				pCancel()
				if err == nil && d > 0 {
					ms := float64(d) / float64(time.Millisecond)
					lastLoadedMS.Store(d.Microseconds())
					loadedMu.Lock()
					loadedSumMS += ms
					loadedCount++
					loadedMu.Unlock()
				}
			}
		}
	}()

	var wg sync.WaitGroup
	var errMu sync.Mutex
	var lastErr error
	var useFallback atomic.Bool

	dlURL := p.DownloadURL(server)
	fallbackDL := p.FallbackDownloadURL(server)
	ulURL := p.UploadURL(server)

	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 64*1024)
			for phaseCtx.Err() == nil {
				var err error
				if phase == PhaseDownload {
					target := dlURL
					if useFallback.Load() && fallbackDL != "" {
						target = fallbackDL
					}
					err = streamDownloadOnce(phaseCtx, client, target, buf, meter)
					if err != nil && !useFallback.Load() && fallbackDL != "" && phaseCtx.Err() == nil {
						useFallback.Store(true)
						err = streamDownloadOnce(phaseCtx, client, fallbackDL, buf, meter)
					}
				} else {
					err = streamUploadOnce(phaseCtx, client, ulURL, 16*1024*1024, meter)
				}
				if err != nil && phaseCtx.Err() == nil {
					errMu.Lock()
					lastErr = err
					errMu.Unlock()
					// Brief backoff to avoid tight spin on transient error.
					select {
					case <-phaseCtx.Done():
						return
					case <-time.After(150 * time.Millisecond):
					}
				}
			}
		}()
	}

	// Emit smooth UI progress ticks while transfer runs.
	start := time.Now()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-phaseCtx.Done():
			break loop
		case <-ticker.C:
			liveMbps, totalBytes, samples := meter.Tick()
			elapsed := time.Since(start)
			progress := float64(elapsed) / float64(duration)
			if progress > 1.0 {
				progress = 1.0
			}
			loadedMS := Round2(float64(lastLoadedMS.Load()) / 1000.0)
			if cb != nil {
				ev := baseProgress
				ev.Phase = phase
				ev.CurrentMbps = liveMbps
				ev.Progress = progress
				ev.LoadedPingMS = loadedMS
				ev.BytesTransferred = totalBytes
				ev.Samples = samples
				if phase == PhaseDownload {
					ev.DownloadMbps = liveMbps
					ev.DownloadPingMS = loadedMS
				} else {
					ev.UploadMbps = liveMbps
					ev.UploadPingMS = loadedMS
				}
				cb(ev)
			}
		}
	}

	wg.Wait()
	<-probeDone

	if parentCtx.Err() != nil {
		return transferOutcome{}, parentCtx.Err()
	}

	finalMbps, totalBytes, samples := meter.Finalize()
	if totalBytes == 0 && lastErr != nil {
		return transferOutcome{}, lastErr
	}

	loadedMu.Lock()
	avgLoadedMS := 0.0
	if loadedCount > 0 {
		avgLoadedMS = Round2(loadedSumMS / float64(loadedCount))
	}
	loadedMu.Unlock()

	return transferOutcome{
		Mbps:         finalMbps,
		Bytes:        totalBytes,
		LoadedPingMS: avgLoadedMS,
		Samples:      samples,
	}, nil
}

func streamDownloadOnce(ctx context.Context, client *http.Client, targetURL string, buf []byte, meter *BandwidthMeter) error {
	req, err := newRequest(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("download HTTP %d", resp.StatusCode)
	}

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			meter.AddBytes(int64(n))
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func streamUploadOnce(ctx context.Context, client *http.Client, targetURL string, bodyBytes int64, meter *BandwidthMeter) error {
	stream := newPayloadStream(bodyBytes, meter)
	req, err := newRequest(ctx, http.MethodPost, targetURL, stream)
	if err != nil {
		return err
	}
	req.ContentLength = bodyBytes
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("upload HTTP %d", resp.StatusCode)
	}
	return nil
}
