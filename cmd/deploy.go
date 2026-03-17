package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(deployCmd)
}

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Build image and deploy manifests to k3s",
	RunE:  runDeploy,
}

func runCmd(desc, cmd string) error {
	fmt.Printf("=== %s ===\n", desc)
	fmt.Printf("  $ %s\n", cmd)
	c := exec.Command("sh", "-c", cmd)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func runDeploy(cmd *cobra.Command, args []string) error {
	// find repo root (where go.mod lives)
	ex, _ := os.Executable()
	repoRoot := filepath.Dir(ex)
	// fallback: check common locations
	for _, candidate := range []string{repoRoot, "/mnt/c/code/k3sc", "."} {
		if _, err := os.Stat(filepath.Join(candidate, "manifests")); err == nil {
			repoRoot = candidate
			break
		}
	}

	imageDir := filepath.Join(repoRoot, "image")
	manifests := filepath.Join(repoRoot, "manifests")
	nerdctl := "sudo nerdctl --address /run/k3s/containerd/containerd.sock --namespace k8s.io"
	kubectl := "sudo k3s kubectl"

	if err := runCmd("building claude-agent image", fmt.Sprintf("%s build -t claude-agent:latest %s", nerdctl, imageDir)); err != nil {
		return err
	}
	if err := runCmd("applying namespace", fmt.Sprintf("%s apply -f %s/namespace.yaml", kubectl, manifests)); err != nil {
		return err
	}

	entries, _ := os.ReadDir(manifests)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "pvc-") {
			if err := runCmd("applying "+e.Name(), fmt.Sprintf("%s apply -f %s/%s", kubectl, manifests, e.Name())); err != nil {
				return err
			}
		}
	}

	if err := runCmd("creating configmap", fmt.Sprintf(
		"%s create configmap dispatcher-scripts -n claude-agents --from-file=job-template.yaml=%s/job-template.yaml --dry-run=client -o yaml | %s apply -f -",
		kubectl, manifests, kubectl)); err != nil {
		return err
	}

	if err := runCmd("applying dispatcher cronjob + RBAC", fmt.Sprintf("%s apply -f %s/dispatcher-cronjob.yaml", kubectl, manifests)); err != nil {
		return err
	}

	fmt.Println("\n=== deployment complete ===")
	return nil
}
