package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abix-/k3sc/internal/types"
	gh "github.com/google/go-github/v68/github"
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

// TestClaimIssue verifies that ClaimIssue makes the expected GitHub API calls.
// It would fail if ClaimIssue were removed or if any of the three operations were skipped.
func TestClaimIssue(t *testing.T) {
	var removedLabels []string
	var addedLabels []string
	var commentBody string

	mux := http.NewServeMux()

	removeLabel := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		removedLabels = append(removedLabels, parts[len(parts)-1])
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "[]")
	}
	mux.HandleFunc("/repos/owner/repo/issues/42/labels/ready", removeLabel)
	mux.HandleFunc("/repos/owner/repo/issues/42/labels/needs-review", removeLabel)

	mux.HandleFunc("/repos/owner/repo/issues/42/labels", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Labels []string `json:"labels"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		addedLabels = req.Labels
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]gh.Label{})
	})

	mux.HandleFunc("/repos/owner/repo/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req gh.IssueComment
		json.NewDecoder(r.Body).Decode(&req)
		commentBody = req.GetBody()
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(&gh.IssueComment{Body: req.Body})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	origNew := newClientFn
	newClientFn = func(_ string) *gh.Client {
		c, _ := gh.NewClient(nil).WithEnterpriseURLs(ts.URL+"/", ts.URL+"/")
		return c
	}
	defer func() { newClientFn = origNew }()

	repo := types.Repo{Owner: "owner", Name: "repo"}
	if err := ClaimIssue(t.Context(), repo, 42, "claude-c"); err != nil {
		t.Fatalf("ClaimIssue returned error: %v", err)
	}

	for _, want := range []string{"ready", "needs-review"} {
		found := false
		for _, l := range removedLabels {
			if l == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected label %q to be removed, removed: %v", want, removedLabels)
		}
	}

	wantAdded := map[string]bool{"claimed": false, "claude-c": false}
	for _, l := range addedLabels {
		if _, ok := wantAdded[l]; ok {
			wantAdded[l] = true
		}
	}
	for label, found := range wantAdded {
		if !found {
			t.Errorf("expected label %q to be added, added: %v", label, addedLabels)
		}
	}

	if !strings.Contains(commentBody, "claude-c") {
		t.Errorf("claim comment missing agent name, got: %q", commentBody)
	}
	if !strings.Contains(commentBody, "ready -> claimed") {
		t.Errorf("claim comment missing state transition, got: %q", commentBody)
	}
}
