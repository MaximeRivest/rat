#!/usr/bin/env python3
"""
Slack → pi bridge.

Watches the Slack kernel's activity log for inbound messages and
forwards them to a pi kernel for decision-making. Pi's response
is sent back to Slack automatically.

Usage:
  python3 slack-bridge.py --slack slack@amrmdtest --pi pi@amrmdtest
"""

import argparse
import json
import os
import subprocess
import sys
import time


def find_activity_log(kernel_name):
    """Find the activity.jsonl for a kernel."""
    cache = os.path.expanduser(f"~/.cache/rat/kernels/{kernel_name}/activity.jsonl")
    if os.path.exists(cache):
        return cache
    return None


def tail_activity(path, pos=0):
    """Read new entries from activity.jsonl since pos."""
    try:
        with open(path, "r") as f:
            f.seek(pos)
            lines = f.readlines()
            new_pos = f.tell()
    except (FileNotFoundError, OSError):
        return [], pos

    entries = []
    for line in lines:
        line = line.strip()
        if not line:
            continue
        try:
            entries.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    return entries, new_pos


def seek_to_end(path):
    """Get the current end position of the file."""
    try:
        with open(path, "r") as f:
            f.seek(0, 2)
            return f.tell()
    except (FileNotFoundError, OSError):
        return 0


def ask_pi(pi_kernel, from_user, channel, text):
    """Ask pi what to reply. Returns the response text or None."""
    # Single-line prompt to avoid shell escaping issues.
    prompt = f"Slack message from {from_user} in {channel}: \"{text}\" — Write a short reply. If no reply is needed, respond with exactly: NO_REPLY"

    try:
        result = subprocess.run(
            ["rat", "run", pi_kernel, prompt],
            capture_output=True, text=True, timeout=120
        )
        response = result.stdout.strip()
        # rat run adds status lines at the end — strip them.
        # The actual response is everything before the last line with ✓/✗
        lines = response.split("\n")
        # Remove trailing status lines (✓ 2.3s, claude-opus... etc.)
        while lines and (lines[-1].startswith("✓") or lines[-1].startswith("✗")
                         or lines[-1].startswith("claude-") or lines[-1].startswith("anthropic")
                         or lines[-1].strip() == ""):
            lines.pop()
        response = "\n".join(lines).strip()

        if not response or "NO_REPLY" in response:
            return None
        return response
    except subprocess.TimeoutExpired:
        print("  pi: timed out", file=sys.stderr)
        return None
    except Exception as e:
        print(f"  pi: {e}", file=sys.stderr)
        return None


def send_slack(slack_kernel, from_user, channel, text):
    """Send a reply back to Slack."""
    if channel == "DM":
        code = f"/dm @{from_user} {text}"
    else:
        code = text

    try:
        result = subprocess.run(
            ["rat", "run", slack_kernel, code],
            capture_output=True, text=True, timeout=30
        )
        ok = result.returncode == 0
        status = "✓" if ok else "✗"
        print(f"  {status} slack: {result.stdout.strip()[:100]}")
        if not ok:
            print(f"    {result.stderr.strip()[:100]}", file=sys.stderr)
    except Exception as e:
        print(f"  ✗ slack: {e}", file=sys.stderr)


def main():
    parser = argparse.ArgumentParser(description="Slack → pi bridge")
    parser.add_argument("--slack", required=True, help="Slack kernel name")
    parser.add_argument("--pi", required=True, help="Pi kernel name")
    parser.add_argument("--poll", type=float, default=1.0, help="Poll interval seconds")
    args = parser.parse_args()

    log_path = find_activity_log(args.slack)
    if not log_path:
        print(f"Activity log not found for {args.slack}", file=sys.stderr)
        sys.exit(1)

    print(f"slack→pi bridge")
    print(f"  slack: {args.slack}")
    print(f"  pi:    {args.pi}")
    print()

    pos = seek_to_end(log_path)

    while True:
        entries, pos = tail_activity(log_path, pos)
        for entry in entries:
            if entry.get("event") != "message":
                continue
            data = entry.get("data", {})
            from_user = data.get("from", "")
            text = data.get("text", "")
            channel = data.get("channel", "")
            print(f"← {from_user} @{channel}: {text}")

            reply = ask_pi(args.pi, from_user, channel, text)
            if reply:
                print(f"→ {reply[:100]}")
                send_slack(args.slack, from_user, channel, reply)
            else:
                print(f"  (no reply)")

        time.sleep(args.poll)


if __name__ == "__main__":
    main()
