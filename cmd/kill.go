package cmd

import (
	"fmt"
	"strconv"

	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var killRepo string

func init() {
	killCmd.Flags().StringVar(&killRepo, "repo", "endless", "repo name (endless or k3sc)")
	rootCmd.AddCommand(killCmd)
}

var killCmd = &cobra.Command{
	Use:   "kill <issue>",
	Short: "Kill running agent job and reset GitHub claim",
	Args:  cobra.ExactArgs(1),
	RunE:  runKill,
}

func runKill(cmd *cobra.Command, args []string) error {
	issue, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("invalid issue number: %s", args[0])
	}

	ctx := cmd.Context()

	// delete k8s job
	cs, err := k8s.NewClient()
	if err != nil {
		return err
	}

	jobs, err := cs.BatchV1().Jobs(types.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=claude-agent,issue-number=%d", issue),
	})
	if err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}

	bg := metav1.DeletePropagationBackground
	deleted := 0
	for _, j := range jobs.Items {
		if j.Status.Active > 0 {
			if err := cs.BatchV1().Jobs(types.Namespace).Delete(ctx, j.Name, metav1.DeleteOptions{
				PropagationPolicy: &bg,
			}); err != nil {
				return fmt.Errorf("delete job %s: %w", j.Name, err)
			}
			fmt.Printf("deleted job %s\n", j.Name)
			deleted++
		}
	}
	if deleted == 0 {
		fmt.Printf("no active jobs for issue %d\n", issue)
	}

	// reset github labels
	return resetIssueLabels(ctx, killRepo, issue, "ready")
}
