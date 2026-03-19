package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

var skipTest bool

func init() {
	deployCmd.Flags().BoolVar(&skipTest, "skip-test", false, "skip go test step")
	rootCmd.AddCommand(deployCmd)
}

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Build, test, and deploy operator to k3s",
	RunE:  runDeploy,
}

func runCmd(desc, name string, args ...string) error {
	fmt.Printf("=== %s ===\n", desc)
	fmt.Printf("  $ %s %s\n", name, strings.Join(args, " "))
	c := exec.Command(name, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func runCmdEnv(desc string, env []string, name string, args ...string) error {
	fmt.Printf("=== %s ===\n", desc)
	fmt.Printf("  $ %s %s\n", name, strings.Join(args, " "))
	c := exec.Command(name, args...)
	c.Env = append(os.Environ(), env...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func ensureSecret() error {
	// check if secret already exists with all keys
	c := exec.Command("wsl", "-d", "Ubuntu-24.04", "--", "sudo", "k3s", "kubectl",
		"get", "secret", "claude-secrets", "-n", "claude-agents", "-o", "json")
	if out, err := c.Output(); err == nil {
		var secret struct {
			Data map[string]string `json:"data"`
		}
		if err := json.Unmarshal(out, &secret); err == nil &&
			secret.Data["GITHUB_TOKEN"] != "" &&
			secret.Data["CLAUDE_CODE_OAUTH_TOKEN"] != "" {
			fmt.Println("=== secret claude-secrets exists with all keys, skipping ===")
			return nil
		}
	}

	home := os.Getenv("USERPROFILE")
	if home == "" {
		home = os.Getenv("HOME")
	}

	// read GitHub token from ~/.gh-token
	ghTokenBytes, err := os.ReadFile(filepath.Join(home, ".gh-token"))
	if err != nil {
		fmt.Println("=== WARNING: no ~/.gh-token found ===")
		fmt.Println("  Create claude-secrets manually, but do not put tokens directly on a shell command line")
		return nil
	}
	ghToken := strings.TrimSpace(string(ghTokenBytes))

	// read Claude OAuth token from ~/.claude/.credentials.json
	credsPath := filepath.Join(home, ".claude", ".credentials.json")
	credsBytes, err := os.ReadFile(credsPath)
	if err != nil {
		fmt.Println("=== WARNING: no ~/.claude/.credentials.json found ===")
		fmt.Println("  Run 'claude setup-token' first, then re-run deploy")
		return nil
	}
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(credsBytes, &creds); err != nil || creds.ClaudeAiOauth.AccessToken == "" {
		fmt.Println("=== WARNING: could not parse Claude OAuth token from credentials ===")
		return nil
	}

	manifest, err := json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      "claude-secrets",
			"namespace": "claude-agents",
		},
		"type": "Opaque",
		"stringData": map[string]string{
			"GITHUB_TOKEN":            ghToken,
			"CLAUDE_CODE_OAUTH_TOKEN": creds.ClaudeAiOauth.AccessToken,
		},
	})
	if err != nil {
		return fmt.Errorf("marshal secret manifest: %w", err)
	}

	// Apply via stdin so tokens never appear in shell or process args.
	fmt.Println("=== applying secret from ~/.gh-token + ~/.claude/.credentials.json ===")
	cr := exec.Command("wsl", "-d", "Ubuntu-24.04", "--", "sudo", "k3s", "kubectl", "apply", "-f", "-")
	cr.Stdin = bytes.NewReader(manifest)
	cr.Stdout = os.Stdout
	cr.Stderr = os.Stderr
	return cr.Run()
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("repo root not found: no go.mod in cwd or any parent directory")
}

func runDeploy(cmd *cobra.Command, args []string) error {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}
	if err := os.Chdir(repoRoot); err != nil {
		return fmt.Errorf("chdir %s: %w", repoRoot, err)
	}

	// 1. build windows binary
	exe := "k3sc"
	if runtime.GOOS == "windows" {
		exe = "k3sc.exe"
	}
	if err := runCmd("building windows binary", "go", "build", "-o", exe, "."); err != nil {
		return fmt.Errorf("go build: %w", err)
	}

	// 2. tests
	if !skipTest {
		if err := runCmd("running tests", "go", "test", "./..."); err != nil {
			return fmt.Errorf("go test: %w", err)
		}
	} else {
		fmt.Println("=== skipping tests ===")
	}

	// 3. cross-compile linux binary
	if err := runCmdEnv("cross-compiling linux binary",
		[]string{"GOOS=linux", "GOARCH=amd64"},
		"go", "build", "-o", filepath.Join("image", "k3sc"), "."); err != nil {
		return fmt.Errorf("linux build: %w", err)
	}

	// 4. build container image via WSL
	mntRoot := strings.ReplaceAll(repoRoot, `\`, `/`)
	// convert C:\code\k3sc -> /mnt/c/code/k3sc
	if len(mntRoot) >= 2 && mntRoot[1] == ':' {
		mntRoot = "/mnt/" + strings.ToLower(mntRoot[:1]) + mntRoot[2:]
	}
	nerdctl := "sudo nerdctl --address /run/k3s/containerd/containerd.sock --namespace k8s.io"
	buildCmd := fmt.Sprintf("cd %s && %s build -t claude-agent:latest image/", mntRoot, nerdctl)
	if err := runCmd("building container image",
		"wsl", "-d", "Ubuntu-24.04", "--", "bash", "-c", buildCmd); err != nil {
		return fmt.Errorf("image build: %w", err)
	}

	// 5. apply k8s manifests
	kubectl := "sudo k3s kubectl"
	mntManifests := mntRoot + "/manifests"

	// order matters: namespace -> CRD -> secret -> PVCs -> configmap -> operator
	manifests := []string{
		"namespace.yaml",
		"crd.yaml",
		"pvc-cargo-target.yaml",
		"pvc-cargo-home.yaml",
		"pvc-workspaces.yaml",
		"operator-deployment.yaml",
	}
	for _, file := range manifests {
		applyCmd := fmt.Sprintf("%s apply -f %s/%s", kubectl, mntManifests, file)
		if err := runCmd("applying "+file,
			"wsl", "-d", "Ubuntu-24.04", "--", "bash", "-c", applyCmd); err != nil {
			return fmt.Errorf("apply %s: %w", file, err)
		}
	}

	// secret: don't overwrite if it exists (template has placeholder values)
	if err := ensureSecret(); err != nil {
		return err
	}

	// configmap from job template (special: create --dry-run | apply)
	cmCmd := fmt.Sprintf("%s create configmap dispatcher-scripts -n claude-agents --from-file=job-template.yaml=%s/job-template.yaml --dry-run=client -o yaml | %s apply -f -",
		kubectl, mntManifests, kubectl)
	if err := runCmd("applying job template configmap",
		"wsl", "-d", "Ubuntu-24.04", "--", "bash", "-c", cmCmd); err != nil {
		return fmt.Errorf("apply configmap: %w", err)
	}

	// 6. restart operator
	restartCmd := fmt.Sprintf("%s rollout restart deployment k3sc-operator -n claude-agents", kubectl)
	if err := runCmd("restarting operator",
		"wsl", "-d", "Ubuntu-24.04", "--", "bash", "-c", restartCmd); err != nil {
		return fmt.Errorf("rollout restart: %w", err)
	}

	// 6. wait for rollout
	waitCmd := fmt.Sprintf("%s rollout status deployment k3sc-operator -n claude-agents --timeout=60s", kubectl)
	if err := runCmd("waiting for rollout",
		"wsl", "-d", "Ubuntu-24.04", "--", "bash", "-c", waitCmd); err != nil {
		return fmt.Errorf("rollout status: %w", err)
	}

	fmt.Println("\n=== deploy complete ===")
	return nil
}
