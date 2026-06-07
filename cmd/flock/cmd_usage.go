package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"time"
)

// cmdUsage prints the recent usage records.
//
//	flock usage [--limit=N] [--user=X]
func cmdUsage(args []string) {
	fs := flag.NewFlagSet("usage", flag.ExitOnError)
	limit := fs.Int("limit", 50, "maximum number of rows to show")
	user := fs.String("user", "", "filter to a specific user_id (client-side)")
	fs.Usage = func() {
		showHelp(helpSpec{
			name:    "usage",
			summary: "show recent inference usage records",
			usage:   "flock usage [--limit N] [--user X]",
			flags:   fs,
			examples: []string{
				"flock usage                 # latest 50 records",
				"flock usage --limit 200     # latest 200",
				"flock usage --user alice    # filter by user",
			},
		})
	}
	if wantsHelp(args) {
		fs.Usage()
	}
	_ = fs.Parse(args)

	cfg := loadConfigOrExit()
	body, err := adminCall(context.Background(), cfg, "GET", "/admin/v1/usage/recent", nil)
	if err != nil {
		die("%v: %s", err, string(body))
	}
	var rows []map[string]any
	_ = json.Unmarshal(body, &rows)
	if len(rows) == 0 {
		fmt.Println("(no usage records yet)")
		return
	}

	fmt.Printf("%-19s %-14s %-22s %-12s %5s %5s %7s %s\n",
		"TIME", "USER/KEY", "MODEL", "PROTOCOL", "PROMPT", "COMPL", "MS", "OUTCOME")
	count := 0
	for _, r := range rows {
		if *user != "" && fmt.Sprint(r["UserID"]) != *user {
			continue
		}
		ts := parseTime(r["TS"])
		fmt.Printf("%-19s %-14s %-22s %-12s %5v %5v %7v %s\n",
			ts.Format("2006-01-02 15:04:05"),
			truncStr(fmt.Sprint(firstNonEmpty(r["UserID"], r["APIKeyID"])), 14),
			truncStr(fmt.Sprint(r["Model"]), 22),
			fmt.Sprint(r["Protocol"]),
			r["PromptTokens"], r["CompletionTokens"], r["LatencyMS"],
			r["Outcome"])
		count++
		if count >= *limit {
			break
		}
	}
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
