package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPVCPrefix(t *testing.T) {
	cases := []struct {
		name  string
		isPVC bool
	}{
		{"pvc-claude-a.yaml", true},
		{"pvc-claude-b.yaml", true},
		{"pvc-foo", true},
		{"namespace.yaml", false},
		{"operator-deployment.yaml", false},
		{"job-template.yaml", false},
	}
	for _, c := range cases {
		got := strings.HasPrefix(c.name, "pvc-")
		if got != c.isPVC {
			t.Errorf("HasPrefix(%q, \"pvc-\") = %v, want %v", c.name, got, c.isPVC)
		}
	}
}

func TestFindRepoRoot(t *testing.T) {
	// create temp dir tree: root/sub/sub2
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	sub2 := filepath.Join(sub, "sub2")
	if err := os.MkdirAll(sub2, 0755); err != nil {
		t.Fatal(err)
	}

	// place go.mod in root
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// change to sub2 and verify findRepoRoot walks up to root
	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	if err := os.Chdir(sub2); err != nil {
		t.Fatal(err)
	}

	got, err := findRepoRoot()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != root {
		t.Fatalf("expected %q, got %q", root, got)
	}
}

func TestFindRepoRootFromRepoDir(t *testing.T) {
	orig, _ := os.Getwd()
	defer os.Chdir(orig)

	got, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot from repo root returned error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(got, "go.mod")); statErr != nil {
		t.Fatalf("returned path %q has no go.mod", got)
	}
}
