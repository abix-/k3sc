package operator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const maxCodexSessionFiles = 64

type codexSessionFile struct {
	Path    string
	ModTime time.Time
}

type codexSessionEvent struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type       string           `json:"type"`
		RateLimits *codexRateLimits `json:"rate_limits"`
	} `json:"payload"`
}

type codexRateLimits struct {
	Primary   *codexRateWindow `json:"primary"`
	Secondary *codexRateWindow `json:"secondary"`
	Credits   *codexCredits    `json:"credits"`
	PlanType  *string          `json:"plan_type"`
}

type codexRateWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

type codexCredits struct {
	HasCredits bool     `json:"has_credits"`
	Unlimited  bool     `json:"unlimited"`
	Balance    *float64 `json:"balance"`
}

type codexSessionSnapshot struct {
	Timestamp  time.Time
	Path       string
	RateLimits codexRateLimits
}

func probeCodexStatus(ctx context.Context) (*codexStatusSnapshot, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return loadCodexStatusFromSessions(ctx, time.Now(), filepath.Join(home, ".codex", "sessions"))
}

func loadCodexStatusFromSessions(ctx context.Context, now time.Time, root string) (*codexStatusSnapshot, error) {
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no shared codex session data yet")
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("codex session path is not a directory: %s", root)
	}

	files, err := collectCodexSessionFiles(root)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no shared codex session data yet")
	}

	limit := len(files)
	if limit > maxCodexSessionFiles {
		limit = maxCodexSessionFiles
	}

	var latest *codexSessionSnapshot
	var firstErr error
	for _, file := range files[:limit] {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		snapshot, err := latestCodexRateLimitInFile(file.Path)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if snapshot == nil {
			continue
		}
		if latest == nil || snapshot.Timestamp.After(latest.Timestamp) {
			latest = snapshot
		}
	}

	if latest == nil {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("no codex rate limit events found in shared session data")
	}

	return codexStatusFromSessionSnapshot(now, latest), nil
}

func collectCodexSessionFiles(root string) ([]codexSessionFile, error) {
	var files []codexSessionFile
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, codexSessionFile{
			Path:    path,
			ModTime: info.ModTime(),
		})
		return nil
	}); err != nil {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime.After(files[j].ModTime)
	})
	return files, nil
}

func latestCodexRateLimitInFile(path string) (*codexSessionSnapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 128*1024), 16*1024*1024)

	var latest *codexSessionSnapshot
	for scanner.Scan() {
		var event codexSessionEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Type != "event_msg" || event.Payload.Type != "token_count" || event.Payload.RateLimits == nil {
			continue
		}

		ts, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err != nil {
			continue
		}

		snapshot := &codexSessionSnapshot{
			Timestamp:  ts,
			Path:       path,
			RateLimits: *event.Payload.RateLimits,
		}
		if latest == nil || snapshot.Timestamp.After(latest.Timestamp) {
			latest = snapshot
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return latest, nil
}

func codexStatusFromSessionSnapshot(now time.Time, snapshot *codexSessionSnapshot) *codexStatusSnapshot {
	if snapshot == nil {
		return nil
	}

	status := &codexStatusSnapshot{
		LastUpdated: snapshot.Timestamp,
		Raw:         snapshot.Path,
	}
	if credits := snapshot.RateLimits.Credits; credits != nil && credits.Balance != nil {
		balance := *credits.Balance
		status.Credits = &balance
	}

	assignCodexWindow(now, status, snapshot.RateLimits.Primary)
	assignCodexWindow(now, status, snapshot.RateLimits.Secondary)
	return status
}

func assignCodexWindow(now time.Time, status *codexStatusSnapshot, window *codexRateWindow) {
	if status == nil || window == nil {
		return
	}

	left, reset := codexWindowStatus(now, window)
	switch window.WindowMinutes {
	case 300:
		status.FiveHourPercentLeft = left
		status.FiveHourReset = reset
	case 10080:
		status.WeeklyPercentLeft = left
		status.WeeklyReset = reset
	default:
		if status.FiveHourPercentLeft == nil {
			status.FiveHourPercentLeft = left
			status.FiveHourReset = reset
			return
		}
		if status.WeeklyPercentLeft == nil {
			status.WeeklyPercentLeft = left
			status.WeeklyReset = reset
		}
	}
}

func codexWindowStatus(now time.Time, window *codexRateWindow) (*int, string) {
	if window == nil {
		return nil, ""
	}

	if window.ResetsAt > 0 {
		resetAt := time.Unix(window.ResetsAt, 0).UTC()
		if !resetAt.After(now.UTC()) {
			full := 100
			return &full, ""
		}
		left := clampPercent(100 - int(window.UsedPercent+0.5))
		return &left, fmt.Sprintf("resets %s", resetAt.Local().Format(time.RFC822))
	}

	left := clampPercent(100 - int(window.UsedPercent+0.5))
	return &left, ""
}

func clampPercent(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
