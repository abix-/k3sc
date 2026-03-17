package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strconv"

	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/types"
	"k8s.io/client-go/kubernetes"
)

const DefaultMaxSlots = 5

// MaxSlots returns the configured max slots from env or default.
func MaxSlots() int {
	if v := os.Getenv("MAX_SLOTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return DefaultMaxSlots
}

// FindFreeSlot returns the lowest available slot (1-based), or -1 if none free.
func FindFreeSlot(ctx context.Context, cs *kubernetes.Clientset, maxSlots int) (int, error) {
	activeSlots, err := k8s.GetActiveSlots(ctx, cs)
	if err != nil {
		return -1, err
	}
	return FindFreeSlotFromList(activeSlots, maxSlots), nil
}

// FindFreeSlotFromList returns the lowest slot not in activeSlots, or -1.
func FindFreeSlotFromList(activeSlots []int, maxSlots int) int {
	for i := 1; i <= maxSlots; i++ {
		found := false
		for _, s := range activeSlots {
			if s == i {
				found = true
				break
			}
		}
		if !found {
			return i
		}
	}
	return -1
}

// LoadTemplate finds and reads the job template YAML.
func LoadTemplate() (string, error) {
	templatePath := os.Getenv("JOB_TEMPLATE")
	if templatePath == "" {
		exe, _ := os.Executable()
		candidates := []string{
			filepath.Join(filepath.Dir(exe), "manifests", "job-template.yaml"),
			filepath.Join("manifests", "job-template.yaml"),
			"/etc/dispatcher/job-template.yaml",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				templatePath = c
				break
			}
		}
		if templatePath == "" {
			templatePath = candidates[len(candidates)-1]
		}
	}
	data, err := os.ReadFile(templatePath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// RepoFromString parses "owner/name" into a types.Repo, defaulting to first configured repo.
func RepoFromString(s string) types.Repo {
	for _, r := range types.Repos {
		full := r.Owner + "/" + r.Name
		if full == s {
			return r
		}
	}
	return types.Repos[0]
}
