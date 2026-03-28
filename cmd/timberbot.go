package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/tui"
	"github.com/abix-/k3sc/internal/types"
	batchv1 "k8s.io/api/batch/v1"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

var timberbotRounds int

func init() {
	timberbotStartCmd.Flags().IntVar(&timberbotRounds, "rounds", 5, "number of /timberbot rounds per agent")
	timberbotCmd.AddCommand(timberbotStartCmd)
	timberbotCmd.AddCommand(timberbotStopCmd)
	timberbotCmd.AddCommand(timberbotStatusCmd)
	timberbotCmd.AddCommand(timberbotTopCmd)
	rootCmd.AddCommand(timberbotCmd)
}

var timberbotCmd = &cobra.Command{
	Use:   "timberbot",
	Short: "Manage timberbot player dispatch",
}

var timberbotStartCmd = &cobra.Command{
	Use:   "start <goal>",
	Short: "Start dispatching timberbot player agents",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runTimberbotStart,
}

var timberbotStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop dispatching timberbot player agents",
	RunE:  runTimberbotStop,
}

var timberbotStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show timberbot dispatch status",
	RunE:  runTimberbotStatus,
}

var timberbotTopCmd = &cobra.Command{
	Use:   "top",
	Short: "Live dashboard for timberbot player",
	RunE:  runTimberbotTop,
}

func runTimberbotStart(cmd *cobra.Command, args []string) error {
	goal := strings.Join(args, " ")
	ctx := cmd.Context()

	info := types.TimberbotInfo{
		Enabled: true,
		Goal:    goal,
		Rounds:  timberbotRounds,
		Host:    resolveWSLGateway(),
	}
	if err := k8s.SetTimberbotSpec(ctx, info); err != nil {
		return err
	}

	fmt.Printf("timberbot started: %d rounds, goal: %s\n", timberbotRounds, goal)

	// trigger immediate dispatch scan
	msg, err := k8s.TriggerDispatch(ctx)
	if err != nil {
		fmt.Printf("warning: trigger dispatch: %v\n", err)
	} else {
		fmt.Print(msg)
	}
	return nil
}

func runTimberbotStop(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	current, err := k8s.GetTimberbotSpec(ctx)
	if err != nil {
		return err
	}
	if !current.Enabled {
		fmt.Println("timberbot already stopped")
		return nil
	}

	current.Enabled = false
	if err := k8s.SetTimberbotSpec(ctx, current); err != nil {
		return err
	}
	fmt.Println("timberbot stopped")
	return nil
}

func runTimberbotStatus(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	info, err := k8s.GetTimberbotSpec(ctx)
	if err != nil {
		return err
	}

	if !info.Enabled {
		fmt.Println("timberbot: disabled")
		return nil
	}

	fmt.Printf("timberbot: enabled\n")
	fmt.Printf("  goal: %s\n", info.Goal)
	fmt.Printf("  rounds: %d\n", info.Rounds)
	return nil
}

// resolveWSLGateway gets the WSL2 default gateway (Windows host IP) by asking WSL.
func resolveWSLGateway() string {
	out, err := exec.Command("wsl", "-d", "Ubuntu-24.04", "--", "bash", "-c",
		"ip route show default | awk '/default/ {print \\$3}'").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runTimberbotTop(cmd *cobra.Command, args []string) error {
	cs, err := k8s.NewClient()
	if err != nil {
		return err
	}

	streamer := tui.NewLogStreamer(cs, types.Namespace)
	defer streamer.Stop()

	gatherFn := func() (*tui.TimberbotData, error) {
		ctx := context.Background()
		var (
			info types.TimberbotInfo
			pods []types.AgentPod
			jobs []batchv1.Job
			mu   sync.Mutex
			wg   sync.WaitGroup
		)
		wg.Add(3)
		go func() {
			defer wg.Done()
			t, _ := k8s.GetTimberbotSpec(ctx)
			mu.Lock()
			info = t
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			p, _ := k8s.GetAgentPods(ctx, cs)
			mu.Lock()
			pods = p
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			j, _ := k8s.GetTimberbotJobs(ctx, cs)
			mu.Lock()
			jobs = j
			mu.Unlock()
		}()
		wg.Wait()

		// filter to timberbot pods only
		var tbPods []types.AgentPod
		for _, p := range pods {
			if p.JobKind == "timberbot" {
				tbPods = append(tbPods, p)
			}
		}

		streamer.Sync(tbPods)
		liveLogs := streamer.Snapshot()

		return &tui.TimberbotData{
			Info:     info,
			Runs:     jobsToRuns(jobs),
			LiveLogs: liveLogs,
		}, nil
	}

	m := tui.NewTimberbotModel(gatherFn, gatherFn)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func jobsToRuns(jobs []batchv1.Job) []tui.TimberbotRun {
	var runs []tui.TimberbotRun
	for _, j := range jobs {
		phase := "Running"
		if j.Status.Succeeded > 0 {
			phase = "Succeeded"
		} else if j.Status.Failed > 0 {
			phase = "Failed"
		} else {
			for _, c := range j.Status.Conditions {
				if c.Type == batchv1.JobFailed && c.Status == "True" {
					phase = "Failed"
					break
				}
			}
		}
		if phase == "Running" && j.Status.Active == 0 && j.Status.Succeeded == 0 && j.Status.Failed == 0 {
			phase = "Pending"
		}

		slot := 0
		if s, ok := j.Labels["agent-slot"]; ok {
			fmt.Sscanf(s, "%d", &slot)
		}
		family := types.AgentFamily(j.Labels["agent-family"])
		if family == "" {
			family = types.FamilyClaude
		}
		agent := types.AgentName(family, slot)

		var started, finished *time.Time
		if j.Status.StartTime != nil {
			t := j.Status.StartTime.Time
			started = &t
		}
		if j.Status.CompletionTime != nil {
			t := j.Status.CompletionTime.Time
			finished = &t
		}

		runs = append(runs, tui.TimberbotRun{
			Name:     j.Name,
			Agent:    agent,
			Phase:    phase,
			Started:  started,
			Finished: finished,
		})
	}
	return runs
}
