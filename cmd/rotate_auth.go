package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(rotateAuthCmd)
}

var rotateAuthCmd = &cobra.Command{
	Use:   "rotate-auth",
	Short: "Update k8s claude-secrets from local auth files",
	Long:  "Reads Claude OAuth token from ~/.claude/.credentials.json, Codex auth from ~/.codex/auth.json, and GitHub token from ~/.gh-token, then patches the claude-secrets k8s secret.",
	RunE:  runRotateAuth,
}

func runRotateAuth(cmd *cobra.Command, args []string) error {
	home := os.Getenv("USERPROFILE")
	if home == "" {
		home = os.Getenv("HOME")
	}

	stringData := map[string]string{}

	// Claude OAuth token
	credsPath := filepath.Join(home, ".claude", ".credentials.json")
	credsBytes, err := os.ReadFile(credsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s not found, skipping Claude token\n", credsPath)
	} else {
		var creds struct {
			ClaudeAiOauth struct {
				AccessToken string `json:"accessToken"`
			} `json:"claudeAiOauth"`
		}
		if err := json.Unmarshal(credsBytes, &creds); err != nil || creds.ClaudeAiOauth.AccessToken == "" {
			return fmt.Errorf("parse Claude OAuth token from %s", credsPath)
		}
		stringData["CLAUDE_CODE_OAUTH_TOKEN"] = creds.ClaudeAiOauth.AccessToken
		fmt.Println("claude: read token from", credsPath)
	}

	// GitHub token
	ghTokenBytes, err := os.ReadFile(filepath.Join(home, ".gh-token"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: ~/.gh-token not found, skipping GitHub token\n")
	} else {
		ghToken := strings.TrimSpace(string(ghTokenBytes))
		if ghToken != "" {
			stringData["GITHUB_TOKEN"] = ghToken
			fmt.Println("github: read token from ~/.gh-token")
		}
	}

	// Codex auth
	codexAuthPath := filepath.Join(home, ".codex", "auth.json")
	codexAuthBytes, err := os.ReadFile(codexAuthPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s not found, skipping Codex auth\n", codexAuthPath)
	} else {
		codexAuthJSON := strings.TrimSpace(string(codexAuthBytes))
		if codexAuthJSON != "" {
			stringData["CODEX_AUTH_JSON"] = codexAuthJSON
			fmt.Println("codex: read auth from", codexAuthPath)
		}
		var codexAuth struct {
			OpenAIAPIKey *string `json:"OPENAI_API_KEY"`
		}
		if err := json.Unmarshal(codexAuthBytes, &codexAuth); err == nil && codexAuth.OpenAIAPIKey != nil {
			if key := strings.TrimSpace(*codexAuth.OpenAIAPIKey); key != "" {
				stringData["OPENAI_API_KEY"] = key
			}
		}
	}

	if len(stringData) == 0 {
		return fmt.Errorf("no auth sources found")
	}

	manifest, err := json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      "claude-secrets",
			"namespace": "claude-agents",
		},
		"type":       "Opaque",
		"stringData": stringData,
	})
	if err != nil {
		return fmt.Errorf("marshal secret: %w", err)
	}

	// apply via stdin so secrets never appear in process args
	c := exec.Command("wsl", "-d", "Ubuntu-24.04", "--", "sudo", "k3s", "kubectl", "apply", "-f", "-")
	c.Stdin = bytes.NewReader(manifest)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("kubectl apply: %w", err)
	}

	fmt.Printf("rotated %d auth key(s) in claude-secrets\n", len(stringData))
	return nil
}
