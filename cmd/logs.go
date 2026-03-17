package cmd

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
)

var follow bool

func init() {
	logsCmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	rootCmd.AddCommand(logsCmd)
}

var logsCmd = &cobra.Command{
	Use:   "logs [repo] [issue-number]",
	Short: "View agent pod logs",
	RunE:  runLogs,
}

func runLogs(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	cs, err := k8s.NewClient()
	if err != nil {
		return err
	}

	pods, err := k8s.GetAgentPods(ctx, cs)
	if err != nil {
		return err
	}

	repoName, issue, err := parseLogsTarget(args)
	if err != nil {
		return err
	}
	if issue > 0 {
		pod, err := selectLogPod(pods, repoName, issue)
		if err != nil {
			return err
		}
		if pod == nil {
			if repoName != "" {
				fmt.Printf("No pods found for %s#%d\n", repoName, issue)
				return nil
			}
			fmt.Printf("No pods found for issue #%d\n", issue)
			return nil
		}

		if follow {
			fmt.Printf("Following logs for %s#%d (pod %s)...\n", pod.Repo.Name, issue, pod.Name)
			return k8s.FollowLog(ctx, cs, pod.Name)
		}

		log, err := k8s.GetFullLog(ctx, cs, pod.Name)
		if err != nil {
			return err
		}
		fmt.Print(log)
		return nil
	}

	if len(pods) == 0 {
		fmt.Println("No agent pods found.")
		return nil
	}

	fmt.Printf("%-7s %-12s %-10s %-11s %-16s Last Output\n", "Issue", "Repo", "Agent", "Status", "Started")
	for _, pod := range pods {
		agent := types.AgentName(pod.Slot)
		tail, _ := k8s.GetPodLogTail(ctx, cs, pod.Name, 20)
		fmt.Printf("#%-6d %-12s %-10s %-11s %-16s %s\n",
			pod.Issue, pod.Repo.Name, agent, pod.Phase.Display(),
			fmtTime(pod.Started), tail)
	}
	return nil
}

func parseLogsTarget(args []string) (string, int, error) {
	switch len(args) {
	case 0:
		return "", 0, nil
	case 1:
		issue, err := strconv.Atoi(args[0])
		if err != nil || issue <= 0 {
			return "", 0, fmt.Errorf("invalid issue number: %s", args[0])
		}
		return "", issue, nil
	case 2:
		repoName := args[0]
		if !isKnownRepo(repoName) {
			return "", 0, fmt.Errorf("unknown repo: %s", repoName)
		}
		issue, err := strconv.Atoi(args[1])
		if err != nil || issue <= 0 {
			return "", 0, fmt.Errorf("invalid issue number: %s", args[1])
		}
		return repoName, issue, nil
	default:
		return "", 0, fmt.Errorf("usage: k3sc logs [repo] [issue-number]")
	}
}

func isKnownRepo(name string) bool {
	for _, repo := range types.Repos {
		if repo.Name == name {
			return true
		}
	}
	return false
}

func selectLogPod(pods []types.AgentPod, repoName string, issue int) (*types.AgentPod, error) {
	var matches []types.AgentPod
	repos := map[string]bool{}
	for _, pod := range pods {
		if pod.Issue != issue {
			continue
		}
		repos[pod.Repo.Name] = true
		if repoName != "" && pod.Repo.Name != repoName {
			continue
		}
		matches = append(matches, pod)
	}
	if len(matches) == 0 {
		return nil, nil
	}
	if repoName == "" && len(repos) > 1 {
		names := make([]string, 0, len(repos))
		for name := range repos {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("issue #%d has pods in multiple repos (%s); rerun as `k3sc logs <repo> %d`", issue, strings.Join(names, ", "), issue)
	}

	pod := matches[len(matches)-1]
	return &pod, nil
}
