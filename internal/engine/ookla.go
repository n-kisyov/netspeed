package engine

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type OoklaProvider struct {
	client *http.Client
}

func NewOoklaProvider(client *http.Client) *OoklaProvider {
	return &OoklaProvider{client: client}
}

func (o *OoklaProvider) Type() ProviderType {
	return ProviderOokla
}

type ooklaConfigXML struct {
	Client struct {
		IP  string `xml:"ip,attr"`
		Lat string `xml:"lat,attr"`
		Lon string `xml:"lon,attr"`
		ISP string `xml:"isp,attr"`
	} `xml:"client"`
}

func (o *OoklaProvider) DiscoverClient(ctx context.Context) (ClientInfo, error) {
	req, err := newRequest(ctx, http.MethodGet, "https://www.speedtest.net/speedtest-config.php", nil)
	if err != nil {
		return ClientInfo{}, err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return ClientInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return ClientInfo{}, fmt.Errorf("ookla config HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return ClientInfo{}, err
	}

	var cfg ooklaConfigXML
	if err := xml.Unmarshal(body, &cfg); err != nil {
		return ClientInfo{}, fmt.Errorf("parse ookla config: %w", err)
	}

	lat, _ := strconv.ParseFloat(cfg.Client.Lat, 64)
	lon, _ := strconv.ParseFloat(cfg.Client.Lon, 64)
	return ClientInfo{
		IP:  cfg.Client.IP,
		ISP: cfg.Client.ISP,
		Lat: lat,
		Lon: lon,
	}, nil
}

type ooklaJSONServer struct {
	ID       json.RawMessage `json:"id"`
	URL      string          `json:"url"`
	Lat      json.RawMessage `json:"lat"`
	Lon      json.RawMessage `json:"lon"`
	Distance float64         `json:"distance"`
	Name     string          `json:"name"`
	Country  string          `json:"country"`
	Sponsor  string          `json:"sponsor"`
	Host     string          `json:"host"`
}

type ooklaServersXML struct {
	Servers struct {
		Items []struct {
			ID      string `xml:"id,attr"`
			URL     string `xml:"url,attr"`
			Lat     string `xml:"lat,attr"`
			Lon     string `xml:"lon,attr"`
			Name    string `xml:"name,attr"`
			Country string `xml:"country,attr"`
			Sponsor string `xml:"sponsor,attr"`
			Host    string `xml:"host,attr"`
		} `xml:"server"`
	} `xml:"servers"`
}

func rawToString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	s = strings.Trim(s, `"`)
	return s
}

func rawToFloat(raw json.RawMessage) float64 {
	v, _ := strconv.ParseFloat(rawToString(raw), 64)
	return v
}

func (o *OoklaProvider) DiscoverServers(ctx context.Context, client ClientInfo) ([]Server, error) {
	servers, err := o.fetchServersJSON(ctx, client)
	if err != nil || len(servers) == 0 {
		xmlServers, xmlErr := o.fetchServersXML(ctx, client)
		if xmlErr != nil && err != nil {
			return nil, fmt.Errorf("ookla servers unavailable: %w; xml fallback: %v", err, xmlErr)
		}
		if len(xmlServers) > 0 {
			servers = xmlServers
		}
	}
	if len(servers) == 0 {
		return nil, errors.New("no ookla servers found")
	}

	sort.Slice(servers, func(i, j int) bool {
		return servers[i].DistanceKM < servers[j].DistanceKM
	})

	if len(servers) > 25 {
		servers = servers[:25]
	}

	// Probe the closest 8 servers concurrently to determine real latency.
	probeBatch := func(startIdx, endIdx int) bool {
		if startIdx >= endIdx {
			return false
		}
		var wg sync.WaitGroup
		for i := startIdx; i < endIdx; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				probeCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
				defer cancel()
				d, probeErr := o.ProbeLatency(probeCtx, servers[idx])
				if probeErr == nil {
					servers[idx].LatencyMS = Round2(float64(d) / float64(time.Millisecond))
				} else {
					servers[idx].LatencyMS = 9999
				}
			}(i)
		}
		wg.Wait()
		for i := startIdx; i < endIdx; i++ {
			if servers[i].LatencyMS > 0 && servers[i].LatencyMS < 9000 {
				return true
			}
		}
		return false
	}

	probeLimit := 8
	if len(servers) < probeLimit {
		probeLimit = len(servers)
	}
	anyResponsive := probeBatch(0, probeLimit)
	if !anyResponsive && len(servers) > probeLimit {
		secondEnd := probeLimit + 6
		if secondEnd > len(servers) {
			secondEnd = len(servers)
		}
		anyResponsive = probeBatch(probeLimit, secondEnd)
	}

	// Rank servers: 0 = responsive probed (lowest latency first), 1 = unprobed, 2 = failed probes.
	latencyRank := func(lat float64) int {
		if lat > 0 && lat < 9000 {
			return 0
		}
		if lat == 0 {
			return 1
		}
		return 2
	}
	sort.SliceStable(servers, func(i, j int) bool {
		ri, rj := latencyRank(servers[i].LatencyMS), latencyRank(servers[j].LatencyMS)
		if ri != rj {
			return ri < rj
		}
		if ri == 0 {
			return servers[i].LatencyMS < servers[j].LatencyMS
		}
		return servers[i].DistanceKM < servers[j].DistanceKM
	})

	if !anyResponsive || servers[0].LatencyMS >= 9000 || servers[0].LatencyMS <= 0 {
		return nil, errors.New("all probed ookla servers failed latency check")
	}

	return servers, nil
}

func (o *OoklaProvider) fetchServersJSON(ctx context.Context, client ClientInfo) ([]Server, error) {
	req, err := newRequest(ctx, http.MethodGet, "https://www.speedtest.net/api/js/servers?engine=js&limit=20&https_functional=true", nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ookla json servers HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}

	var rawList []ooklaJSONServer
	if err := json.Unmarshal(body, &rawList); err != nil {
		return nil, err
	}

	servers := make([]Server, 0, len(rawList))
	for _, s := range rawList {
		if s.URL == "" {
			continue
		}
		dist := Round2(s.Distance)
		if dist == 0 && (client.Lat != 0 || client.Lon != 0) {
			lat := rawToFloat(s.Lat)
			lon := rawToFloat(s.Lon)
			if lat != 0 || lon != 0 {
				dist = HaversineKM(client.Lat, client.Lon, lat, lon)
			}
		}
		servers = append(servers, Server{
			ID:         rawToString(s.ID),
			Name:       fmt.Sprintf("%s (%s)", s.Sponsor, s.Name),
			Sponsor:    s.Sponsor,
			City:       s.Name,
			Country:    s.Country,
			Host:       s.Host,
			URL:        normalizeOoklaUploadURL(s.URL),
			DistanceKM: dist,
			Provider:   ProviderOokla,
		})
	}
	return servers, nil
}

func (o *OoklaProvider) fetchServersXML(ctx context.Context, client ClientInfo) ([]Server, error) {
	endpoints := []string{
		"https://c.speedtest.net/speedtest-servers-static.php",
		"https://www.speedtest.net/speedtest-servers.php",
	}
	var lastErr error
	for _, ep := range endpoints {
		req, err := newRequest(ctx, http.MethodGet, ep, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := o.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
		_ = resp.Body.Close()
		if resp.StatusCode >= 400 || readErr != nil {
			lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, ep)
			continue
		}

		var parsed ooklaServersXML
		if err := xml.Unmarshal(body, &parsed); err != nil {
			lastErr = err
			continue
		}
		servers := make([]Server, 0, len(parsed.Servers.Items))
		for _, s := range parsed.Servers.Items {
			if s.URL == "" {
				continue
			}
			lat, _ := strconv.ParseFloat(s.Lat, 64)
			lon, _ := strconv.ParseFloat(s.Lon, 64)
			dist := 0.0
			if client.Lat != 0 || client.Lon != 0 {
				dist = HaversineKM(client.Lat, client.Lon, lat, lon)
			}
			servers = append(servers, Server{
				ID:         s.ID,
				Name:       fmt.Sprintf("%s (%s)", s.Sponsor, s.Name),
				Sponsor:    s.Sponsor,
				City:       s.Name,
				Country:    s.Country,
				Host:       s.Host,
				URL:        normalizeOoklaUploadURL(s.URL),
				DistanceKM: dist,
				Provider:   ProviderOokla,
			})
		}
		if len(servers) > 0 {
			return servers, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no XML servers returned")
	}
	return nil, lastErr
}

func normalizeOoklaUploadURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	return raw
}

func ooklaBaseURL(uploadURL string) string {
	u, err := url.Parse(uploadURL)
	if err != nil {
		return strings.TrimSuffix(uploadURL, "/upload.php")
	}
	idx := strings.LastIndex(u.Path, "/")
	if idx >= 0 {
		u.Path = u.Path[:idx]
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func (o *OoklaProvider) ProbeLatency(ctx context.Context, server Server) (time.Duration, error) {
	base := ooklaBaseURL(server.URL)
	target := fmt.Sprintf("%s/latency.txt?x=%d", base, time.Now().UnixNano())
	return measureHTTPRTT(ctx, o.client, target)
}

func (o *OoklaProvider) DownloadURL(server Server) string {
	base := ooklaBaseURL(server.URL)
	return fmt.Sprintf("%s/download?size=25000000", base)
}

func (o *OoklaProvider) FallbackDownloadURL(server Server) string {
	base := ooklaBaseURL(server.URL)
	return fmt.Sprintf("%s/random4000x4000.jpg", base)
}

func (o *OoklaProvider) UploadURL(server Server) string {
	if strings.HasSuffix(strings.ToLower(server.URL), ".php") || strings.Contains(server.URL, "upload") {
		return server.URL
	}
	return ooklaBaseURL(server.URL) + "/upload.php"
}
