package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"time"
)

// cmdUsage prints the recent usage records.
//
//	flock usage [--limit=N] [--user=X]
func cmdUsage(args []string) {
	args, asJSON := extractJSONFlag(args)
	fs := flag.NewFlagSet("usage", flag.ExitOnError)
	limit := fs.Int("limit", 50, "maximum number of rows to show")
	user := fs.String("user", "", "filter to a specific user_id (client-side)")
	summary := fs.Bool("summary", false, "show aggregate stats (total, top models, p50/p95, error rate) instead of rows")
	fs.Usage = func() {
		showHelp(helpSpec{
			name:    "usage",
			summary: "show recent inference usage records",
			usage:   "flock usage [--limit N] [--user X] [--summary] [--json]",
			flags:   fs,
			examples: []string{
				"flock usage                 # latest 50 records",
				"flock usage --limit 200     # latest 200",
				"flock usage --user alice    # filter by user",
				"flock usage --summary       # aggregate view (top models, p50/p95, error rate)",
				"flock usage --summary --json",
				"flock usage --json          # machine-readable rows",
			},
		})
	}
	if wantsHelp(args) {
		fs.Usage()
	}
	_ = fs.Parse(args)

	cfg := loadConfigOrExit()

	if *summary {
		body, err := adminCall(context.Background(), cfg, "GET", "/admin/v1/usage/summary", nil)
		if err != nil {
			die("%v: %s", err, string(body))
		}
		if asJSON {
			fmt.Println(string(body))
			return
		}
		renderUsageSummary(body)
		return
	}

	body, err := adminCall(context.Background(), cfg, "GET", "/admin/v1/usage/recent", nil)
	if err != nil {
		die("%v: %s", err, string(body))
	}
	var rows []map[string]any
	_ = json.Unmarshal(body, &rows)

	// Apply --user filter + --limit up-front so JSON and table modes
	// both honor them.
	filtered := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		if *user != "" && fmt.Sprint(r["UserID"]) != *user {
			continue
		}
		filtered = append(filtered, r)
		if len(filtered) >= *limit {
			break
		}
	}

	if asJSON {
		emitJSON(filtered)
		return
	}
	if len(filtered) == 0 {
		fmt.Println("(no usage records yet)")
		return
	}

	fmt.Printf("%s %s %s %s %s %s %s %s\n",
		bold(fmt.Sprintf("%-19s", "TIME")),
		bold(fmt.Sprintf("%-14s", "USER/KEY")),
		bold(fmt.Sprintf("%-22s", "MODEL")),
		bold(fmt.Sprintf("%-12s", "PROTOCOL")),
		bold(fmt.Sprintf("%5s", "PROMPT")),
		bold(fmt.Sprintf("%5s", "COMPL")),
		bold(fmt.Sprintf("%7s", "MS")),
		bold("OUTCOME"))
	for _, r := range filtered {
		ts := parseTime(r["TS"])
		outcome := fmt.Sprint(r["Outcome"])
		coloredOutcome := outcome
		switch strings.ToLower(outcome) {
		case "ok", "success", "completed":
			coloredOutcome = green(outcome)
		case "error", "failed", "timeout", "cancelled":
			coloredOutcome = red(outcome)
		}
		fmt.Printf("%s %-14s %-22s %-12s %5v %5v %7v %s\n",
			dim(ts.Format("2006-01-02 15:04:05")),
			truncStr(fmt.Sprint(firstNonEmpty(r["UserID"], r["APIKeyID"])), 14),
			truncStr(fmt.Sprint(r["Model"]), 22),
			fmt.Sprint(r["Protocol"]),
			r["PromptTokens"], r["CompletionTokens"], r["LatencyMS"],
			coloredOutcome)
	}
}

// renderUsageSummary pretty-prints the /admin/v1/usage/summary JSON for
// a terminal. Mirrors the dashboard's home view in plain text.
func renderUsageSummary(body []byte) {
	var s struct {
		Total       int     `json:"total"`
		TokensTotal int64   `json:"tokens_total"`
		ErrorRate   float64 `json:"error_rate"`
		P50MS       int     `json:"p50_ms"`
		P95MS       int     `json:"p95_ms"`
		P99MS       int     `json:"p99_ms"`
		TopModels   []struct {
			Model string `json:"model"`
			Count int    `json:"count"`
		} `json:"top_models"`
		RPM60Min []int `json:"rpm_60min"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		die("decode summary: %v", err)
	}
	fmt.Printf("%s\n", bold("Usage summary (last 1000 requests)"))
	if s.Total == 0 {
		fmt.Println(dim("  (no requests recorded yet)"))
		return
	}
	fmt.Printf("  %s  %d   %s  %s\n",
		bold("Total requests"), s.Total,
		bold("Tokens served"), fmt.Sprintf("%d", s.TokensTotal))
	errColor := green
	if s.ErrorRate > 0.05 {
		errColor = red
	} else if s.ErrorRate > 0 {
		errColor = yellow
	}
	fmt.Printf("  %s  p50=%dms  p95=%dms  p99=%dms   %s  %s\n",
		bold("Latency"),
		s.P50MS, s.P95MS, s.P99MS,
		bold("Error rate"),
		errColor(fmt.Sprintf("%.1f%%", s.ErrorRate*100)))
	if len(s.TopModels) > 0 {
		fmt.Printf("  %s\n", bold("Top models"))
		for _, m := range s.TopModels {
			fmt.Printf("    %s  %s\n", padCyan(m.Model, 24), dim(fmt.Sprintf("%d requests", m.Count)))
		}
	}
	if len(s.RPM60Min) == 60 {
		fmt.Printf("  %s  %s\n", bold("Last 60 min"), sparkline(s.RPM60Min))
	}
}

// sparkline renders a series of ints as a compact bar-chart row using
// the unicode block ramp.
func sparkline(vals []int) string {
	if len(vals) == 0 {
		return ""
	}
	max := 0
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	if max == 0 {
		return strings.Repeat("·", len(vals))
	}
	bars := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, v := range vals {
		idx := int(float64(v) / float64(max) * float64(len(bars)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(bars) {
			idx = len(bars) - 1
		}
		b.WriteRune(bars[idx])
	}
	return cyan(b.String())
}

func parseTime(v any) time.Time {
	s, _ := v.(string)
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func firstNonEmpty(vals ...any) any {
	for _, v := range vals {
		s := fmt.Sprint(v)
		if s != "" && s != "<nil>" {
			return v
		}
	}
	return ""
}
