package cmd

import (
	"fmt"

	"github.com/abix-/k3sc/internal/github"
	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
)

var releaseRepo string
var releasePR int

func init() {
	releaseCmd.Flags().StringVar(&releaseRepo, "repo", "endless", "repo name (endless or k3sc)")
	releaseCmd.Flags().IntVar(&releasePR, "pr", 0, "pull request number to release")
	rootCmd.AddCommand(releaseCmd)
}

var releaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Release a locally reserved PR review assignment",
	RunE:  runRelease,
}

func runRelease(cmd *cobra.Command, args []string) error {
	if releasePR <= 0 {
		return fmt.Errorf("--pr is required")
	}

	ctx := cmd.Context()
	repo := types.RepoByName(releaseRepo)
	found, err := k8s.DeleteReviewLease(ctx, repo, releasePR)
	if err != nil {
		return err
	}
	if err := github.UnclaimPullRequest(ctx, repo, releasePR); err != nil {
		return err
	}
	_, _ = k8s.TriggerDispatch(ctx)

	if found {
		cmd.Printf("released %s PR #%d\n", repo.Name, releasePR)
	} else {
		cmd.Printf("cleared owner label for %s PR #%d (no active lease)\n", repo.Name, releasePR)
	}
	return nil
}
