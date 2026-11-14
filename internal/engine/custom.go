package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type customEndpointMode int

const (
	customModeGeneric customEndpointMode = iota
	customModeOokla
	customModeCloudflare
)

// CustomProvider runs speed tests against a user-supplied HTTP/HTTPS URL.
// It auto-detects Cloudflare endpoints, custom Ookla server endpoints (/upload.php or /latency.txt),
// and generic HTTP download/upload endpoints.
type CustomProvider struct {
	client  *http.Client
	mu      sync.RWMutex
	rawURL  string
	normURL string
	baseURL string
	mode    customEndpointMode
	ulURL   string
}

func NewCustomProvider(client *http.Client, rawURL string) (*CustomProvider, error) {
	norm, err := NormalizeCustomURL(rawURL)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(norm)
	base := strings.TrimRight(fmt.Sprintf("%s://%s", u.Scheme, u.Host), "/")
	if idx := strings.LastIndex(u.Path, "/"); idx > 0 {
		base += u.Path[:idx]
	}

	mode := customModeGeneric
	if strings.EqualFold(u.Hostname(), "speed.cloudflare.com") {
		mode = customModeCloudflare
	} else if strings.HasSuffix(strings.ToLower(u.Path), "/upload.php") {
		mode = customModeOokla
	}

	return &CustomProvider{
		client:  client,
		rawURL:  rawURL,
		normURL: norm,
		baseURL: strings.TrimRight(base, "/"),
		mode:    mode,
		ulURL:   norm,
	}, nil
}

// NormalizeCustomURL validates and normalizes user-entered server URLs.
func NormalizeCustomURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("custom server URL cannot be empty")
	}
	if !strings.Contains(raw, "://") {
		scheme := "https://"
		hostPart := strings.SplitN(raw, "/", 2)[0]
		hostOnly := hostPart
		if h, _, err := net.SplitHostPort(hostPart); err == nil {
			hostOnly = h
		}
		if strings.EqualFold(hostOnly, "localhost") || net.ParseIP(hostOnly) != nil {
			scheme = "http://"
		}
		raw = scheme + raw
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return "", fmt.Errorf("invalid custom server URL: %w", err)
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("custom server URL must use http:// or https:// with a valid host")
	}
	return u.String(), nil
}

func (c *CustomProvider) Type() ProviderType {
	return ProviderCustom
}

func (c *CustomProvider) DiscoverClient(ctx context.Context) (ClientInfo, error) {
	// Best-effort client metadata lookup via Cloudflare meta (does not fail custom test if offline from internet).
	cf := NewCloudflareProvider(c.client)
	info, err := cf.DiscoverClient(ctx)
	if err != nil {
		return ClientInfo{IP: "Local / Custom", ISP: "Custom Endpoint"}, nil
	}
	return info, nil
}

func (c *CustomProvider) DiscoverServers(ctx context.Context, client ClientInfo) ([]Server, error) {
	c.detectMode(ctx)

	u, _ := url.Parse(c.normURL)
	srv := Server{
		ID:       "custom-url",
		Name:     fmt.Sprintf("Custom (%s)", u.Host),
		Sponsor:  "Custom Server",
		City:     u.Host,
		Country:  "Custom",
		Host:     u.Host,
		URL:      c.normURL,
		Provider: ProviderCustom,
	}

	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	d, err := c.ProbeLatency(probeCtx, srv)
	if err != nil {
		return nil, fmt.Errorf("failed to reach custom server %s: %w", c.normURL, err)
	}
	srv.LatencyMS = Round2(float64(d) / float64(time.Millisecond))
	return []Server{srv}, nil
}

func (c *CustomProvider) detectMode(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.mode == customModeCloudflare || c.mode == customModeOokla {
		if c.mode == customModeOokla {
			c.ulURL = c.baseURL + "/upload.php"
		} else {
			c.ulURL = "https://speed.cloudflare.com/__up"
		}
		return
	}

	// Probe whether the server exposes an Ookla-compatible /latency.txt endpoint.
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	latencyEndpoint := c.baseURL + "/latency.txt"
	if req, err := newRequest(probeCtx, http.MethodGet, latencyEndpoint, nil); err == nil {
		if resp, err := c.client.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 256))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				c.mode = customModeOokla
				c.ulURL = c.baseURL + "/upload.php"
				return
			}
		}
	}

	// Otherwise remain in generic HTTP mode and determine best upload path.
	c.ulURL = c.normURL
}

func (c *CustomProvider) ProbeLatency(ctx context.Context, server Server) (time.Duration, error) {
	c.mu.RLock()
	mode := c.mode
	base := c.baseURL
	norm := c.normURL
	c.mu.RUnlock()

	switch mode {
	case customModeCloudflare:
		return measureHTTPRTT(ctx, c.client, fmt.Sprintf("https://speed.cloudflare.com/__down?bytes=0&x=%d", time.Now().UnixNano()))
	case customModeOokla:
		return measureHTTPRTT(ctx, c.client, fmt.Sprintf("%s/latency.txt?x=%d", base, time.Now().UnixNano()))
	default:
		// Try HEAD first so we don't pull huge static payloads during latency checks; fall back to GET.
		req, err := newRequest(ctx, http.MethodHead, norm, nil)
		if err != nil {
			return 0, err
		}
		start := time.Now()
		resp, err := c.client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 400 {
				return time.Since(start), nil
			}
		}
		return measureHTTPRTT(ctx, c.client, norm)
	}
}

func (c *CustomProvider) DownloadURL(server Server) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	switch c.mode {
	case customModeCloudflare:
		return "https://speed.cloudflare.com/__down?bytes=25000000"
	case customModeOokla:
		return c.baseURL + "/download?size=25000000"
	default:
		return c.normURL
	}
}

func (c *CustomProvider) FallbackDownloadURL(server Server) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	switch c.mode {
	case customModeCloudflare:
		return "https://speed.cloudflare.com/__down?bytes=10000000"
	case customModeOokla:
		return c.baseURL + "/random4000x4000.jpg"
	default:
		return ""
	}
}

func (c *CustomProvider) UploadURL(server Server) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ulURL
}
