package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"netspeed/internal/config"
	"netspeed/internal/engine"
	"netspeed/internal/history"
)

type cliOptions struct {
	listServers  bool
	serverID     string
	provider     string
	customURL    string
	threads      int
	durationSec  int
	unit         string
	noDownload   bool
	noUpload     bool
	jsonOut      bool
	csvOut       bool
	csvHeader    bool
	simpleOut    bool
	showHistory  bool
	clearHistory bool
	scheduleStr  string
}

func parseFlags(defaults config.Config) cliOptions {
	var o cliOptions
	flag.BoolVar(&o.listServers, "L", false, "List nearest speed test servers")
	flag.BoolVar(&o.listServers, "list", false, "List nearest speed test servers")

	flag.StringVar(&o.serverID, "s", "", "Target server ID")
	flag.StringVar(&o.serverID, "server", "", "Target server ID")

	flag.StringVar(&o.provider, "p", defaults.Provider, "Provider: auto, ookla, cloudflare, custom")
	flag.StringVar(&o.provider, "provider", defaults.Provider, "Provider: auto, ookla, cloudflare, custom")

	flag.StringVar(&o.customURL, "u", "", "Custom server URL")
	flag.StringVar(&o.customURL, "url", "", "Custom server URL")

	flag.IntVar(&o.threads, "t", defaults.Threads, "Parallel transfer threads (1-32)")
	flag.IntVar(&o.threads, "threads", defaults.Threads, "Parallel transfer threads (1-32)")

	flag.IntVar(&o.durationSec, "d", defaults.DurationSeconds, "Duration per phase in seconds")
	flag.IntVar(&o.durationSec, "duration", defaults.DurationSeconds, "Duration per phase in seconds")

	flag.StringVar(&o.unit, "unit", defaults.Unit, "Display unit: Mbps, MB/s, Kbps")
	flag.BoolVar(&o.noDownload, "no-download", false, "Skip download phase")
	flag.BoolVar(&o.noUpload, "no-upload", false, "Skip upload phase")
	flag.BoolVar(&o.jsonOut, "json", false, "Output result as JSON")
	flag.BoolVar(&o.csvOut, "csv", false, "Output result as CSV row")
	flag.BoolVar(&o.csvHeader, "csv-header", false, "Include CSV header row with --csv")
	flag.BoolVar(&o.simpleOut, "simple", false, "Compact 3-line output")
	flag.BoolVar(&o.showHistory, "history", false, "Print saved test history")
	flag.BoolVar(&o.clearHistory, "clear-history", false, "Clear saved test history")
	flag.StringVar(&o.scheduleStr, "schedule", "", "Run repeatedly on interval (e.g. 15m, 1h)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "NetSpeed CLI — Speedtest.net & Cloudflare desktop/CLI clone\n\nUsage:\n  netspeed-cli [flags]\n\nFlags:\n")
		fmt.Fprintf(os.Stderr, "  -L, --list              List nearby available servers\n")
		fmt.Fprintf(os.Stderr, "  -s, --server <id>       Run test against specific server ID\n")
		fmt.Fprintf(os.Stderr, "  -p, --provider <name>   Provider: auto, ookla, cloudflare, custom (default %q)\n", defaults.Provider)
		fmt.Fprintf(os.Stderr, "  -u, --url <url>         Test against a custom HTTP/HTTPS/Ookla server URL\n")
		fmt.Fprintf(os.Stderr, "  -t, --threads <n>       Parallel transfer threads (default %d)\n", defaults.Threads)
		fmt.Fprintf(os.Stderr, "  -d, --duration <sec>    Per-phase duration in seconds (default %d)\n", defaults.DurationSeconds)
		fmt.Fprintf(os.Stderr, "      --unit <unit>       Speed unit: Mbps, MB/s, Kbps (default %q)\n", defaults.Unit)
		fmt.Fprintf(os.Stderr, "      --no-download       Skip download measurement\n")
		fmt.Fprintf(os.Stderr, "      --no-upload         Skip upload measurement\n")
		fmt.Fprintf(os.Stderr, "      --json              Output final result as JSON\n")
		fmt.Fprintf(os.Stderr, "      --csv               Output final result as CSV\n")
		fmt.Fprintf(os.Stderr, "      --csv-header        Include CSV header row with --csv\n")
		fmt.Fprintf(os.Stderr, "      --simple            Output Ping / Download / Upload only\n")
		fmt.Fprintf(os.Stderr, "      --history           Show recent saved test history (GUI + CLI)\n")
		fmt.Fprintf(os.Stderr, "      --clear-history     Delete saved test history\n")
		fmt.Fprintf(os.Stderr, "      --schedule <dur>    Run continuously at interval (e.g. 30m, 1h)\n")
	}

	flag.Parse()
	return o
}

func main() {
	cfgDefaults := config.Load()
	opts := parseFlags(cfgDefaults)
	store := history.DefaultStore()
	eng := engine.New()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if opts.clearHistory {
		if err := store.Clear(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to clear history: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("History cleared.")
		return
	}

	if opts.showHistory {
		if err := printHistory(store, opts.unit); err != nil {
			fmt.Fprintf(os.Stderr, "failed to read history: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if opts.listServers {
		prov := engine.ProviderType(strings.ToLower(strings.TrimSpace(opts.provider)))
		if err := listServersCmd(ctx, eng, prov); err != nil {
			fmt.Fprintf(os.Stderr, "server discovery failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	provider := engine.ProviderType(strings.ToLower(strings.TrimSpace(opts.provider)))
	if opts.customURL != "" {
		provider = engine.ProviderCustom
	}

	testCfg := engine.TestConfig{
		Provider:     provider,
		ServerID:     opts.serverID,
		CustomURL:    opts.customURL,
		Threads:      opts.threads,
		Duration:     time.Duration(opts.durationSec) * time.Second,
		SkipDownload: opts.noDownload,
		SkipUpload:   opts.noUpload,
		Trigger:      "manual",
	}

	if opts.scheduleStr != "" {
		interval, err := time.ParseDuration(opts.scheduleStr)
		if err != nil || interval < time.Minute {
			fmt.Fprintf(os.Stderr, "invalid --schedule interval %q (minimum 1m, e.g. 15m, 1h)\n", opts.scheduleStr)
			os.Exit(1)
		}
		testCfg.Trigger = "scheduled"
		runScheduledLoop(ctx, eng, store, testCfg, opts, interval)
		return
	}

	if err := executeSingleRun(ctx, eng, store, testCfg, opts); err != nil {
		fmt.Fprintf(os.Stderr, "speed test failed: %v\n", err)
		os.Exit(1)
	}
}

func runScheduledLoop(ctx context.Context, eng *engine.Engine, store *history.Store, cfg engine.TestConfig, opts cliOptions, interval time.Duration) {
	fmt.Printf("Starting scheduled CLI mode (every %s). Press Ctrl+C to stop.\n", interval)
	for {
		if err := executeSingleRun(ctx, eng, store, cfg, opts); err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintf(os.Stderr, "[%s] scheduled test error: %v\n", time.Now().Format("15:04:05"), err)
		}
		next := time.Now().Add(interval)
		if !opts.jsonOut && !opts.csvOut {
			fmt.Printf("Next scheduled test at %s\n", next.Format("2006-01-02 15:04:05"))
		}
		select {
		case <-ctx.Done():
			fmt.Println("\nScheduled CLI stopped.")
			return
		case <-time.After(interval):
		}
	}
}

func executeSingleRun(ctx context.Context, eng *engine.Engine, store *history.Store, cfg engine.TestConfig, opts cliOptions) error {
	machineReadable := opts.jsonOut || opts.csvOut || opts.simpleOut

	var tui *terminalUI
	var cb engine.ProgressCallback
	if !machineReadable {
		tui = newTerminalUI(opts.unit)
		tui.printBanner(cfg)
		cb = tui.onProgress
	}

	res, err := eng.Run(ctx, cfg, cb)
	if err != nil {
		return err
	}

	_ = store.Append(res)

	switch {
	case opts.jsonOut:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	case opts.csvOut:
		cw := csv.NewWriter(os.Stdout)
		if opts.csvHeader {
			_ = cw.Write([]string{"timestamp", "provider", "server", "ping_ms", "jitter_ms", "download_mbps", "upload_mbps", "download_bytes", "upload_bytes"})
		}
		_ = cw.Write([]string{
			res.Timestamp.Format(time.RFC3339),
			string(res.Provider),
			res.Server.Name,
			fmt.Sprintf("%.2f", res.PingMS),
			fmt.Sprintf("%.2f", res.JitterMS),
			fmt.Sprintf("%.2f", res.DownloadMbps),
			fmt.Sprintf("%.2f", res.UploadMbps),
			fmt.Sprintf("%d", res.DownloadBytes),
			fmt.Sprintf("%d", res.UploadBytes),
		})
		cw.Flush()
		return cw.Error()
	case opts.simpleOut:
		dlVal, dlUnit := engine.FormatSpeed(res.DownloadMbps, opts.unit)
		ulVal, ulUnit := engine.FormatSpeed(res.UploadMbps, opts.unit)
		fmt.Printf("Ping: %.2f ms (Jitter: %.2f ms)\n", res.PingMS, res.JitterMS)
		fmt.Printf("Download: %.2f %s\n", dlVal, dlUnit)
		fmt.Printf("Upload: %.2f %s\n", ulVal, ulUnit)
		return nil
	default:
		tui.printSummary(res)
		return nil
	}
}

func listServersCmd(ctx context.Context, eng *engine.Engine, provider engine.ProviderType) error {
	client, servers, err := eng.ListServers(ctx, provider)
	if err != nil {
		return err
	}
	if client.IP != "" {
		fmt.Printf("Detected Client: %s (%s)\n\n", client.ISP, client.IP)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPROVIDER\tSPONSOR\tLOCATION\tDISTANCE\tLATENCY")
	fmt.Fprintln(tw, "--\t--------\t-------\t--------\t--------\t-------")
	for _, s := range servers {
		lat := "—"
		if s.LatencyMS > 0 && s.LatencyMS < 9000 {
			lat = fmt.Sprintf("%.1f ms", s.LatencyMS)
		}
		dist := "—"
		if s.DistanceKM > 0 {
			dist = fmt.Sprintf("%.1f km", s.DistanceKM)
		}
		loc := s.City
		if s.Country != "" {
			loc = fmt.Sprintf("%s, %s", s.City, s.Country)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Provider, s.Sponsor, loc, dist, lat)
	}
	return tw.Flush()
}

func printHistory(store *history.Store, unit string) error {
	items, err := store.Load()
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("No saved speed test history found.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tTYPE\tPROVIDER\tSERVER\tPING\tJITTER\tDOWNLOAD\tUPLOAD")
	fmt.Fprintln(tw, "----\t----\t--------\t------\t----\t------\t--------\t------")
	limit := 30
	if len(items) < limit {
		limit = len(items)
	}
	for _, r := range items[:limit] {
		dlV, dlU := engine.FormatSpeed(r.DownloadMbps, unit)
		ulV, ulU := engine.FormatSpeed(r.UploadMbps, unit)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%.1f ms\t%.1f ms\t%.2f %s\t%.2f %s\n",
			r.Timestamp.Local().Format("2006-01-02 15:04"),
			r.Trigger,
			r.Provider,
			r.Server.Name,
			r.PingMS,
			r.JitterMS,
			dlV, dlU,
			ulV, ulU,
		)
	}
	return tw.Flush()
}
