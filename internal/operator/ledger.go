package operator

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CostEntry is one row in the append-only cost ledger.
type CostEntry struct {
	Timestamp   time.Time `json:"ts"`
	Repo        string    `json:"repo"`
	Issue       int       `json:"issue"`
	Job         string    `json:"job"`
	Agent       string    `json:"agent"`
	Family      string    `json:"family"`
	Model       string    `json:"model"`
	Input       int64     `json:"input"`
	Output      int64     `json:"output"`
	CacheCreate int64     `json:"cache_create"`
	CacheRead   int64     `json:"cache_read"`
	Total       int64     `json:"total"`
	CostUSD     float64   `json:"cost_usd"`
	DurationSec int       `json:"duration_sec"`
	ExitCode    int       `json:"exit_code"`
}

func ledgerPath() string {
	home := os.Getenv("HOME")
	if home == "" {
		home = os.Getenv("USERPROFILE")
	}
	return filepath.Join(home, ".k3sc-costs.jsonl")
}

// AppendCostEntry appends a single cost entry to the ledger file.
func AppendCostEntry(entry CostEntry) error {
	path := ledgerPath()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open ledger %s: %w", path, err)
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal cost entry: %w", err)
	}
	data = append(data, '\n')
	_, err = f.Write(data)
	return err
}

// ReadLedger reads all cost entries from the ledger file.
func ReadLedger() ([]CostEntry, error) {
	path := ledgerPath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []CostEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var e CostEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue // skip malformed lines
		}
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}
