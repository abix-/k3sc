package cmd

import (
	"context"
	"fmt"

	"github.com/abix-/k3s-claude/internal/k8s"
	"github.com/abix-/k3s-claude/internal/types"
	"github.com/spf13/cobra"
)

var follow bool

func init() {
	logsCmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	rootCmd.AddCommand(logsCmd)
}

var logsCmd = &cobra.Command{
	Use:   "logs [issue-number]",
	Short: "View agent pod logs",
	RunE:  runLogs,
}

func runLogs(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	cs, err := k8s.NewClient()
	if err != nil {
		return err
	}

	if len(args) > 0 {
		var issue int
		fmt.Sscanf(args[0], "%d", &issue)
		if issue == 0 {
			return fmt.Errorf("invalid issue number: %s", args[0])
		}

		podName, err := k8s.FindPodForIssue(ctx, cs, issue)
		if err != nil {
			return err
		}
		if podName == "" {
			fmt.Printf("No pods found for issue #%d\n", issue)
			return nil
		}

		if follow {
			fmt.Printf("Following logs for issue #%d (pod %s)...\n", issue, podName)
			return k8s.FollowLog(ctx, cs, podName)
		}

		log, err := k8s.GetFullLog(ctx, cs, podName)
		if err != nil {
			return err
		}
		fmt.Print(log)
		return nil
	}

	// summary
	pods, err := k8s.GetAgentPods(ctx, cs)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		fmt.Println("No agent pods found.")
		return nil
	}

	fmt.Printf("%-7s %-10s %-11s %-16s Last Output\n", "Issue", "Agent", "Status", "Started")
	for _, pod := range pods {
		agent := types.AgentName(pod.Slot)
		tail, _ := k8s.GetPodLogTail(ctx, cs, pod.Name, 20)
		fmt.Printf("#%-6d %-10s %-11s %-16s %s\n",
			pod.Issue, agent, pod.Phase.Display(),
			fmtTime(pod.Started), tail)
	}
	return nil
}
