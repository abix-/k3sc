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

func readAWSCredentialsFile(home string) (keyID, secret, region string) {
	credsPath := filepath.Join(home, ".aws", "credentials")
	data, err := os.ReadFile(credsPath)
	if err != nil {
		return "", "", ""
	}
	inDefault := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "[default]" {
			inDefault = true
			continue
		}
		if strings.HasPrefix(line, "[") {
			inDefault = false
			continue
		}
		if !inDefault {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch key {
		case "aws_access_key_id":
			keyID = val
		case "aws_secret_access_key":
			secret = val
		case "region":
			region = val
		}
	}
	// also check ~/.aws/config for region if not in credentials
	if region == "" {
		configPath := filepath.Join(home, ".aws", "config")
		if cfgData, err := os.ReadFile(configPath); err == nil {
			inDefault = false
			for _, line := range strings.Split(string(cfgData), "\n") {
				line = strings.TrimSpace(line)
				if line == "[default]" || line == "[profile default]" {
					inDefault = true
					continue
				}
				if strings.HasPrefix(line, "[") {
					inDefault = false
					continue
				}
				if !inDefault {
					continue
				}
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 && strings.TrimSpace(parts[0]) == "region" {
					region = strings.TrimSpace(parts[1])
					break
				}
			}
		}
	}
	return keyID, secret, region
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

	// Anthropic API key (direct API access)
	if apiKey := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); apiKey != "" {
		stringData["ANTHROPIC_API_KEY"] = apiKey
		fmt.Println("anthropic: read API key from ANTHROPIC_API_KEY env var")
	}

	// AWS Bedrock credentials
	awsKeyID := strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID"))
	awsSecret := strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY"))
	awsRegion := strings.TrimSpace(os.Getenv("AWS_REGION"))
	if awsKeyID == "" || awsSecret == "" {
		awsKeyID, awsSecret, awsRegion = readAWSCredentialsFile(home)
	}
	if awsKeyID != "" && awsSecret != "" {
		stringData["AWS_ACCESS_KEY_ID"] = awsKeyID
		stringData["AWS_SECRET_ACCESS_KEY"] = awsSecret
		if awsRegion == "" {
			awsRegion = "us-east-1"
		}
		stringData["AWS_REGION"] = awsRegion
		stringData["CLAUDE_CODE_USE_BEDROCK"] = "1"
		fmt.Printf("aws: read Bedrock credentials (region=%s)\n", awsRegion)
	} else {
		fmt.Fprintln(os.Stderr, "warning: no AWS credentials found, skipping Bedrock auth")
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
