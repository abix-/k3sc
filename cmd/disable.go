package cmd

import (
	"fmt"
	"strings"

	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(disableCmd)
	rootCmd.AddCommand(enableCmd)
}

var disableCmd = &cobra.Command{
	Use:   "disable <family>",
	Short: "Disable dispatch for an agent family (claude, codex)",
	Args:  cobra.ExactArgs(1),
	RunE:  runDisable,
}

var enableCmd = &cobra.Command{
	Use:   "enable <family>",
	Short: "Re-enable dispatch for an agent family (claude, codex)",
	Args:  cobra.ExactArgs(1),
	RunE:  runEnable,
}

func runDisable(cmd *cobra.Command, args []string) error {
	family := types.AgentFamily(strings.ToLower(args[0]))
	if family != types.FamilyClaude && family != types.FamilyCodex {
		return fmt.Errorf("unknown family %q (use claude or codex)", args[0])
	}

	ctx := cmd.Context()
	info, err := k8s.GetDispatchState(ctx)
	if err != nil {
		return err
	}

	for _, f := range info.DisabledFamilies {
		if f == family {
			fmt.Printf("%s dispatch already disabled\n", family)
			return nil
		}
	}

	disabled := make([]string, 0, len(info.DisabledFamilies)+1)
	for _, f := range info.DisabledFamilies {
		disabled = append(disabled, string(f))
	}
	disabled = append(disabled, string(family))

	if err := k8s.SetDisabledFamilies(ctx, disabled); err != nil {
		return err
	}
	fmt.Printf("%s dispatch disabled\n", family)
	return nil
}

func runEnable(cmd *cobra.Command, args []string) error {
	family := types.AgentFamily(strings.ToLower(args[0]))
	if family != types.FamilyClaude && family != types.FamilyCodex {
		return fmt.Errorf("unknown family %q (use claude or codex)", args[0])
	}

	ctx := cmd.Context()
	info, err := k8s.GetDispatchState(ctx)
	if err != nil {
		return err
	}

	var remaining []string
	found := false
	for _, f := range info.DisabledFamilies {
		if f == family {
			found = true
			continue
		}
		remaining = append(remaining, string(f))
	}

	if !found {
		fmt.Printf("%s dispatch already enabled\n", family)
		return nil
	}

	if err := k8s.SetDisabledFamilies(ctx, remaining); err != nil {
		return err
	}
	fmt.Printf("%s dispatch enabled\n", family)
	return nil
}
