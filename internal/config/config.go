package config

import (
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/abix-/k3sc/internal/types"
	"sigs.k8s.io/yaml"
)

// C is the global config, loaded once at startup.
var C Config

type Config struct {
	Namespace      string       `json:"namespace"`
	MaxSlots       int          `json:"max_slots"`
	LaunchDir      string       `json:"launch_dir"`
	GitHubURL      string       `json:"github_url"`
	Repos          []RepoConfig `json:"repos"`
	AllowedAuthors []string     `json:"allowed_authors"`
	Scan           ScanConfig   `json:"scan"`
}

type RepoConfig struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

type ScanConfig struct {
	MinInterval Duration `json:"min_interval"`
	MaxInterval Duration `json:"max_interval"`
	TaskTTL     Duration `json:"task_ttl"`
}

// Duration wraps time.Duration for YAML unmarshaling from strings like "2m", "1h".
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	// sigs.k8s.io/yaml converts YAML to JSON first, so we get JSON bytes.
	// Strip quotes if present.
	s := string(b)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.Duration.String() + `"`), nil
}

func defaults() Config {
	return Config{
		Namespace:      "claude-agents",
		MaxSlots:       5,
		LaunchDir:      launchDirDefault(),
		GitHubURL:      "https://github.com",
		AllowedAuthors: []string{},
		Repos:          []RepoConfig{},
		Scan: ScanConfig{
			MinInterval: Duration{2 * time.Minute},
			MaxInterval: Duration{1 * time.Hour},
			TaskTTL:     Duration{24 * time.Hour},
		},
	}
}

func launchDirDefault() string {
	if runtime.GOOS == "windows" {
		return `C:\code`
	}
	return filepath.Join(os.Getenv("HOME"), "code")
}

// Load reads ~/.k3sc.yaml and merges with defaults. Missing file is not an error.
func Load() {
	C = defaults()

	home := os.Getenv("HOME")
	if home == "" {
		home = os.Getenv("USERPROFILE")
	}
	path := filepath.Join(home, ".k3sc.yaml")

	data, err := os.ReadFile(path)
	if err != nil {
		return // no config file, use defaults
	}

	var file Config
	if err := yaml.Unmarshal(data, &file); err != nil {
		return // bad config, use defaults
	}

	// merge: only override non-zero values
	if file.Namespace != "" {
		C.Namespace = file.Namespace
	}
	if file.MaxSlots != 0 {
		C.MaxSlots = file.MaxSlots
	}
	if file.LaunchDir != "" {
		C.LaunchDir = file.LaunchDir
	}
	if file.GitHubURL != "" {
		C.GitHubURL = file.GitHubURL
	}
	if len(file.Repos) > 0 {
		C.Repos = file.Repos
	}
	if len(file.AllowedAuthors) > 0 {
		C.AllowedAuthors = file.AllowedAuthors
	}
	if file.Scan.MinInterval.Duration != 0 {
		C.Scan.MinInterval = file.Scan.MinInterval
	}
	if file.Scan.MaxInterval.Duration != 0 {
		C.Scan.MaxInterval = file.Scan.MaxInterval
	}
	if file.Scan.TaskTTL.Duration != 0 {
		C.Scan.TaskTTL = file.Scan.TaskTTL
	}

	apply()
}

// apply pushes config values into package-level vars in other packages.
func apply() {
	types.Namespace = C.Namespace
	types.GitHubURL = C.GitHubURL
	repos := make([]types.Repo, len(C.Repos))
	for i, r := range C.Repos {
		repos[i] = types.Repo{Owner: r.Owner, Name: r.Name}
	}
	types.Repos = repos
}
