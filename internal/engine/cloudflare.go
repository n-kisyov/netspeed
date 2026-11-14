package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type CloudflareProvider struct {
	client *http.Client
}

func NewCloudflareProvider(client *http.Client) *CloudflareProvider {
	return &CloudflareProvider{client: client}
}

func (c *CloudflareProvider) Type() ProviderType {
	return ProviderCloudflare
}

type cloudflareMeta struct {
	ClientIP       string          `json:"clientIp"`
	ASN            json.RawMessage `json:"asn"`
	ASOrganization string          `json:"asOrganization"`
	City           string          `json:"city"`
	Country        string          `json:"country"`
	Colo           string          `json:"colo"`
	Latitude       json.RawMessage `json:"latitude"`
	Longitude      json.RawMessage `json:"longitude"`
}

func (c *CloudflareProvider) fetchMeta(ctx context.Context) (ClientInfo, string, error) {
	req, err := newRequest(ctx, http.MethodGet, "https://speed.cloudflare.com/meta", nil)
	if err != nil {
		return ClientInfo{}, "", err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return ClientInfo{}, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return ClientInfo{}, "", fmt.Errorf("cloudflare meta HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return ClientInfo{}, "", err
	}

	var meta cloudflareMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return ClientInfo{}, "", err
	}

	info := ClientInfo{
		IP:      meta.ClientIP,
		ISP:     meta.ASOrganization,
		ASN:     rawToString(meta.ASN),
		City:    meta.City,
		Country: meta.Country,
		Lat:     rawToFloat(meta.Latitude),
		Lon:     rawToFloat(meta.Longitude),
	}
	return info, meta.Colo, nil
}

func (c *CloudflareProvider) DiscoverClient(ctx context.Context) (ClientInfo, error) {
	info, _, err := c.fetchMeta(ctx)
	return info, err
}

func (c *CloudflareProvider) DiscoverServers(ctx context.Context, client ClientInfo) ([]Server, error) {
	info, colo, err := c.fetchMeta(ctx)
	city := info.City
	if city == "" {
		city = "Anycast Edge"
	}
	if colo != "" {
		city = fmt.Sprintf("%s (%s)", city, colo)
	}
	country := info.Country
	if country == "" {
		country = "Global"
	}

	srv := Server{
		ID:       "cloudflare-edge",
		Name:     fmt.Sprintf("Cloudflare Edge — %s", city),
		Sponsor:  "Cloudflare",
		City:     city,
		Country:  country,
		Host:     "speed.cloudflare.com",
		URL:      "https://speed.cloudflare.com",
		Provider: ProviderCloudflare,
	}

	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if d, probeErr := c.ProbeLatency(probeCtx, srv); probeErr == nil {
		srv.LatencyMS = Round2(float64(d) / float64(time.Millisecond))
	} else if err != nil {
		return nil, err
	}

	return []Server{srv}, nil
}

func (c *CloudflareProvider) ProbeLatency(ctx context.Context, server Server) (time.Duration, error) {
	target := fmt.Sprintf("https://speed.cloudflare.com/__down?bytes=0&x=%d", time.Now().UnixNano())
	return measureHTTPRTT(ctx, c.client, target)
}

func (c *CloudflareProvider) DownloadURL(server Server) string {
	return "https://speed.cloudflare.com/__down?bytes=25000000"
}

func (c *CloudflareProvider) FallbackDownloadURL(server Server) string {
	return "https://speed.cloudflare.com/__down?bytes=10000000"
}

func (c *CloudflareProvider) UploadURL(server Server) string {
	return "https://speed.cloudflare.com/__up"
}
