"""dispatcher.py -- find eligible GitHub issues and create k8s Jobs.

Runs as a CronJob every 3 minutes inside the claude-agents namespace.

GitHub labels are the source of truth. The dispatcher only reads labels
to find eligible issues. It does NOT use job existence for dedup --
once a pod claims an issue, the label changes to 'claimed' and the
issue drops out of the eligible list naturally.
"""

import json
import subprocess
import sys
import time
import os

REPO = "abix-/endless"
NAMESPACE = "claude-agents"
MAX_SLOTS = 3
JOB_TEMPLATE = "/etc/dispatcher/job-template.yaml"
TIMESTAMP = str(int(time.time()))


def run(cmd):
    r = subprocess.run(cmd, shell=True, capture_output=True, text=True)
    return r.stdout.strip()


def log(msg):
    os.environ["TZ"] = "America/New_York"
    t = time.strftime("%Y-%m-%d %H:%M:%S %Z")
    print(f"[dispatcher] {t} {msg}", flush=True)


def get_issues(label):
    raw = run(f'gh issue list --repo {REPO} --label {label} --state open --json number --jq ".[].number"')
    if not raw:
        return []
    return sorted(int(n) for n in raw.splitlines() if n.strip())


def get_active_slots():
    raw = run(f"kubectl get jobs -n {NAMESPACE} -l app=claude-agent "
              f"--field-selector=status.active=1 "
              f"-o jsonpath='{{.items[*].metadata.labels.agent-slot}}'")
    if not raw:
        return []
    return [int(s) for s in raw.split() if s.strip()]


def create_job(issue, slot):
    with open(JOB_TEMPLATE) as f:
        manifest = f.read()

    manifest = manifest.replace("__ISSUE_NUMBER__", str(issue))
    manifest = manifest.replace("__AGENT_SLOT__", str(slot))
    # unique job name so completed jobs don't collide
    manifest = manifest.replace(
        f'name: "claude-issue-{issue}"',
        f'name: "claude-issue-{issue}-{TIMESTAMP}"'
    )

    r = subprocess.run(
        f"kubectl apply -n {NAMESPACE} -f -",
        shell=True, input=manifest, capture_output=True, text=True
    )
    print(f"  {r.stdout.strip()}", flush=True)
    if r.returncode != 0 and r.stderr:
        print(f"  ERROR: {r.stderr.strip()}", flush=True)


def main():
    log("starting scan")

    # needs-review first (matches /issue claim algorithm), then ready
    review_issues = get_issues("needs-review")
    ready_issues = get_issues("ready")
    all_issues = review_issues + [i for i in ready_issues if i not in review_issues]

    if not all_issues:
        log("no eligible issues found")
        return

    log(f"eligible issues: {' '.join(str(i) for i in all_issues)}")

    active_slots = get_active_slots()
    log(f"active jobs: {len(active_slots)}, slots in use: {' '.join(str(s) for s in active_slots)}")

    created = 0
    for issue in all_issues:
        if len(active_slots) >= MAX_SLOTS:
            log(f"at max capacity ({MAX_SLOTS}), stopping")
            break

        # find free slot
        slot = None
        for i in range(1, MAX_SLOTS + 1):
            if i not in active_slots:
                slot = i
                break

        if slot is None:
            log("no free slots available")
            break

        log(f"creating job for issue {issue} in slot {slot}")
        create_job(issue, slot)
        active_slots.append(slot)
        created += 1

    log(f"scan complete -- created {created} jobs")


if __name__ == "__main__":
    main()
