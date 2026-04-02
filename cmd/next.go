package cmd

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/abix-/k3sc/internal/github"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(nextCmd)
}

var nextCmd = &cobra.Command{
	Use:   "next",
	Short: "Pick a random issue or PR that needs human review",
	RunE:  runNext,
}

func runNext(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	issues, err := github.GetAllOpenIssues(ctx)
	if err != nil {
		return err
	}

	prs, err := github.GetOpenPRs(ctx)
	if err != nil {
		return err
	}

	type item struct {
		kind string
		repo string
		num  int
		title string
		url  string
	}

	var candidates []item
	for _, i := range issues {
		if i.State == "needs-human" {
			candidates = append(candidates, item{
				kind:  i.State,
				repo:  i.Repo.Name,
				num:   i.Number,
				title: i.Title,
				url:   fmt.Sprintf("%s/%s/%s/issues/%d", strings.TrimRight(types.GitHubURL, "/"), i.Repo.Owner, i.Repo.Name, i.Number),
			})
		}
	}
	for _, pr := range prs {
		candidates = append(candidates, item{
			kind:  "pr",
			repo:  pr.Repo.Name,
			num:   pr.Number,
			title: pr.Title,
			url:   fmt.Sprintf("%s/%s/%s/pull/%d", strings.TrimRight(types.GitHubURL, "/"), pr.Repo.Owner, pr.Repo.Name, pr.Number),
		})
	}

	if len(candidates) == 0 {
		fmt.Println("nothing to review")
		return nil
	}

	pick := candidates[rand.Intn(len(candidates))]
	fmt.Printf("[%s] %s#%d: %s\n%s\n", pick.kind, pick.repo, pick.num, pick.title, pick.url)
	return nil
}
