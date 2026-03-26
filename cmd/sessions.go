package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/abix-/k3sc/internal/claude"
	"github.com/abix-/k3sc/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

var (
	sessionsOnce bool
	sessionsJSON bool
)

func init() {
	sessionsCmd.Flags().BoolVar(&sessionsOnce, "once", false, "Print once and exit (no TUI)")
	sessionsCmd.Flags().BoolVar(&sessionsJSON, "json", false, "Print JSON and exit")
	rootCmd.AddCommand(sessionsCmd)
}

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "Live dashboard of running Claude Code sessions and usage",
	RunE:  runSessions,
}

func runSessions(cmd *cobra.Command, args []string) error {
	snapshot, err := claude.CollectSessions()
	if err != nil {
		return err
	}

	if sessionsJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snapshot)
	}
	if sessionsOnce {
		printSessions(snapshot)
		return nil
	}

	gatherFn := func() (*claude.Snapshot, error) {
		return claude.CollectSessions()
	}

	model := tui.NewSessionsModel(snapshot, gatherFn)
	program := tea.NewProgram(model, tea.WithAltScreen())
	_, err = program.Run()
	return err
}

func printSessions(snapshot *claude.Snapshot) {
	fmt.Println("=== CLAUDE SESSIONS ===")
	fmt.Printf(
		"Sessions: %d | Metadata: %d | Usage: %d | Cost: $%.4f | Tokens: total=%s cached=%s output=%s\n\n",
		snapshot.SessionCount,
		snapshot.MetadataCount,
		snapshot.UsageCount,
		snapshot.TotalCostUSD,
		withCommas(snapshot.TotalTokens),
		withCommas(snapshot.CachedTokens),
		withCommas(snapshot.OutputTokens),
	)

	fmt.Println("=== ACTIVE BLOCK ===")
	switch {
	case snapshot.ActiveBlock != nil:
		block := snapshot.ActiveBlock
		fmt.Printf("Started : %s\n", sessionTimeString(block.StartTime))
		fmt.Printf("Ends    : %s\n", sessionTimeString(block.EndTime))
		fmt.Printf("Remain  : %s\n", minutesLabel(block.RemainingMinutes))
		fmt.Printf(
			"Usage   : total=%s input=%s output=%s cache-create=%s cache-read=%s cost=%s\n",
			withCommas(block.TotalTokens),
			withCommas(block.InputTokens),
			withCommas(block.OutputTokens),
			withCommas(block.CacheCreationTokens),
			withCommas(block.CacheReadTokens),
			fmt.Sprintf("$%.4f", block.CostUSD),
		)
		fmt.Printf(
			"Burn    : %.0f tok/min | %s/hr | projected total=%s cost=%s\n",
			block.TokensPerMinute,
			fmt.Sprintf("$%.4f", block.CostPerHour),
			withCommas(block.ProjectedTokens),
			fmt.Sprintf("$%.4f", block.ProjectedCostUSD),
		)
		if block.TokenLimit > 0 {
			fmt.Printf(
				"Max     : limit=%s current=%.1f%% left=%s projected=%.1f%% projected-left=%s status=%s\n",
				withCommas(block.TokenLimit),
				block.CurrentPercentUsed,
				withCommas(block.CurrentRemainingTokens),
				block.ProjectedPercentUsed,
				withCommas(block.ProjectedRemainingTokens),
				valueOr(block.TokenLimitStatus, "unknown"),
			)
		}
		if len(block.Models) > 0 {
			fmt.Printf("Models  : %s\n", strings.Join(block.Models, ", "))
		}
	case snapshot.BlockError != "":
		fmt.Printf("Error   : %s\n", snapshot.BlockError)
	default:
		fmt.Println("No active block data.")
	}
	fmt.Println()

	for idx, session := range snapshot.Sessions {
		fmt.Printf("[%d] PID %d | Cost %s | Total %s | Cached %s | Output %s | Last %s\n",
			idx+1,
			session.PID,
			fmt.Sprintf("$%.4f", session.TotalCostUSD),
			withCommas(session.TotalTokens),
			withCommas(session.CachedTokens),
			withCommas(session.OutputTokens),
			sessionTimeString(session.LastActivity),
		)
		fmt.Printf("    Session : %s\n", valueOr(session.SessionID, "<missing session ID>"))
		fmt.Printf("    CWD     : %s\n", valueOr(session.CWD, "<missing cwd>"))
		fmt.Printf("    Started : %s\n", sessionTimeString(session.StartedAt))
		if len(session.Models) > 0 {
			fmt.Printf("    Models  : %s\n", strings.Join(session.Models, ", "))
		}
		if session.Name != "" {
			fmt.Printf("    Name    : %s\n", session.Name)
		}
		if session.MetadataError != "" {
			fmt.Printf("    Metadata: %s\n", session.MetadataError)
		}
		if session.UsageError != "" {
			fmt.Printf("    Usage   : %s\n", session.UsageError)
		}
		fmt.Println()
	}
}

func withCommas(n int64) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return sign + s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return sign + strings.Join(parts, ",")
}

func sessionTimeString(t *time.Time) string {
	if t == nil {
		return "unknown"
	}
	return t.Local().Format("2006-01-02 03:04:05 PM")
}

func minutesLabel(minutes int) string {
	if minutes <= 0 {
		return "0m"
	}
	hours := minutes / 60
	remaining := minutes % 60
	if hours == 0 {
		return fmt.Sprintf("%dm", remaining)
	}
	return fmt.Sprintf("%dh %dm", hours, remaining)
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
