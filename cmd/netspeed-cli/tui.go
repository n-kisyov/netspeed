package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"netspeed/internal/engine"
)

const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiDim     = "\x1b[2m"
	ansiCyan    = "\x1b[36m"
	ansiMagenta = "\x1b[35m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiRed     = "\x1b[31m"
)

type terminalUI struct {
	mu          sync.Mutex
	ansiEnabled bool
	unit        string
	printedInfo bool
	lastPhase   engine.Phase
	lastUpdate  time.Time
}

func newTerminalUI(unit string) *terminalUI {
	return &terminalUI{
		ansiEnabled: enableTerminalVT(),
		unit:        unit,
	}
}

func (u *terminalUI) c(code, text string) string {
	if !u.ansiEnabled {
		return text
	}
	return code + text + ansiReset
}

func (u *terminalUI) printBanner(cfg engine.TestConfig) {
	fmt.Println()
	fmt.Printf("  %s %s\n", u.c(ansiBold+ansiCyan, "NetSpeed CLI"), u.c(ansiDim, fmt.Sprintf("(%s · %d threads · %ds)", cfg.Provider, cfg.Threads, int(cfg.Duration.Seconds()))))
	fmt.Println("  " + strings.Repeat("─", 56))
}

func (u *terminalUI) onProgress(ev engine.ProgressEvent) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if !u.printedInfo && ev.Server.Name != "" {
		isp := ev.Client.ISP
		if isp == "" {
			isp = "Unknown ISP"
		}
		ip := ev.Client.IP
		if ip == "" {
			ip = "—"
		}
		fmt.Printf("  %-11s %s (%s)\n", u.c(ansiDim, "Client:"), isp, ip)
		loc := ev.Server.Name
		if ev.Server.DistanceKM > 0 {
			loc = fmt.Sprintf("%s [%.0f km]", ev.Server.Name, ev.Server.DistanceKM)
		}
		fmt.Printf("  %-11s %s\n", u.c(ansiDim, "Server:"), loc)
		if ev.Message != "" && strings.Contains(strings.ToLower(ev.Message), "cloudflare") {
			fmt.Printf("  %-11s %s\n", u.c(ansiYellow, "Notice:"), ev.Message)
		}
		fmt.Println()
		u.printedInfo = true
	}

	switch ev.Phase {
	case engine.PhaseInit:
		if u.lastPhase != engine.PhaseInit {
			fmt.Printf("\r  %s", u.c(ansiDim, ev.Message))
			u.lastPhase = engine.PhaseInit
		}
	case engine.PhasePing:
		if ev.Progress >= 1 {
			fmt.Printf("\r  %-11s %s   Jitter: %s   Loss: %s%s\n",
				u.c(ansiGreen, "Idle Ping:"),
				u.c(ansiBold, fmt.Sprintf("%.2f ms", ev.PingMS)),
				u.c(ansiBold, fmt.Sprintf("%.2f ms", ev.JitterMS)),
				u.c(ansiBold, fmt.Sprintf("%.1f%%", ev.PacketLossPct)),
				strings.Repeat(" ", 16))
			u.lastPhase = engine.PhasePing
		} else if u.lastPhase != engine.PhasePing {
			fmt.Printf("\r  %s%s", u.c(ansiGreen, "Measuring ping & jitter..."), strings.Repeat(" ", 25))
			u.lastPhase = engine.PhasePing
		}
	case engine.PhaseDownload, engine.PhaseUpload:
		if u.lastPhase != ev.Phase {
			u.lastPhase = ev.Phase
			u.lastUpdate = time.Time{}
		}
		if time.Since(u.lastUpdate) < 66*time.Millisecond && ev.Progress < 0.99 {
			return
		}
		u.lastUpdate = time.Now()

		label := "Download:"
		color := ansiCyan
		if ev.Phase == engine.PhaseUpload {
			label = "Upload:"
			color = ansiMagenta
		}
		val, unitLabel := engine.FormatSpeed(ev.CurrentMbps, u.unit)
		bar := renderBar(ev.Progress, 24)
		spark := renderSparkChar(ev.Samples)
		loaded := ""
		if ev.LoadedPingMS > 0 {
			loaded = fmt.Sprintf(" · ping %.0fms", ev.LoadedPingMS)
		}
		fmt.Printf("\r  %-11s %s %s %9.2f %-4s %s%s   ",
			u.c(color, label),
			u.c(color, bar),
			u.c(ansiDim, fmt.Sprintf("%3.0f%%", ev.Progress*100)),
			val,
			unitLabel,
			u.c(ansiDim, spark+loaded),
			"",
		)
	case engine.PhaseDone:
		fmt.Print("\r" + strings.Repeat(" ", 88) + "\r")
	case engine.PhaseError:
		fmt.Printf("\n  %s %v\n", u.c(ansiRed, "Error:"), ev.Err)
	}
}

func (u *terminalUI) printSummary(res engine.Result) {
	dlVal, dlUnit := engine.FormatSpeed(res.DownloadMbps, u.unit)
	ulVal, ulUnit := engine.FormatSpeed(res.UploadMbps, u.unit)
	dlMB := float64(res.DownloadBytes) / (1024 * 1024)
	ulMB := float64(res.UploadBytes) / (1024 * 1024)

	fmt.Println("  ┌──────────────────────────────────────────────────────┐")
	fmt.Printf("  │ %-52s │\n", "NetSpeed Test Summary")
	fmt.Println("  ├──────────────────────────────────────────────────────┤")
	fmt.Printf("  │ %-16s %8.2f %-26s │\n", "Download:", dlVal, fmt.Sprintf("%s  (%.1f MB)", dlUnit, dlMB))
	fmt.Printf("  │ %-16s %8.2f %-26s │\n", "Upload:", ulVal, fmt.Sprintf("%s  (%.1f MB)", ulUnit, ulMB))
	fmt.Printf("  │ %-16s %8.2f %-26s │\n", "Idle Ping:", res.PingMS, fmt.Sprintf("ms  (Jitter: %.2f ms)", res.JitterMS))
	fmt.Printf("  │ %-16s %8.1f %-26s │\n", "Packet Loss:", res.PacketLossPct, "%")
	if res.DownloadPingMS > 0 || res.UploadPingMS > 0 {
		fmt.Printf("  │ %-16s  DL: %.1f ms  ·  UL: %.1f ms%-11s │\n", "Loaded Ping:", res.DownloadPingMS, res.UploadPingMS, "")
	}
	if res.BufferbloatGrade != "" {
		fmt.Printf("  │ %-16s %-35s │\n", "Quality Grade:", fmt.Sprintf("%s (Bufferbloat)", res.BufferbloatGrade))
	}
	fmt.Println("  ├──────────────────────────────────────────────────────┤")
	fmt.Printf("  │ %-16s %-35.35s │\n", "Provider:", string(res.Provider))
	fmt.Printf("  │ %-16s %-35.35s │\n", "Server:", res.Server.Name)
	if res.Client.ISP != "" {
		fmt.Printf("  │ %-16s %-35.35s │\n", "ISP:", res.Client.ISP)
	}
	fmt.Println("  └──────────────────────────────────────────────────────┘")
	fmt.Println()
}

func renderBar(progress float64, width int) string {
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	filled := int(progress * float64(width))
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func renderSparkChar(samples []float64) string {
	if len(samples) == 0 {
		return ""
	}
	blocks := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	start := 0
	if len(samples) > 10 {
		start = len(samples) - 10
	}
	tail := samples[start:]
	maxV := 1.0
	for _, s := range tail {
		if s > maxV {
			maxV = s
		}
	}
	var b strings.Builder
	for _, s := range tail {
		idx := int((s / maxV) * float64(len(blocks)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		b.WriteRune(blocks[idx])
	}
	return b.String()
}
