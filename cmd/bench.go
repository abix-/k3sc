package cmd

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(benchCheckCmd)
	rootCmd.AddCommand(benchUpdateCmd)
}

// baseline JSON shape: {"key": {"mean_ns": 12345}, "_comment": "...", ...}
type baselineEntry struct {
	MeanNs float64 `json:"mean_ns"`
}

// Criterion estimates.json shape (nested)
type criterionEstimates struct {
	Mean struct {
		PointEstimate float64 `json:"point_estimate"`
	} `json:"mean"`
}

type benchResult struct {
	key     string
	baseNs  float64
	currNs  float64
	deltaPct float64
}

var benchCheckCmd = &cobra.Command{
	Use:   "bench-check",
	Short: "Check Criterion results against baseline for regressions",
	Long:  "Walks target/criterion/ for estimates.json, compares against ci-baseline.json, exits non-zero on regression.",
	RunE:  runBenchCheck,
}

var benchUpdateCmd = &cobra.Command{
	Use:   "bench-update",
	Short: "Update ci-baseline.json from latest Criterion run",
	Long:  "Reads target/criterion/ and overwrites ci-baseline.json with new mean times.",
	RunE:  runBenchUpdate,
}

func findRustDir() (string, error) {
	// look for rust/ subdir or Cargo.toml in cwd
	if info, err := os.Stat("rust/Cargo.toml"); err == nil && !info.IsDir() {
		return "rust", nil
	}
	if info, err := os.Stat("Cargo.toml"); err == nil && !info.IsDir() {
		return ".", nil
	}
	return "", fmt.Errorf("cannot find rust project (no rust/Cargo.toml or Cargo.toml in cwd)")
}

func loadBaseline(path string) (map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	result := make(map[string]float64)
	for k, v := range raw {
		if strings.HasPrefix(k, "_") {
			continue
		}
		var entry baselineEntry
		if err := json.Unmarshal(v, &entry); err != nil {
			continue
		}
		result[k] = entry.MeanNs
	}
	return result, nil
}

func loadBaselineRaw(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func collectCriterionResults(criterionDir string) (map[string]float64, error) {
	results := make(map[string]float64)
	err := filepath.Walk(criterionDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable dirs
		}
		if info.IsDir() || info.Name() != "estimates.json" {
			return nil
		}
		// path: .../criterion/<group>/<id>/new/estimates.json
		rel, _ := filepath.Rel(criterionDir, path)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		// expect: <group>/<id>/new/estimates.json -> 4 parts
		if len(parts) < 4 || parts[len(parts)-2] != "new" {
			return nil
		}
		group := parts[len(parts)-4]
		benchID := parts[len(parts)-3]
		key := group + "/" + benchID

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var est criterionEstimates
		if err := json.Unmarshal(data, &est); err != nil {
			return nil
		}
		results[key] = est.Mean.PointEstimate // nanoseconds (Criterion point_estimate is already ns)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func formatNs(ns float64) string {
	if ns >= 1_000_000 {
		return fmt.Sprintf("%.1fms", ns/1_000_000)
	}
	if ns >= 1_000 {
		return fmt.Sprintf("%.0fus", ns/1_000)
	}
	return fmt.Sprintf("%.0fns", ns)
}

func runBenchCheck(cmd *cobra.Command, args []string) error {
	threshold := 20.0
	if v := os.Getenv("BENCH_THRESHOLD"); v != "" {
		if t, err := strconv.ParseFloat(v, 64); err == nil {
			threshold = t
		}
	}
	warnOnly := os.Getenv("BENCH_WARN_ONLY") == "1"

	rustDir, err := findRustDir()
	if err != nil {
		return err
	}
	baselinePath := filepath.Join(rustDir, "benches", "ci-baseline.json")
	criterionDir := filepath.Join(rustDir, "target", "criterion")

	baseline, err := loadBaseline(baselinePath)
	if err != nil {
		return fmt.Errorf("load baseline: %w", err)
	}

	results, err := collectCriterionResults(criterionDir)
	if err != nil {
		return fmt.Errorf("collect criterion results: %w", err)
	}
	if len(results) == 0 {
		fmt.Printf("WARNING: no Criterion output found in %s\n", criterionDir)
		fmt.Println("  Run: cargo bench --bench system_bench")
		return nil
	}

	var regressions, improvements, passing []benchResult

	keys := make([]string, 0, len(baseline))
	for k := range baseline {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		baseNs := baseline[key]
		currNs, ok := results[key]
		if !ok {
			continue
		}
		if baseNs < 500 {
			continue
		}
		pct := (currNs - baseNs) / baseNs * 100.0
		r := benchResult{key: key, baseNs: baseNs, currNs: currNs, deltaPct: pct}
		if pct > threshold {
			regressions = append(regressions, r)
		} else if pct < -10 {
			improvements = append(improvements, r)
		} else {
			passing = append(passing, r)
		}
	}

	checked := len(regressions) + len(improvements) + len(passing)

	// build markdown summary
	var sb strings.Builder
	sb.WriteString("## Benchmark Regression Report\n\n")
	sb.WriteString(fmt.Sprintf("Threshold: %.0f%%  |  Benchmarks checked: %d\n", threshold, checked))

	writeTable := func(title string, items []benchResult) {
		sb.WriteString(fmt.Sprintf("\n### %s (%d)\n\n", title, len(items)))
		sb.WriteString("| Benchmark | Baseline | Current | Delta |\n")
		sb.WriteString("|-----------|----------|---------|-------|\n")
		for _, r := range items {
			sign := ""
			if r.deltaPct > 0 {
				sign = "+"
			}
			sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s%.1f%% |\n",
				r.key, formatNs(r.baseNs), formatNs(r.currNs), sign, r.deltaPct))
		}
	}

	if len(regressions) > 0 {
		writeTable("Regressions", regressions)
	}
	if len(improvements) > 0 {
		writeTable("Improvements", improvements)
	}
	if len(passing) > 0 {
		writeTable("Passing", passing)
	}

	summary := sb.String()

	// write to GitHub step summary if available
	if summaryFile := os.Getenv("GITHUB_STEP_SUMMARY"); summaryFile != "" {
		f, err := os.OpenFile(summaryFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			f.WriteString(summary)
			f.Close()
		}
	}

	fmt.Print(summary)

	if len(regressions) > 0 {
		fmt.Printf("\nFAIL: %d benchmark(s) regressed by >%.0f%%\n", len(regressions), threshold)
		for _, r := range regressions {
			fmt.Printf("  %s: %s -> %s (+%.1f%%)\n", r.key, formatNs(r.baseNs), formatNs(r.currNs), r.deltaPct)
		}
		fmt.Println()
		fmt.Println("To update the baseline after an intentional perf change:")
		fmt.Println("  See docs/bench-guardrails.md")
		if warnOnly {
			fmt.Println("(warn-only mode: not failing CI)")
			return nil
		}
		os.Exit(1)
	}

	if checked == 0 {
		fmt.Println("No baseline entries matched current results -- skipping regression check")
		return nil
	}

	fmt.Printf("OK: all %d benchmarks within %.0f%% threshold\n", checked, threshold)
	return nil
}

func runBenchUpdate(cmd *cobra.Command, args []string) error {
	rustDir, err := findRustDir()
	if err != nil {
		return err
	}
	baselinePath := filepath.Join(rustDir, "benches", "ci-baseline.json")
	criterionDir := filepath.Join(rustDir, "target", "criterion")

	raw, err := loadBaselineRaw(baselinePath)
	if err != nil {
		return fmt.Errorf("load baseline: %w", err)
	}

	results, err := collectCriterionResults(criterionDir)
	if err != nil {
		return fmt.Errorf("collect criterion results: %w", err)
	}
	if len(results) == 0 {
		return fmt.Errorf("no Criterion output in %s -- run: cargo bench --bench system_bench", criterionDir)
	}

	updated := 0
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if strings.HasPrefix(key, "_") {
			continue
		}
		newNs, ok := results[key]
		if !ok {
			fmt.Printf("  SKIP (no result): %s\n", key)
			continue
		}
		var entry baselineEntry
		if err := json.Unmarshal(raw[key], &entry); err != nil {
			continue
		}
		oldNs := entry.MeanNs
		pct := 0.0
		if oldNs > 0 {
			pct = (newNs - oldNs) / oldNs * 100.0
		}
		roundedNs := math.Round(newNs)
		entry.MeanNs = roundedNs
		b, _ := json.Marshal(entry)
		raw[key] = b

		sign := ""
		if pct > 0 {
			sign = "+"
		}
		fmt.Printf("  %s: %.0f -> %.0f ns (%s%.1f%%)\n", key, oldNs, roundedNs, sign, pct)
		updated++
	}

	// update timestamp
	dateStr, _ := json.Marshal(time.Now().Format("2006-01-02"))
	raw["_updated"] = dateStr

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal baseline: %w", err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(baselinePath, out, 0o644); err != nil {
		return fmt.Errorf("write baseline: %w", err)
	}

	fmt.Printf("\nUpdated %d entries in %s\n", updated, baselinePath)
	fmt.Println("Commit the updated ci-baseline.json with the new date/commit reference.")
	return nil
}
