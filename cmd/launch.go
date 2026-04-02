package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/abix-/k3sc/internal/config"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(launchCmd)
}

var launchCmd = &cobra.Command{
	Use:   "launch [claude|codex]",
	Short: "Launch claude or codex in the next free slot directory",
	RunE:  runLaunch,
}

const lockFile = ".k3sc.lock"

func runLaunch(cmd *cobra.Command, args []string) error {
	agent := "claude"
	if len(args) > 0 && args[0] == "codex" {
		agent = "codex"
	}

	base := config.C.LaunchDir
	repoURL := strings.TrimRight(types.GitHubURL, "/") + "/" + config.C.Repos[0].Owner + "/" + config.C.Repos[0].Name + ".git"
	prefix := agent + "-"

	// find free slot: dir exists but no lockfile, or dir doesn't exist yet
	entries, err := os.ReadDir(base)
	if err != nil {
		return fmt.Errorf("read %s: %w", base, err)
	}

	existing := map[int]bool{}
	locked := map[int]bool{}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		suffix := strings.TrimPrefix(e.Name(), prefix)
		n, err := strconv.Atoi(suffix)
		if err != nil {
			continue
		}
		existing[n] = true
		lock := filepath.Join(base, e.Name(), lockFile)
		if _, err := os.Stat(lock); err == nil {
			// lockfile exists -- check if the PID inside is still alive
			data, _ := os.ReadFile(lock)
			pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				out, _ := exec.Command("tasklist", "/FI",
					fmt.Sprintf("PID eq %d", pid), "/NH").Output()
				if strings.Contains(string(out), strconv.Itoa(pid)) {
					locked[n] = true
					continue
				}
			}
			// stale lock -- remove it
			os.Remove(lock)
		}
	}

	// pick lowest free slot: existing but unlocked first, then next new slot
	slot := 0
	for i := 1; i <= 20; i++ {
		if existing[i] && !locked[i] {
			slot = i
			break
		}
	}
	if slot == 0 {
		// no unlocked existing dir, pick next after highest
		max := 0
		for n := range existing {
			if n > max {
				max = n
			}
		}
		slot = max + 1
	}

	dir := filepath.Join(base, fmt.Sprintf("%s%d", prefix, slot))
	fmt.Printf("%s slot %d -> %s\n", agent, slot, dir)

	// clone if directory doesn't exist
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		fmt.Printf("cloning %s into %s...\n", repoURL, dir)
		c := exec.Command("git", "clone", repoURL, dir)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("git clone: %w", err)
		}
	}

	lockPath := filepath.Join(dir, lockFile)

	// codex is an npm .cmd shim on Windows; wezterm needs cmd.exe to run it
	var c *exec.Cmd
	if agent == "claude" {
		c = exec.Command("wezterm", "start", "--cwd", dir, "--", "claude", "/obey")
	} else if agent == "codex" {
		c = exec.Command("wezterm", "start", "--cwd", dir, "--", "cmd", "/c", "codex")
	} else {
		c = exec.Command("wezterm", "start", "--cwd", dir, "--", agent)
	}
	if err := c.Start(); err != nil {
		return fmt.Errorf("wezterm: %w", err)
	}

	os.WriteFile(lockPath, []byte(strconv.Itoa(c.Process.Pid)), 0o644)
	c.Process.Release()
	return nil
}
