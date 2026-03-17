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

func runLaunch(cmd *cobra.Command, args []string) error {
	base := `C:\code`
	repoURL := "https://github.com/abix-/endless.git"

	// find occupied slots by checking which endless-claude-N dirs have a claude process
	occupied := map[int]bool{}
	entries, err := os.ReadDir(base)
	if err != nil {
		return fmt.Errorf("read %s: %w", base, err)
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "endless-claude-") {
			continue
		}
		suffix := strings.TrimPrefix(e.Name(), "endless-claude-")
		n, err := strconv.Atoi(suffix)
		if err != nil {
			continue
		}
		// check if claude is running in this directory
		out, _ := exec.Command("wmic", "process", "where",
			fmt.Sprintf(`name='claude.exe' and commandline like '%%%s%%'`, e.Name()),
			"get", "processid", "/format:list").Output()
		if strings.Contains(string(out), "ProcessId=") {
			occupied[n] = true
		}
	}

	// pick next free slot (1-based)
	slot := 0
	for i := 1; i <= 20; i++ {
		if !occupied[i] {
			slot = i
			break
		}
	}
	if slot == 0 {
		return fmt.Errorf("no free slots (1-20)")
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

	// launch claude interactively in that directory
	fmt.Printf("launching claude in %s\n", dir)
	c := exec.Command("claude")
	c.Dir = dir
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
