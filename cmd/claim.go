package cmd

import (
	"fmt"
	"strconv"

	"github.com/abix-/k3sc/internal/github"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
)

var claimRepo string
var claimOwner string

func init() {
	claimCmd.Flags().StringVar(&claimRepo, "repo", "endless", "repo name (endless or k3sc)")
	claimCmd.Flags().StringVar(&claimOwner, "owner", "human", "owner label to apply")
	rootCmd.AddCommand(claimCmd)
}

var claimCmd = &cobra.Command{
	Use:   "claim <issue>",
	Short: "Claim a GitHub issue or PR",
	Args:  cobra.ExactArgs(1),
	RunE:  runClaim,
}

func runClaim(cmd *cobra.Command, args []string) error {
	issue, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("invalid issue number: %s", args[0])
	}

	ctx := cmd.Context()
	repo := types.RepoByName(claimRepo)

	owner, err := github.GetIssueOwner(ctx, repo, issue)
	if err != nil {
		return err
	}
	if owner != "" {
		return fmt.Errorf("issue %d already claimed by %s", issue, owner)
	}

	if err := github.ClaimIssue(ctx, repo, issue, claimOwner); err != nil {
		return err
	}
	fmt.Printf("issue %d: claimed by %s\n", issue, claimOwner)
	return nil
}
