package cmd

import (
	"context"
	"fmt"
	"strconv"

	"github.com/abix-/k3sc/internal/github"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
)

var resetTo string
var resetRepo string

func init() {
	resetCmd.Flags().StringVar(&resetTo, "to", "ready", "target label after reset")
	resetCmd.Flags().StringVar(&resetRepo, "repo", "endless", "repo name (endless or k3sc)")
	rootCmd.AddCommand(resetCmd)
}

var resetCmd = &cobra.Command{
	Use:   "reset <issue>",
	Short: "Remove owner claim from a GitHub issue",
	Args:  cobra.ExactArgs(1),
	RunE:  runReset,
}

func runReset(cmd *cobra.Command, args []string) error {
	issue, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("invalid issue number: %s", args[0])
	}
	return resetIssueLabels(cmd.Context(), resetRepo, issue, resetTo)
}

// resetIssueLabels removes the owner claim from an issue and sets the target label.
// Shared by both reset and kill commands.
func resetIssueLabels(ctx context.Context, repoName string, issue int, targetLabel string) error {
	repo := types.RepoByName(repoName)
	owner, err := github.GetIssueOwner(ctx, repo, issue)
	if err != nil {
		return err
	}
	if owner == "" {
		fmt.Printf("issue %d is not claimed\n", issue)
		return nil
	}
	if err := github.UnclaimIssue(ctx, repo, issue, owner, targetLabel); err != nil {
		return err
	}
	fmt.Printf("issue %d: %s -> %s\n", issue, owner, targetLabel)
	return nil
}
