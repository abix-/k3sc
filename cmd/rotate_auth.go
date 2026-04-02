package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/abix-/k3sc/internal/config"
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

type awsCredResult struct {
	keyID        string
	secret       string
	sessionToken string
	region       string
	expiry       string // empty for static keys
}

// resolveAWSCredentials tries SSO profile first, then env vars, then credentials file.
func resolveAWSCredentials(home string) *awsCredResult {
	// 1. SSO profile via aws CLI
	if profile := config.C.AWSProfile; profile != "" {
		out, err := exec.Command("aws", "configure", "export-credentials",
			"--profile", profile, "--format", "env-no-export").Output()
		if err == nil {
			creds := &awsCredResult{region: "us-east-1"}
			for _, line := range strings.Split(string(out), "\n") {
				parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
				if len(parts) != 2 {
					continue
				}
				switch parts[0] {
				case "AWS_ACCESS_KEY_ID":
					creds.keyID = parts[1]
				case "AWS_SECRET_ACCESS_KEY":
					creds.secret = parts[1]
				case "AWS_SESSION_TOKEN":
					creds.sessionToken = parts[1]
				case "AWS_CREDENTIAL_EXPIRATION":
					creds.expiry = parts[1]
				}
			}
			// region from env or profile config
			if r := strings.TrimSpace(os.Getenv("AWS_REGION")); r != "" {
				creds.region = r
			}
			if creds.keyID != "" && creds.secret != "" {
				return creds
			}
		} else {
			fmt.Fprintf(os.Stderr, "warning: aws sso profile %q failed (run `aws sso login --profile %s`)\n", profile, profile)
		}
	}

	// 2. env vars
	keyID := strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID"))
	secret := strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY"))
	region := strings.TrimSpace(os.Getenv("AWS_REGION"))

	// 3. credentials file
	if keyID == "" || secret == "" {
		keyID, secret, region = readAWSCredentialsFile(home)
	}

	if keyID == "" || secret == "" {
		return nil
	}
	if region == "" {
		region = "us-east-1"
	}
	return &awsCredResult{keyID: keyID, secret: secret, region: region}
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
				AccessToken string  `json:"accessToken"`
				ExpiresAt   float64 `json:"expiresAt"`
			} `json:"claudeAiOauth"`
		}
		if err := json.Unmarshal(credsBytes, &creds); err != nil || creds.ClaudeAiOauth.AccessToken == "" {
			return fmt.Errorf("parse Claude OAuth token from %s", credsPath)
		}
		stringData["CLAUDE_CODE_OAUTH_TOKEN"] = creds.ClaudeAiOauth.AccessToken
		if creds.ClaudeAiOauth.ExpiresAt > 0 {
			expiresAt := time.Unix(int64(creds.ClaudeAiOauth.ExpiresAt/1000), 0)
			if time.Now().After(expiresAt) {
				fmt.Fprintf(os.Stderr, "warning: Claude OAuth token expired %s (run `claude auth login` to refresh)\n", expiresAt.Format("Jan 2 15:04"))
			} else {
				fmt.Printf("claude: OAuth token valid until %s\n", expiresAt.Format("Jan 2 15:04"))
			}
		}
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

	// AWS Bedrock credentials -- try SSO profile first, then env/static
	awsCreds := resolveAWSCredentials(home)
	if awsCreds != nil {
		stringData["AWS_ACCESS_KEY_ID"] = awsCreds.keyID
		stringData["AWS_SECRET_ACCESS_KEY"] = awsCreds.secret
		if awsCreds.sessionToken != "" {
			stringData["AWS_SESSION_TOKEN"] = awsCreds.sessionToken
		}
		stringData["AWS_REGION"] = awsCreds.region
		stringData["CLAUDE_CODE_USE_BEDROCK"] = "1"
		if awsCreds.expiry != "" {
			fmt.Printf("aws: Bedrock credentials (region=%s, expires=%s)\n", awsCreds.region, awsCreds.expiry)
		} else {
			fmt.Printf("aws: Bedrock credentials (region=%s, static keys)\n", awsCreds.region)
		}
	} else {
		fmt.Fprintln(os.Stderr, "warning: no AWS credentials found, skipping Bedrock auth")
	}

	// validate at least one Claude auth method is present
	hasBedrock := stringData["CLAUDE_CODE_USE_BEDROCK"] == "1"
	hasAPIKey := stringData["ANTHROPIC_API_KEY"] != ""
	hasOAuth := stringData["CLAUDE_CODE_OAUTH_TOKEN"] != ""
	if !hasBedrock && !hasAPIKey && !hasOAuth {
		hint := "run `claude auth login`"
		if config.C.AWSProfile != "" {
			hint = fmt.Sprintf("run `aws sso login --profile %s` or `claude auth login`", config.C.AWSProfile)
		}
		return fmt.Errorf("no Claude auth found -- %s", hint)
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
