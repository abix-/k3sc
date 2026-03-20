package cmd

import (
	"github.com/abix-/k3sc/internal/k8s"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(dispatchCmd)
}

var dispatchCmd = &cobra.Command{
	Use:   "dispatch",
	Short: "Trigger an immediate operator dispatch scan",
	RunE: func(cmd *cobra.Command, args []string) error {
		msg, err := k8s.TriggerDispatch(cmd.Context())
		if err != nil {
			return err
		}
		cmd.Print(msg)
		return nil
	},
}
