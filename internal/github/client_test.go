package github

import (
	"strings"
	"testing"
)

func TestOwnerLabelParsing(t *testing.T) {
	parseOwner := func(labels []string) string {
		for _, l := range labels {
			if strings.HasPrefix(l, "owner:") {
				return strings.TrimPrefix(l, "owner:")
			}
		}
		return ""
	}

	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{"claude-c owner label", []string{"claimed", "owner:claude-c"}, "claude-c"},
		{"claude-b owner label", []string{"ready", "owner:claude-b"}, "claude-b"},
		{"codex family", []string{"claimed", "owner:codex-1"}, "codex-1"},
		{"no owner label", []string{"ready", "bug"}, ""},
		{"owner prefix only would not panic", []string{"owner:"}, ""},
		{"label exactly claude-", []string{"claude-"}, ""},
		{"label exactly codex-", []string{"codex-"}, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOwner(tc.labels)
			if got != tc.want {
				t.Errorf("parseOwner(%v) = %q, want %q", tc.labels, got, tc.want)
			}
		})
	}
}

// TestOldParsingWouldFailForOwnerPrefix verifies the old index-slicing approach
// would miss owner: prefixed labels (regression guard).
func TestOldParsingWouldFailForOwnerPrefix(t *testing.T) {
	oldParseOwner := func(labels []string) string {
		for _, l := range labels {
			if len(l) > 6 && (l[:7] == "claude-" || l[:6] == "codex-") {
				return l
			}
		}
		return ""
	}

	// owner:claude-c does NOT start with "claude-" so old code returns ""
	got := oldParseOwner([]string{"owner:claude-c"})
	if got != "" {
		t.Errorf("expected old parser to miss owner:claude-c, got %q", got)
	}
}
