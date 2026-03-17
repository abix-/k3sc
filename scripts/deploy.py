"""deploy.py -- build the claude-agent image and deploy to k3s.

Run inside WSL2: python3 /mnt/c/code/k3s-claude/scripts/deploy.py
"""

import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
NERDCTL = "sudo nerdctl --address /run/k3s/containerd/containerd.sock --namespace k8s.io"
KUBECTL = "sudo k3s kubectl"


def run(cmd, check=True):
    print(f"  $ {cmd}")
    r = subprocess.run(cmd, shell=True)
    if check and r.returncode != 0:
        print(f"  FAILED (exit {r.returncode})")
        sys.exit(r.returncode)


def main():
    image_dir = REPO_ROOT / "image"
    manifests = REPO_ROOT / "manifests"
    scripts = REPO_ROOT / "scripts"

    print("=== building claude-agent image ===")
    run(f'{NERDCTL} build -t claude-agent:latest "{image_dir}"')

    print("\n=== applying namespace ===")
    run(f'{KUBECTL} apply -f "{manifests}/namespace.yaml"')

    print("\n=== applying PVCs ===")
    for pvc in sorted(manifests.glob("pvc-*.yaml")):
        run(f'{KUBECTL} apply -f "{pvc}"')

    print("\n=== creating configmap from scripts ===")
    run(f'{KUBECTL} create configmap dispatcher-scripts -n claude-agents '
        f'--from-file=dispatcher.py="{scripts}/dispatcher.py" '
        f'--from-file=job-template.yaml="{manifests}/job-template.yaml" '
        f'--dry-run=client -o yaml | {KUBECTL} apply -f -')

    print("\n=== applying dispatcher cronjob + RBAC ===")
    run(f'{KUBECTL} apply -f "{manifests}/dispatcher-cronjob.yaml"')

    print("""
=== deployment complete ===

next steps:
  1. create secret (if not already done):
     sudo k3s kubectl create secret generic claude-secrets -n claude-agents \\
       --from-literal=CLAUDE_CODE_OAUTH_TOKEN=<token> \\
       --from-literal=GITHUB_TOKEN=<token>

  2. test dispatcher manually:
     sudo k3s kubectl create job --from=cronjob/claude-dispatcher test-dispatch -n claude-agents

  3. watch pods:
     sudo k3s kubectl get pods -n claude-agents -w
""")


if __name__ == "__main__":
    main()
