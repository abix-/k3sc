package cmd

import (
	"fmt"
	"os"

	"github.com/abix-/k3sc/internal/config"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "k3sc",
	Short: "k3s Claude agent management",
}

func init() {
	config.Load()
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
