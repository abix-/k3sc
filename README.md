# k3sc

Go CLI that orchestrates [Claude Code](https://docs.anthropic.com/en/docs/claude-code) agents as Kubernetes jobs on k3s. An operator watches GitHub issues across multiple repos, claims eligible ones, and spins up pods that autonomously implement, review, and hand off work.

Built for [Endless](https://github.com/abix-/endless), a real-time colony sim in Bevy/Rust.

## How it works

```
GitHub Issues (ready/needs-review labels)
        |
   [operator + scanner goroutine]
        |
   creates ClaudeTask CRs -> reconciler creates k8s Jobs
        |
   [claude-agent pod]
        |
        +-- clones repo
        +-- runs: claude --dangerously-skip-permissions -p "/issue 42"
        +-- implements or reviews the issue
        +-- pushes branch, creates PR, hands off via labels
```

The scanner polls all configured repos for open issues with workflow labels (`ready`, `needs-review`). It assigns each to a free slot and creates a ClaudeTask CR, which the reconciler picks up to claim the issue on GitHub and create a k8s Job. When idle, the scanner backs off exponentially (2min -> 1hr cap) to minimize GitHub API usage.

Each agent pod gets a letter-based identity (claude-a, claude-b, ..., claude-z) and its own workspace on a shared PVC.

## Subcommands

| Command | Description |
|---------|-------------|
| `k3sc top` | Live TUI dashboard -- agents, issues, PRs, operator logs |
| `k3sc top --once` | One-shot text output |
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
| `k3sc launch` | Launch a Windows-side Claude session in a free slot directory |

## TUI

The `top` command provides a live dashboard with sections for cluster status, operator output, GitHub issues, agent pods with live log tails, and open PRs. Hotkeys:

`q` quit | `n` dispatch now | `p` pause | `d` toggle dispatcher | `l` toggle live logs | `r` refresh | `+`/`-` adjust max agents

## Architecture

- **Operator**: Deployment running a controller-runtime reconciler + scanner goroutine
- **Agent pods**: Ubuntu 24.04 with Node.js, Claude Code CLI, Rust toolchain, gh CLI, kubectl
- **Shared PVCs**: `workspaces` (git clones), `cargo-target` (build artifacts), `cargo-home` (crate registry)
- **Host mounts**: Claude skills, commands, docs, and CLAUDE.md mounted read-only from the host
- **Auth**: Claude Code OAuth token via k8s secret, GitHub token via host-mounted file

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

The scanner only picks up `ready` and `needs-review` issues (prioritizing `needs-review`).

## Prerequisites

- k3s running in WSL2 (Ubuntu 24.04)
- Go 1.25+ (for building the CLI)
- Claude Code OAuth token (`claude setup-token`)
- GitHub personal access token with repo scope

## Quick start

```bash
# build CLI
cd /c/code/k3sc
go build -o k3sc.exe .

# cross-compile linux binary for container
GOOS=linux GOARCH=amd64 go build -o image/k3sc .

# create namespace + secrets (one-time)
sudo k3s kubectl apply -f manifests/namespace.yaml
sudo k3s kubectl create secret generic claude-secrets -n claude-agents \
  --from-literal=CLAUDE_CODE_OAUTH_TOKEN=<token> \
  --from-literal=GITHUB_TOKEN=<token>

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
  operator/       controller-runtime reconciler + scanner
  tui/            Bubbletea TUI model
  types/          shared types (Repo, Issue, AgentPod, etc.)
  format/         output formatting helpers
image/
  Dockerfile      agent container image
  entrypoint.sh   pod startup script
  claude-config/  CLAUDE.md baked into the image
manifests/        k8s manifests (namespace, PVCs, job template, CRD, operator)
```
