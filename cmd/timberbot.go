package cmd

import (
	"fmt"
	"strings"

	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
)

var timberbotRounds int

func init() {
	timberbotStartCmd.Flags().IntVar(&timberbotRounds, "rounds", 5, "number of /timberbot rounds per agent")
	timberbotCmd.AddCommand(timberbotStartCmd)
	timberbotCmd.AddCommand(timberbotStopCmd)
	timberbotCmd.AddCommand(timberbotStatusCmd)
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

func runTimberbotStart(cmd *cobra.Command, args []string) error {
	goal := strings.Join(args, " ")
	ctx := cmd.Context()

	info := types.TimberbotInfo{
		Enabled: true,
		Goal:    goal,
		Rounds:  timberbotRounds,
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
