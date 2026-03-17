package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(launchCmd)
}

var launchCmd = &cobra.Command{
	Use:   "launch",
	Short: "Launch claude in the next free endless-claude-N directory",
	RunE:  runLaunch,
}

const lockFile = ".claude-k3.lock"

func runLaunch(cmd *cobra.Command, args []string) error {
	base := `C:\code`
	repoURL := "https://github.com/abix-/endless.git"

	// find free slot: dir exists but no lockfile, or dir doesn't exist yet
	entries, err := os.ReadDir(base)
	if err != nil {
		return fmt.Errorf("read %s: %w", base, err)
	}

	existing := map[int]bool{}
	locked := map[int]bool{}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "endless-claude-") {
			continue
		}
		suffix := strings.TrimPrefix(e.Name(), "endless-claude-")
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

	dir := filepath.Join(base, fmt.Sprintf("endless-claude-%d", slot))
	fmt.Printf("slot %d -> %s\n", slot, dir)

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

	// write lockfile with our PID (claude will inherit it)
	lockPath := filepath.Join(dir, lockFile)

	// launch claude interactively
	fmt.Printf("launching claude in %s\n", dir)
	c := exec.Command("claude")
	c.Dir = dir
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	if err := c.Start(); err != nil {
		return err
	}

	// write claude's PID to lockfile
	os.WriteFile(lockPath, []byte(strconv.Itoa(c.Process.Pid)), 0o644)

	err = c.Wait()

	// cleanup lock
	os.Remove(lockPath)

	return err
}
