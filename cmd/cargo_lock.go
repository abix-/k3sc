package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// killEndlessExe terminates endless.exe on Windows if it is running.
// It is a no-op on non-Windows platforms and when the process is not running.
func killEndlessExe() {
	if runtime.GOOS != "windows" {
		return
	}
	cmd := exec.Command("taskkill", "/F", "/IM", "endless.exe")
	// ignore error: exit code 128 means process not found, which is fine
	if err := cmd.Run(); err == nil {
		// process was killed; wait briefly for file handles to release
		time.Sleep(500 * time.Millisecond)
	}
}

// hasManifestPath checks if --manifest-path is already in the args.
func hasManifestPath(args []string) bool {
	for _, a := range args {
		if a == "--manifest-path" || strings.HasPrefix(a, "--manifest-path=") {
			return true
		}
	}
	return false
}

// findManifestPath walks up from cwd looking for Cargo.toml.
func findManifestPath() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, "Cargo.toml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		// check rust/ subdirectory (endless repo layout)
		candidate = filepath.Join(dir, "rust", "Cargo.toml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
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

	killEndlessExe()

	ts = time.Now().Format("15:04:05")
	cargoArgs := args

	// auto-detect --manifest-path if not specified; insert before "--" so
	// test/run don't pass it to the binary as a positional arg
	if !hasManifestPath(cargoArgs) {
		if mp := findManifestPath(); mp != "" {
			dashIdx := -1
			for i, a := range cargoArgs {
				if a == "--" {
					dashIdx = i
					break
				}
			}
			if dashIdx >= 0 {
				tail := make([]string, len(cargoArgs[dashIdx:]))
				copy(tail, cargoArgs[dashIdx:])
				cargoArgs = append(append(cargoArgs[:dashIdx], "--manifest-path", mp), tail...)
			} else {
				cargoArgs = append(cargoArgs, "--manifest-path", mp)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "[cargo-lock] %s acquired lock, running: cargo %s\n", ts, strings.Join(cargoArgs, " "))

	// "run" automatically builds first -- can't run what isn't built
	if cargoArgs[0] == "run" || cargoArgs[0] == "test" {
		// strip everything after "--" for build (test filters, run args)
		var filtered []string
		for _, a := range cargoArgs[1:] {
			if a == "--" {
				break
			}
			filtered = append(filtered, a)
		}
		buildArgs := append([]string{"build"}, filtered...)
		fmt.Fprintf(os.Stderr, "[cargo-lock] %s running: cargo %s\n", time.Now().Format("15:04:05"), strings.Join(buildArgs, " "))
		b := exec.Command("cargo", buildArgs...)
		b.Stdout = os.Stdout
		b.Stderr = os.Stderr
		if err := b.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			return err
		}
		fmt.Fprintf(os.Stderr, "[cargo-lock] %s build ok, running: cargo %s\n", time.Now().Format("15:04:05"), strings.Join(cargoArgs, " "))
	}

	c := exec.Command("cargo", cargoArgs...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}
