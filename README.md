# k3sc

Go CLI that orchestrates [Claude Code](https://docs.anthropic.com/en/docs/claude-code) agents as Kubernetes jobs on k3s. A dispatcher watches GitHub issues across multiple repos, claims eligible ones, and spins up pods that autonomously implement, review, and hand off work.

Built for [Endless](https://github.com/abix-/endless), a real-time colony sim in Bevy/Rust.

## How it works

```
GitHub Issues (ready/needs-review labels)
        |
   [dispatcher]  <-- CronJob, every 3 min
        |
   creates k8s Jobs (up to N concurrent slots)
        |
   [claude-agent pod]
        |
        +-- clones repo
        +-- runs: claude --dangerously-skip-permissions -p "/issue 42"
        +-- implements or reviews the issue
        +-- pushes branch, creates PR, hands off via labels
```

The dispatcher scans all configured repos (currently bix-/endless and bix-/k3sc) for open issues with workflow labels (eady, 
eeds-review). It assigns each to a free slot and creates a k8s Job from a template. If recent agent failures show Claude's extra-usage limit message, it backs its CronJob off to an hourly schedule, records the reset time, and restores the normal */3 * * * * cadence after the reset window passes.

Each agent pod gets a letter-based identity (claude-a, claude-b, ..., claude-z) and its own workspace on a shared PVC.

## Subcommands

| Command | Description |
|---------|-------------|
| `k3sc top` | Live TUI dashboard -- agents, issues, PRs, dispatcher logs |
| `k3sc top --once` | One-shot text output |
| `k3sc dispatch` | Scan GitHub, create jobs for eligible issues |
| `k3sc logs [repo] [issue]` | View agent pod logs (summary or repo-scoped per-issue) |
| `k3sc logs -f [repo] [issue]` | Follow logs live |
| `k3sc deploy` | Build container image and apply k8s manifests |
| `k3sc cargo-lock [args]` | Serialize cargo builds with a file lock |

## TUI

The `top` command provides a live dashboard with sections for cluster status, dispatcher output, GitHub issues, agent pods with live log tails, and open PRs. Hotkeys:

`q` quit | `n` dispatch now | `p` pause | `d` toggle dispatcher | `l` toggle live logs | `r` refresh | `+`/`-` adjust max agents

## Architecture

- **Dispatcher**: k8s CronJob running `k3sc dispatch` inside the same container image
- **Agent pods**: Ubuntu 24.04 with Node.js, Claude Code CLI, Rust toolchain, gh CLI, kubectl
- **Shared PVCs**: `workspaces` (git clones), `cargo-target` (build artifacts), `cargo-home` (crate registry)
- **Host mounts**: Claude skills, commands, docs, and CLAUDE.md mounted read-only from the host
- **Auth**: Claude Code OAuth token via k8s secret, GitHub token via host-mounted file

## Workflow labels

Issues are routed through a state machine via GitHub labels:

| Label | Meaning |
|-------|---------|
| `ready` | Available for an agent to claim |
| `claimed` | An agent is actively working on it |
| `needs-review` | Implementation done, needs another agent to review |
| `needs-human` | Requires human action (merge, design decision) |

The dispatcher only picks up `ready` and `needs-review` issues (prioritizing `needs-review`).

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
  github/         GitHub API client (issues, PRs across repos)
  k8s/            Kubernetes client (pods, jobs, logs)
  tui/            Bubbletea TUI model
  types/          shared types (Repo, Issue, AgentPod, etc.)
image/
  Dockerfile      agent container image
  entrypoint.sh   pod startup script
  claude-config/  CLAUDE.md baked into the image
manifests/        k8s manifests (namespace, PVCs, job template, dispatcher)
```
