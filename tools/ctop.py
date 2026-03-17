"""ctop -- Claude Top dashboard for k3s agent pods.

Run: wsl -d Ubuntu-24.04 -- python3 /mnt/c/code/k3s-claude/ctop.py
"""

import subprocess
import json
import sys
import os
from datetime import datetime, timezone, timedelta
from concurrent.futures import ThreadPoolExecutor

EST = timezone(timedelta(hours=-4))
NAMESPACE = "claude-agents"
REPO = "abix-/endless"
os.environ.setdefault("KUBECONFIG", "/etc/rancher/k3s/k3s.yaml")


def run(cmd):
    r = subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=15)
    return r.stdout.strip()


def kubectl_json(resource, extra=""):
    raw = run(f"sudo k3s kubectl get {resource} -n {NAMESPACE} {extra} -o json 2>/dev/null")
    return json.loads(raw) if raw else {}


def get_cluster():
    node = run("sudo k3s kubectl get nodes -o jsonpath='{.items[0].metadata.name} {.items[0].status.conditions[-1].type} {.items[0].status.nodeInfo.kubeletVersion}'")
    mem = run("free -h | awk '/Mem:/{print $3\"/\"$2}'")
    disk = run("df -h / | awk 'NR==2{print $3\"/\"$2}'")
    return node, mem, disk


def get_pods_json():
    return kubectl_json("pods", "-l app=claude-agent")


def get_cronjob():
    return kubectl_json("cronjob/claude-dispatcher", "")


def get_dispatcher_log():
    latest = run(f"sudo k3s kubectl get pods -n {NAMESPACE} --sort-by=.metadata.creationTimestamp -o name 2>/dev/null | grep dispatcher | tail -1")
    if latest:
        return run(f"sudo k3s kubectl logs {latest} -n {NAMESPACE} 2>/dev/null")
    return ""


def get_pod_log_tail(pod_name):
    raw = run(f"sudo k3s kubectl logs {pod_name} -n {NAMESPACE} --tail=20 2>/dev/null")
    lines = [l for l in raw.splitlines()
             if l.strip() and not l.startswith("[entrypoint]")
             and not l.startswith("[tool]") and not l.startswith("[result]")
             and not l.strip().endswith("/10")]
    return lines[-1][:80] if lines else ""


def get_issues():
    # gh CLI is on Windows, not WSL -- call gh.exe directly
    r = subprocess.run(
        ["gh.exe", "issue", "list", "--repo", REPO, "--state", "open",
         "--json", "number,title,labels",
         "--jq", '.[] | select(.labels | map(.name) | any(. == "ready" or . == "needs-review" or . == "needs-human" or . == "claimed"))'],
        capture_output=True, text=True, timeout=15
    )
    raw = r.stdout.strip()
    issues = []
    for line in raw.splitlines():
        try:
            obj = json.loads(line)
            labels = [l["name"] for l in obj.get("labels", [])]
            state = next((s for s in ["claimed", "needs-human", "needs-review", "ready"] if s in labels), "")
            owner = next((l for l in labels if l.startswith("claude-") or l.startswith("codex-")), "")
            issues.append({
                "number": obj["number"],
                "title": obj["title"][:60],
                "state": state,
                "owner": owner,
            })
        except json.JSONDecodeError:
            continue
    return sorted(issues, key=lambda i: i["number"])


def fmt_time(iso):
    if not iso:
        return ""
    dt = datetime.fromisoformat(iso.replace("Z", "+00:00")).astimezone(EST)
    h = dt.strftime("%I:%M %p EST").lstrip("0")
    return h


def fmt_duration(start_iso, end_iso=None):
    if not start_iso:
        return ""
    start = datetime.fromisoformat(start_iso.replace("Z", "+00:00"))
    end = datetime.fromisoformat(end_iso.replace("Z", "+00:00")) if end_iso else datetime.now(timezone.utc)
    delta = end - start
    mins = int(delta.total_seconds() // 60)
    secs = int(delta.total_seconds() % 60)
    return f"{mins}m {secs:02d}s"


def main():
    with ThreadPoolExecutor(max_workers=5) as ex:
        f_cluster = ex.submit(get_cluster)
        f_pods = ex.submit(get_pods_json)
        f_cron = ex.submit(get_cronjob)
        f_disp = ex.submit(get_dispatcher_log)
        f_issues = ex.submit(get_issues)

    node, mem, disk = f_cluster.result()
    pods_data = f_pods.result()
    cron = f_cron.result()
    disp_log = f_disp.result()
    issues = f_issues.result()

    print(f"=== CLUSTER ===")
    print(f"Node: {node}  RAM: {mem}  Disk: {disk}")
    print()

    # parse pods
    items = pods_data.get("items", [])
    pods = []
    for item in items:
        labels = item.get("metadata", {}).get("labels", {})
        status = item.get("status", {})
        phase = status.get("phase", "Unknown")
        start = status.get("startTime", "")
        term = (status.get("containerStatuses") or [{}])[0].get("state", {}).get("terminated", {})
        finish = term.get("finishedAt", "")
        pods.append({
            "name": item["metadata"]["name"],
            "issue": labels.get("issue-number", "?"),
            "slot": int(labels.get("agent-slot", "0")),
            "phase": phase,
            "start": start,
            "finish": finish,
        })

    # get log tails in parallel
    with ThreadPoolExecutor(max_workers=8) as ex:
        log_futures = {p["name"]: ex.submit(get_pod_log_tail, p["name"]) for p in pods}
    for p in pods:
        p["tail"] = log_futures[p["name"]].result()

    # sort: running first, then completed, then failed
    order = {"Running": 0, "Pending": 0, "Succeeded": 1, "Failed": 2}
    pods.sort(key=lambda p: (order.get(p["phase"], 3), p["start"]))
    running = [p for p in pods if p["phase"] in ("Running", "Pending")]
    completed = [p for p in pods if p["phase"] == "Succeeded"]
    failed = [p for p in pods if p["phase"] == "Failed"]

    print(f"=== AGENTS ({len(running)} running, {len(completed)} completed, {len(failed)} failed) ===")
    if pods:
        print(f"{'Issue':<7} {'Agent':<10} {'Status':<11} {'Started':<16} {'Duration':<10} Last Output")
        for p in pods:
            agent = f"claude-{p['slot'] + 5}"
            status_str = "Running" if p["phase"] in ("Running", "Pending") else ("Completed" if p["phase"] == "Succeeded" else "Failed")
            started = fmt_time(p["start"])
            duration = fmt_duration(p["start"], p["finish"] if p["finish"] else None)
            tail = p["tail"]
            if len(tail) > 50:
                tail = tail[:47] + "..."
            print(f"#{p['issue']:<6} {agent:<10} {status_str:<11} {started:<16} {duration:<10} {tail}")
    else:
        print("  (no agent pods)")
    print()

    # dispatcher
    schedule = cron.get("spec", {}).get("schedule", "?")
    last_run = cron.get("status", {}).get("lastScheduleTime", "")
    max_slots = "?"
    for line in disp_log.splitlines():
        if "max capacity" in line:
            max_slots = line.split("(")[1].split(")")[0] if "(" in line else "?"
            break

    disp_summary = ""
    for line in disp_log.splitlines():
        if "eligible issues:" in line:
            disp_summary = line.split("] ", 1)[1] if "] " in line else line
        if "no eligible" in line:
            disp_summary = line.split("] ", 1)[1] if "] " in line else line
        if "at max capacity" in line:
            disp_summary += " | stopped at max"
        if "scan complete" in line:
            pass

    print(f"=== DISPATCHER ===")
    print(f"Schedule: {schedule}  |  Last run: {fmt_time(last_run)}  |  Max slots: {max_slots}")
    if disp_summary:
        print(f"  {disp_summary}")
    print()

    # issues
    print(f"=== GITHUB ISSUES ===")
    if issues:
        print(f"{'Issue':<7} {'State':<14} {'Owner':<10} Title")
        for i in issues:
            print(f"#{i['number']:<6} {i['state']:<14} {i['owner']:<10} {i['title']}")
    else:
        print("  (no issues with workflow labels)")
    print()


if __name__ == "__main__":
    main()
