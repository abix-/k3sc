# k3sc

Go CLI that orchestrates [Claude Code](https://docs.anthropic.com/en/docs/claude-code) agents as Kubernetes jobs on k3s. An operator watches GitHub issues across multiple repos, claims eligible ones, and spins up pods that autonomously implement, review, and hand off work.

Built for [Endless](https://github.com/abix-/endless), a real-time colony sim in Bevy/Rust.

## How it works

```
GitHub Issues (ready/needs-review labels)
        |
   [operator]
        |
   DispatchState reconciler creates/queues AgentJobs -> AgentJob reconciler creates k8s Jobs
        |
   [agent pod]
        |
        +-- clones repo
        +-- runs: claude or codex on the assigned issue
        +-- implements or reviews the issue
        +-- pushes branch, creates PR, hands off via labels
```

The operator runs two reconcilers in one process. A singleton `DispatchState` reconciler polls configured repos for workflow issues (`ready`, `needs-review`), backs off exponentially when idle (2min -> 1hr cap), cleans up orphans, and prepares `AgentJob` work items. The `AgentJob` reconciler claims the issue on GitHub, creates the k8s Job, watches it complete, and transitions labels for the next step.

Each agent pod gets a letter-based identity (claude-a, claude-b, ..., claude-z) and its own workspace on a shared PVC.

Windows-side PR review can also be reserved on demand. `k3sc take --worker claude-a` creates a central `ReviewLease` in k3s, mirrors the reservation to the PR via that exact worker label, and keeps the TUI aware of who owns the next manual review. Worker names must start with `claude-` or `codex-`.

## Subcommands

| Command | Description |
|---------|-------------|
| `k3sc top` | Live TUI dashboard -- agents, issues, PRs, operator logs |
| `k3sc top --once` | One-shot text output |
| `k3sc sessions` | Live TUI dashboard of running Claude Code sessions, `ccusage` totals, and the active 5-hour block |
| `k3sc sessions --once` | One-shot local Claude session snapshot with active block details |
| `k3sc sessions --once --timings` | Benchmark breakdown for process scan, metadata, per-session usage, and active block collection |
| `k3sc dispatch` | Scan GitHub, create jobs for eligible issues |
| `k3sc logs [repo] [issue]` | View agent pod logs (summary or repo-scoped per-issue) |
| `k3sc logs -f [repo] [issue]` | Follow logs live |
| `k3sc deploy` | Build container image and apply k8s manifests |
| `k3sc cargo-lock [args]` | Serialize cargo builds with a file lock (`run` auto-builds first) |
| `k3sc kill <issue>` | Kill running agent job and reset GitHub claim |
| `k3sc reset <issue>` | Remove orphaned GitHub claim (no k8s changes) |
| `k3sc pause` | Scale operator to 0 replicas |
| `k3sc resume` | Scale operator to 1 replica |
| `k3sc next` | Pick a random issue or PR that needs human review |
| `k3sc launch` | Launch a Windows-side Claude or Codex session in a free slot directory |
| `k3sc take --worker claude-a` | Reserve the next eligible open PR for a specific local review worker |
| `k3sc release --repo <repo> --pr <number>` | Release a local PR reservation and clear its owner label |

## TUI

The `top` command provides a live dashboard with sections for cluster status, quota, local PR reservations, operator output, GitHub issues, agent pods with live log tails, and open PRs. Hotkeys:

`q` quit | `n` dispatch now | `p` pause | `d` toggle dispatcher | `l` toggle live logs | `r` refresh | `+`/`-` adjust max agents

The `sessions` command provides a local live dashboard of running `claude.exe` processes, Claude session IDs from `~/.claude/sessions/<pid>.json`, per-session token/cost totals from `ccusage`, and the active Claude billing block with Max-style token-limit projections. Hotkeys:

`q` quit | `r` refresh | `1-9` copy session ID

## Architecture

- **Operator**: One controller-runtime manager running both the singleton dispatch reconciler and the per-issue `AgentJob` reconciler
- **Review leases**: Namespaced `ReviewLease` CRDs reserve open PRs for manual Windows review without relying on local lockfiles
- **Agent pods**: Ubuntu 24.04 with Node.js, Claude Code CLI, Rust toolchain, gh CLI, kubectl
- **Shared PVCs**: `workspaces` (git clones), `cargo-target` (build artifacts), `cargo-home` (crate registry)
- **Host mounts**: Claude skills, commands, docs, and CLAUDE.md mounted read-only from the host
- **Auth**: GitHub, Claude (OAuth, API key, or Bedrock), and Codex auth injected into pods from a k8s secret; pod auth does not depend on host-mounted token files; k3s secrets encryption recommended

## Configuration

Settings are read from `~/.k3sc.yaml`. All fields are optional -- missing file or fields use defaults.

```yaml
namespace: claude-agents     # k8s namespace
max_slots: 5                 # max concurrent agent pods
launch_dir: C:\code          # base dir for k3sc launch slot directories
repos:                       # repos to scan for issues
  - owner: abix-
    name: endless
  - owner: abix-
    name: k3sc
allowed_authors:            # only these issue authors may dispatch agent jobs
  - abix-
scan:
  min_interval: 2m           # fastest scan rate
  max_interval: 1h           # backoff cap when idle
  task_ttl: 24h              # cleanup completed tasks after this
```

The `MAX_SLOTS` env var overrides `max_slots` in the config (for k8s pod compatibility).

## Workflow labels

Issues are routed through a state machine via GitHub labels:

| Label | Meaning |
|-------|---------|
| `ready` | Available for an agent to claim |
| `claude-X` | Agent X is actively working on it |
| `needs-review` | Implementation done, needs another agent to review |
| `needs-human` | Requires human action (merge, design decision) |

The dispatch reconciler only picks up `ready` and `needs-review` issues from configured repos, and only when the issue author is in `allowed_authors` (prioritizing `needs-review`).

Open PR reservations are separate from issue dispatch. They are coordinated through `ReviewLease` CRDs and mirrored to the PR via the exact worker label, such as `claude-a` or `codex-b`.

## Prerequisites

- k3s running in WSL2 (Ubuntu 24.04) with secrets encryption enabled
- Go 1.25+ (for building the CLI)
- At least one Claude auth method:
  - Claude Code OAuth token (`claude setup-token`) for Max subscription
  - `ANTHROPIC_API_KEY` env var for direct API access
  - AWS credentials (`~/.aws/credentials`) for Bedrock
- Codex auth (`codex login`) or `OPENAI_API_KEY` (optional, for codex agent family)
- GitHub personal access token with repo scope

## Quick start

```bash
# install k3s in WSL2 with secrets encryption (protects API keys at rest)
wsl -d Ubuntu-24.04 -- bash -c "curl -sfL https://get.k3s.io | sh -s - --secrets-encryption"

# build CLI
cd /c/code/k3sc
go build -o k3sc.exe .

# cross-compile linux binary for container
GOOS=linux GOARCH=amd64 go build -o image/k3sc .

# ensure local auth exists (at least one Claude auth source required):
# - ~/.claude/.credentials.json (OAuth/subscription)
# - ANTHROPIC_API_KEY env var (direct API)
# - ~/.aws/credentials (Bedrock -- rotate-auth auto-sets CLAUDE_CODE_USE_BEDROCK=1)
# - ~/.gh-token (required)
# - ~/.codex/auth.json or OPENAI_API_KEY (optional)
#
# deploy will create/update the k8s claude-secrets secret from those local auth sources
sudo k3s kubectl apply -f manifests/namespace.yaml

# deploy (builds image, applies manifests)
k3sc deploy

# check status
k3sc top
```

## Project structure

```
cmd/              subcommand implementations (cobra)
internal/
  config/         settings file loader (~/.k3sc.yaml)
  dispatch/       slot allocation, template loading
  github/         GitHub API client (issues, PRs, labels)
  k8s/            Kubernetes client (pods, jobs, logs, CRs)
  operator/       controller-runtime reconcilers for dispatch + AgentJobs
  tui/            Bubbletea TUI model
  types/          shared types (Repo, Issue, AgentPod, etc.)
  format/         output formatting helpers
image/
  Dockerfile      agent container image
  entrypoint.sh   pod startup script
  claude-config/  CLAUDE.md baked into the image
manifests/        k8s manifests (namespace, PVCs, job template, CRD, operator)
```
