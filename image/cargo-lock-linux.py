"""Serialize cargo builds across agent pods using a lockfile.

Usage: python3 cargo-lock.py build --release
       python3 cargo-lock.py check
       python3 cargo-lock.py clippy --release -- -D warnings

Holds an exclusive lock on the shared target dir for the duration
of the cargo command. Other pods wait in line.

Linux port -- uses fcntl.flock instead of msvcrt.
"""

import sys
import os
import subprocess
import time
import fcntl

LOCKFILE = os.path.join(
    os.environ.get("CARGO_TARGET_DIR", "/cargo-target"),
    ".cargo-build.lock"
)


def main():
    if not sys.argv[1:]:
        print("usage: python3 cargo-lock.py <cargo subcommand> [args...]", file=sys.stderr)
        sys.exit(1)

    os.makedirs(os.path.dirname(LOCKFILE), exist_ok=True)

    with open(LOCKFILE, "w") as f:
        ts = time.strftime("%H:%M:%S")
        print(f"[cargo-lock] {ts} waiting for build lock...", file=sys.stderr)

        fcntl.flock(f.fileno(), fcntl.LOCK_EX)

        ts = time.strftime("%H:%M:%S")
        cmd = ["cargo"] + sys.argv[1:]
        print(f"[cargo-lock] {ts} acquired lock, running: {' '.join(cmd)}", file=sys.stderr)

        try:
            result = subprocess.run(cmd)
        finally:
            fcntl.flock(f.fileno(), fcntl.LOCK_UN)
            ts = time.strftime("%H:%M:%S")
            print(f"[cargo-lock] {ts} released lock", file=sys.stderr)

        sys.exit(result.returncode)


if __name__ == "__main__":
    main()
