package engine

import (
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Round2 rounds a float to two decimal places.
func Round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// HaversineKM calculates the great-circle distance in kilometers between two coordinates.
func HaversineKM(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKM = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180.0
	dLon := (lon2 - lon1) * math.Pi / 180.0
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180.0)*math.Cos(lat2*math.Pi/180.0)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return Round2(earthRadiusKM * c)
}

// ComputePingAndJitter computes minimum RTT, mean RTT, and RFC-style inter-probe jitter (all in ms).
func ComputePingAndJitter(rtts []time.Duration) (minMS, avgMS, jitterMS float64) {
	if len(rtts) == 0 {
		return 0, 0, 0
	}
	minD := rtts[0]
	var sum float64
	var diffSum float64
	for i, d := range rtts {
		if d < minD {
			minD = d
		}
		ms := float64(d) / float64(time.Millisecond)
		sum += ms
		if i > 0 {
			prevMS := float64(rtts[i-1]) / float64(time.Millisecond)
			diffSum += math.Abs(ms - prevMS)
		}
	}
	minMS = Round2(float64(minD) / float64(time.Millisecond))
	avgMS = Round2(sum / float64(len(rtts)))
	if len(rtts) > 1 {
		jitterMS = Round2(diffSum / float64(len(rtts)-1))
	}
	return minMS, avgMS, jitterMS
}

// ComputeTrimmedMeanMbps implements an Ookla-style trimmed mean over fixed-interval bucket samples.
// It trims the initial 15% TCP slow-start ramp-up, then discards the lowest 10% and highest 5% of
// the remaining sorted samples before averaging.
func ComputeTrimmedMeanMbps(samples []float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	if len(samples) <= 3 {
		var sum float64
		for _, s := range samples {
			sum += s
		}
		return Round2(sum / float64(len(samples)))
	}

	// Discard initial slow-start ramp-up when enough samples exist.
	startIdx := 0
	if len(samples) > 6 {
		startIdx = int(math.Floor(float64(len(samples)) * 0.15))
		if startIdx >= len(samples)-2 {
			startIdx = 0
		}
	}

	trimmed := make([]float64, len(samples)-startIdx)
	copy(trimmed, samples[startIdx:])
	sort.Float64s(trimmed)

	lowCut := int(math.Floor(float64(len(trimmed)) * 0.10))
	highCut := int(math.Floor(float64(len(trimmed)) * 0.05))
	if lowCut+highCut >= len(trimmed) {
		lowCut = 0
		highCut = 0
	}

	middle := trimmed[lowCut : len(trimmed)-highCut]
	var sum float64
	for _, v := range middle {
		sum += v
	}
	return Round2(sum / float64(len(middle)))
}

// FormatSpeed converts Mbps into the requested display unit and canonical label.
func FormatSpeed(mbps float64, unit string) (float64, string) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "mb/s", "mbytes/s":
		return Round2(mbps / 8.0), "MB/s"
	case "kbps":
		return Round2(mbps * 1000.0), "Kbps"
	default:
		return Round2(mbps), "Mbps"
	}
}

// BandwidthMeter aggregates atomic byte transfers from multiple worker goroutines,
// computes an EMA-smoothed live rate for 50ms UI ticks, and collects 200ms buckets for trimmed mean.
type BandwidthMeter struct {
	totalBytes  atomic.Int64
	mu          sync.Mutex
	startTime   time.Time
	lastTick    time.Time
	lastBytes   int64
	bucketStart time.Time
	bucketBytes int64
	emaMbps     float64
	buckets     []float64
	uiSamples   []float64
}

// NewBandwidthMeter creates and starts a BandwidthMeter.
func NewBandwidthMeter() *BandwidthMeter {
	now := time.Now()
	return &BandwidthMeter{
		startTime:   now,
		lastTick:    now,
		bucketStart: now,
	}
}

// AddBytes records transferred bytes from any concurrent stream worker.
func (m *BandwidthMeter) AddBytes(n int64) {
	if n > 0 {
		m.totalBytes.Add(n)
	}
}

// TotalBytes returns the cumulative bytes recorded so far.
func (m *BandwidthMeter) TotalBytes() int64 {
	return m.totalBytes.Load()
}

// Tick computes the instantaneous and EMA-smoothed Mbps since the last tick,
// and flushes a 200ms bucket sample whenever at least 200ms has elapsed.
func (m *BandwidthMeter) Tick() (liveMbps float64, totalBytes int64, uiSamples []float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	total := m.totalBytes.Load()
	dt := now.Sub(m.lastTick).Seconds()
	if dt > 0 {
		delta := total - m.lastBytes
		instMbps := (float64(delta) * 8.0) / (dt * 1_000_000.0)
		if m.emaMbps == 0 {
			m.emaMbps = instMbps
		} else {
			const alpha = 0.35
			m.emaMbps = alpha*instMbps + (1-alpha)*m.emaMbps
		}
		m.lastTick = now
		m.lastBytes = total
		m.uiSamples = append(m.uiSamples, Round2(m.emaMbps))
	}

	bdt := now.Sub(m.bucketStart).Seconds()
	if bdt >= 0.20 {
		bDelta := total - m.bucketBytes
		bMbps := (float64(bDelta) * 8.0) / (bdt * 1_000_000.0)
		m.buckets = append(m.buckets, bMbps)
		m.bucketStart = now
		m.bucketBytes = total
	}

	cp := make([]float64, len(m.uiSamples))
	copy(cp, m.uiSamples)
	return Round2(m.emaMbps), total, cp
}

// Finalize flushes any remaining bucket and computes the final trimmed-mean Mbps.
func (m *BandwidthMeter) Finalize() (finalMbps float64, totalBytes int64, uiSamples []float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	total := m.totalBytes.Load()
	bdt := now.Sub(m.bucketStart).Seconds()
	if bdt >= 0.05 && total > m.bucketBytes {
		bDelta := total - m.bucketBytes
		bMbps := (float64(bDelta) * 8.0) / (bdt * 1_000_000.0)
		m.buckets = append(m.buckets, bMbps)
		m.bucketStart = now
		m.bucketBytes = total
	}

	if len(m.buckets) == 0 {
		elapsed := now.Sub(m.startTime).Seconds()
		if elapsed > 0 && total > 0 {
			finalMbps = Round2((float64(total) * 8.0) / (elapsed * 1_000_000.0))
		}
	} else {
		finalMbps = ComputeTrimmedMeanMbps(m.buckets)
	}

	cp := make([]float64, len(m.uiSamples))
	copy(cp, m.uiSamples)
	return finalMbps, total, cp
}
