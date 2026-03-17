"""View claude agent pod logs from k3s.

Usage:
    python logs.py              -- summary of all recent pods
    python logs.py 120          -- full logs for issue 120
    python logs.py -f 120       -- follow live logs for issue 120
    python logs.py all          -- full logs for all completed pods
"""

import subprocess
import sys
import json


def kubectl(*args):
    cmd = ["wsl", "-d", "Ubuntu-24.04", "--", "sudo", "k3s", "kubectl"] + list(args)
    result = subprocess.run(cmd, capture_output=True, text=True)
    return result.stdout.strip(), result.stderr.strip()


def get_pods():
    out, _ = kubectl(
        "get", "pods", "-n", "claude-agents", "-l", "app=claude-agent",
        "-o", "json"
    )
    if not out:
        return []
    data = json.loads(out)
    pods = []
    for item in data.get("items", []):
        labels = item.get("metadata", {}).get("labels", {})
        status = item.get("status", {}).get("phase", "Unknown")
        name = item.get("metadata", {}).get("name", "")
        pods.append({
            "name": name,
            "issue": labels.get("issue-number", "?"),
            "slot": labels.get("agent-slot", "?"),
            "status": status,
            "created": item.get("metadata", {}).get("creationTimestamp", ""),
        })
    return sorted(pods, key=lambda p: p["created"])


def get_logs(issue):
    # find pod(s) by issue label, get most recent
    out, _ = kubectl(
        "get", "pods", "-n", "claude-agents",
        "-l", f"issue-number={issue}",
        "--sort-by=.metadata.creationTimestamp",
        "-o", "jsonpath={.items[-1].metadata.name}"
    )
    if not out:
        return f"No pods found for issue {issue}"
    pod_name = out.strip()
    out, err = kubectl("logs", pod_name, "-n", "claude-agents")
    return out or err


def follow_logs(issue):
    # find most recent pod for this issue
    out, _ = kubectl(
        "get", "pods", "-n", "claude-agents",
        "-l", f"issue-number={issue}",
        "--sort-by=.metadata.creationTimestamp",
        "-o", "jsonpath={.items[-1].metadata.name}"
    )
    if not out:
        print(f"No pods found for issue {issue}")
        return
    pod_name = out.strip()
    cmd = [
        "wsl", "-d", "Ubuntu-24.04", "--",
        "sudo", "k3s", "kubectl", "logs", "-f",
        pod_name, "-n", "claude-agents"
    ]
    try:
        subprocess.run(cmd)
    except KeyboardInterrupt:
        pass


def summary():
    pods = get_pods()
    if not pods:
        print("No agent pods found.")
        return

    running = [p for p in pods if p["status"] == "Running"]
    completed = [p for p in pods if p["status"] == "Succeeded"]
    failed = [p for p in pods if p["status"] == "Failed"]

    if running:
        print("RUNNING")
        print("-" * 60)
        for p in running:
            slot = int(p["slot"]) + 5
            print(f"  issue #{p['issue']:>4}  claude-{slot}  {p['name']}")
        print()

    if completed:
        print("COMPLETED")
        print("-" * 60)
        for p in completed:
            slot = int(p["slot"]) + 5
            # get last meaningful line of output
            logs = get_logs(p["issue"])
            last_lines = [l for l in logs.splitlines() if l.strip() and not l.startswith("[entrypoint]")]
            tail = last_lines[-1] if last_lines else "(no output)"
            if len(tail) > 80:
                tail = tail[:77] + "..."
            print(f"  issue #{p['issue']:>4}  claude-{slot}  {tail}")
        print()

    if failed:
        print("FAILED")
        print("-" * 60)
        for p in failed:
            slot = int(p["slot"]) + 5
            logs = get_logs(p["issue"])
            last_lines = [l for l in logs.splitlines() if l.strip()]
            tail = last_lines[-1] if last_lines else "(no output)"
            if len(tail) > 80:
                tail = tail[:77] + "..."
            print(f"  issue #{p['issue']:>4}  claude-{slot}  {tail}")
        print()

    print(f"Total: {len(running)} running, {len(completed)} completed, {len(failed)} failed")
    print()
    print("Usage:")
    print("  python logs.py <issue#>     full logs")
    print("  python logs.py -f <issue#>  follow live")
    print("  python logs.py all          all logs")


def show_all():
    pods = get_pods()
    for p in pods:
        slot = int(p["slot"]) + 5
        print("=" * 64)
        print(f"  issue #{p['issue']}  claude-{slot}  [{p['status']}]")
        print("=" * 64)
        print(get_logs(p["issue"]))
        print()


def main():
    args = sys.argv[1:]

    if not args:
        summary()
    elif args[0] == "-f" and len(args) > 1:
        follow_logs(args[1])
    elif args[0] == "all":
        show_all()
    else:
        print(get_logs(args[0]))


if __name__ == "__main__":
    main()
