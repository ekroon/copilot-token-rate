package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const aggregationQuery = `
SELECT
  model,
  COALESCE(output_tokens, 0) AS output_tokens,
  COALESCE(input_tokens, 0) AS input_tokens,
  COALESCE(cache_read_tokens, 0) AS cache_read_tokens,
  COALESCE(cache_write_tokens, 0) AS cache_write_tokens,
  COALESCE(reasoning_tokens, 0) AS reasoning_tokens,
  COALESCE(duration_ms, 0) AS duration_ms
FROM assistant_usage_events
WHERE datetime(created_at) >= datetime(:cutoff)
ORDER BY model;
`

type config struct {
	dbPath   string
	window   time.Duration
	interval time.Duration
	watch    bool
	json     bool
	once     bool
}

type usageEvent struct {
	Model            string `json:"model"`
	OutputTokens     int64  `json:"output_tokens"`
	InputTokens      int64  `json:"input_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	ReasoningTokens  int64  `json:"reasoning_tokens"`
	DurationMs       int64  `json:"duration_ms"`
}

type tokenRate struct {
	Model           string  `json:"model"`
	OutputTokens    int64   `json:"output_tokens"`
	DurationMs      int64   `json:"duration_ms"`
	TokensPerSecond float64 `json:"tokens_per_second"`
	Requests        int64   `json:"requests"`
}

type snapshot struct {
	GeneratedAt string      `json:"generated_at"`
	Cutoff      string      `json:"cutoff"`
	Models      []tokenRate `json:"models"`
}

func main() {
	cfg, err := parseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	if err := run(cfg, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags(args []string, stderr io.Writer) (config, error) {
	defaultDB, err := defaultDatabasePath()
	if err != nil {
		return config{}, err
	}

	cfg := config{dbPath: defaultDB, window: time.Minute, interval: 10 * time.Second}
	flags := flag.NewFlagSet("copilot-token-rate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: copilot-token-rate [flags]")
		fmt.Fprintln(stderr, "\nReports Copilot output-token throughput by model.")
		flags.PrintDefaults()
	}
	flags.BoolVar(&cfg.watch, "watch", false, "refresh repeatedly at --interval")
	flags.BoolVar(&cfg.once, "once", false, "print one snapshot and exit (default)")
	flags.DurationVar(&cfg.window, "window", time.Minute, "trailing event window")
	flags.DurationVar(&cfg.interval, "interval", 10*time.Second, "watch refresh interval")
	flags.StringVar(&cfg.dbPath, "db", defaultDB, "path to the Copilot SQLite session store")
	flags.BoolVar(&cfg.json, "json", false, "write machine-readable JSON")

	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if cfg.window <= 0 {
		return config{}, errors.New("--window must be greater than zero")
	}
	if cfg.interval <= 0 {
		return config{}, errors.New("--interval must be greater than zero")
	}
	if cfg.watch && cfg.once {
		return config{}, errors.New("--watch and --once cannot be used together")
	}
	cfg.dbPath = expandPath(cfg.dbPath)
	return cfg, nil
}

func run(cfg config, stdout, stderr io.Writer) error {
	if cfg.watch {
		fmt.Fprintf(stderr, "Watching the last %s; refreshing every %s (use --once for a single snapshot)\n", cfg.window, cfg.interval)
		for {
			if err := printSnapshot(cfg, stdout, time.Now().UTC()); err != nil {
				return err
			}
			time.Sleep(cfg.interval)
		}
	}
	return printSnapshot(cfg, stdout, time.Now().UTC())
}

func printSnapshot(cfg config, stdout io.Writer, now time.Time) error {
	cutoff := queryCutoff(now, cfg.window)
	events, err := readEvents(cfg.dbPath, cutoff)
	if err != nil {
		return err
	}
	s := snapshot{
		GeneratedAt: now.UTC().Format(time.RFC3339Nano),
		Cutoff:      cutoff.Format(time.RFC3339Nano),
		Models:      aggregateEvents(events),
	}
	return renderSnapshot(stdout, s, cfg.json)
}

func readEvents(dbPath string, cutoff time.Time) ([]usageEvent, error) {
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("database not found at %s", dbPath)
		}
		return nil, fmt.Errorf("stat database %s: %w", dbPath, err)
	}
	sqlite3, err := exec.LookPath("sqlite3")
	if err != nil {
		return nil, errors.New("sqlite3 executable not found in PATH; install sqlite3 or add it to PATH")
	}

	cutoffValue := cutoff.UTC().Format(time.RFC3339Nano)
	cmd := exec.Command(sqlite3,
		"-readonly",
		"-json",
		"-cmd", ".parameter init",
		"-cmd", ".parameter set :cutoff "+sqliteString(cutoffValue),
		dbPath,
		aggregationQuery,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("querying database: %s", detail)
	}

	var events []usageEvent
	if err := json.Unmarshal(stdout.Bytes(), &events); err != nil {
		return nil, fmt.Errorf("decoding sqlite3 output: %w", err)
	}
	return events, nil
}

func aggregateEvents(events []usageEvent) []tokenRate {
	byModel := make(map[string]*tokenRate)
	for _, event := range events {
		rate := byModel[event.Model]
		if rate == nil {
			rate = &tokenRate{Model: event.Model}
			byModel[event.Model] = rate
		}
		rate.OutputTokens += event.OutputTokens
		rate.DurationMs += event.DurationMs
		rate.Requests++
	}

	result := make([]tokenRate, 0, len(byModel))
	for _, rate := range byModel {
		if rate.DurationMs > 0 {
			rate.TokensPerSecond = float64(rate.OutputTokens) / (float64(rate.DurationMs) / 1000)
		}
		result = append(result, *rate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Model < result[j].Model })
	return result
}

func renderSnapshot(w io.Writer, s snapshot, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(s)
	}

	fmt.Fprintf(w, "Copilot output-token rate (since %s)\n", s.Cutoff)
	fmt.Fprintln(w, "MODEL\tOUTPUT TOKENS\tDURATION\tTOKENS/SEC\tREQUESTS")
	if len(s.Models) == 0 {
		fmt.Fprintln(w, "(no usage events)")
		return nil
	}
	for _, rate := range s.Models {
		fmt.Fprintf(w, "%s\t%d\t%s\t%.2f\t%d\n",
			rate.Model,
			rate.OutputTokens,
			formatMilliseconds(rate.DurationMs),
			rate.TokensPerSecond,
			rate.Requests,
		)
	}
	return nil
}

func queryCutoff(now time.Time, window time.Duration) time.Time {
	return now.UTC().Add(-window)
}

func defaultDatabasePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".copilot", "session-store.db"), nil
}

func expandPath(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func sqliteString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func formatMilliseconds(milliseconds int64) string {
	return (time.Duration(milliseconds) * time.Millisecond).String()
}
