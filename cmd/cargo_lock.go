package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(cargoLockCmd)
}

var cargoLockCmd = &cobra.Command{
	Use:                "cargo-lock",
	Short:              "Serialize cargo builds using a lockfile",
	Long:               "Wraps cargo commands with an exclusive file lock so concurrent agents don't clobber each other.",
	DisableFlagParsing: true,
	RunE:               runCargoLock,
}

func runCargoLock(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: k3sc cargo-lock <cargo subcommand> [args...]")
	}

	// find lockfile: $CARGO_TARGET_DIR/.cargo-build.lock or auto-detect
	targetDir := os.Getenv("CARGO_TARGET_DIR")
	if targetDir == "" {
		// try to find target dir relative to cwd
		if _, err := os.Stat("target"); err == nil {
			targetDir = "target"
		} else if _, err := os.Stat("rust/target"); err == nil {
			targetDir = "rust/target"
		} else {
			targetDir = "."
		}
	}

	lockPath := filepath.Join(targetDir, ".cargo-build.lock")
	os.MkdirAll(filepath.Dir(lockPath), 0o755)

	ts := time.Now().Format("15:04:05")
	fmt.Fprintf(os.Stderr, "[cargo-lock] %s waiting for build lock...\n", ts)

	fl := flock.New(lockPath)
	if err := fl.Lock(); err != nil {
		return fmt.Errorf("acquire lock %s: %w", lockPath, err)
	}
	defer func() {
		fl.Unlock()
		ts := time.Now().Format("15:04:05")
		fmt.Fprintf(os.Stderr, "[cargo-lock] %s released lock\n", ts)
	}()

	ts = time.Now().Format("15:04:05")
	cargoArgs := args
	fmt.Fprintf(os.Stderr, "[cargo-lock] %s acquired lock, running: cargo %s\n", ts, strings.Join(cargoArgs, " "))

	c := exec.Command("cargo", cargoArgs...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	err := c.Run()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}
