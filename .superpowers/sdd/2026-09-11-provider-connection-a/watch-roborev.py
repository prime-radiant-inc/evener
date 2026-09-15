#!/usr/bin/env python3
"""Wait for roborev's PR review to change after this branch's current head.

Roborev keeps ONE sticky comment on the PR and rewrites it on every review, so a
change in that comment's body (or in the head it names) is the signal that a new
verdict is ready. This script snapshots that state, polls `gh` until it changes,
prints a short triage summary, and exits - so the Evener job notification IS the
wake-up, instead of a blind interval timer that only tells you to go look.

Run it in the background after every push. Exit codes:

  0  the review changed (a new verdict for the new head, or an updated one)
  3  timed out with no change (current state printed; re-arm for another window)
  2  could not read the PR at all (gh missing/unauthenticated/no network)

Usage:
  python3 watch-roborev.py [--pr 1136] [--timeout 2700] [--interval 30]
"""

from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import sys
import time

BOT_LOGIN = "roborev-primeradiant"


def roborev_comment(pr: int) -> tuple[str | None, str | None]:
    """Return (body, head) of roborev's sticky comment, or (None, None)."""
    result = subprocess.run(
        ["gh", "pr", "view", str(pr), "--json", "comments"],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(f"gh pr view failed: {result.stderr.strip() or result.returncode}")
    comments = json.loads(result.stdout).get("comments", [])
    for comment in reversed(comments):
        if comment.get("author", {}).get("login") == BOT_LOGIN:
            body = comment.get("body", "")
            head = None
            marker = "roborev: Combined Review (`"
            start = body.find(marker)
            if start != -1:
                head = body[start + len(marker) : start + len(marker) + 40].split("`")[0]
            return body, head
    return None, None


def digest(body: str | None) -> str:
    return hashlib.sha256((body or "").encode()).hexdigest()[:16]


def summarize(body: str) -> str:
    """A few lines of triage: the verdict sentence and every finding headline."""
    out: list[str] = []
    for line in body.splitlines():
        stripped = line.strip()
        if (
            not stripped
            or stripped.startswith("<!--")  # the sticky marker
            or stripped.startswith("#")  # "## Verdict" headings carry no text
            or stripped.startswith("*Reviewers")
        ):
            continue
        if stripped.startswith("**Verdict") or stripped.startswith("Verdict"):
            out.append(stripped.strip("*").strip())
        elif "No issues found" in stripped:
            out.append(f"Verdict: {stripped.strip('*')}")
        elif stripped.startswith("- **"):
            out.append(stripped)
    if not out:
        out.append("(no verdict line found; read the full body)")
    return "\n".join(dict.fromkeys(out))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pr", type=int, default=1136, help="pull request number")
    parser.add_argument("--timeout", type=int, default=2700, help="seconds to wait for a change")
    parser.add_argument("--interval", type=int, default=30, help="seconds between checks")
    args = parser.parse_args()

    try:
        body, head = roborev_comment(args.pr)
    except RuntimeError as exc:
        print(str(exc), file=sys.stderr)
        return 2

    started = digest(body)
    started_head = head
    print(f"watching PR #{args.pr}: baseline head={head or 'none'} digest={started}", flush=True)

    deadline = time.monotonic() + args.timeout
    while time.monotonic() < deadline:
        time.sleep(args.interval)
        try:
            body, head = roborev_comment(args.pr)
        except RuntimeError as exc:
            print(f"poll failed, continuing: {exc}", file=sys.stderr, flush=True)
            continue
        if digest(body) != started:
            print(f"CHANGED: head={head or 'none'} digest={digest(body)} (was {started})", flush=True)
            print("--- triage ---", flush=True)
            print(summarize(body or ""), flush=True)
            print("--- end (read the full body with: gh pr view %d --json comments) ---" % args.pr, flush=True)
            return 0

    print(f"NO CHANGE within {args.timeout}s: head={head or 'none'} digest={digest(body)}", flush=True)
    return 3


if __name__ == "__main__":
    sys.exit(main())
