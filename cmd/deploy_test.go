package cmd

import (
	"strings"
	"testing"
)

func TestPVCPrefix(t *testing.T) {
	cases := []struct {
		name   string
		isPVC  bool
	}{
		{"pvc-claude-a.yaml", true},
		{"pvc-claude-b.yaml", true},
		{"pvc-foo", true},
		{"namespace.yaml", false},
		{"dispatcher-cronjob.yaml", false},
		{"job-template.yaml", false},
	}
	for _, c := range cases {
		got := strings.HasPrefix(c.name, "pvc-")
		if got != c.isPVC {
			t.Errorf("HasPrefix(%q, \"pvc-\") = %v, want %v", c.name, got, c.isPVC)
		}
	}
}
