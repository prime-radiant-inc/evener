#!/usr/bin/env python3
"""Binary search for the commit that caused an eval task regression.

Like git-bisect, but uses eval scores instead of pass/fail. Builds from
each candidate commit in a worktree, launches evals on AWS, polls for
scores, and narrows the range until it finds the first bad commit.

Usage:
  python3 tools/eval_bisect.py \\
    --task build-cython-ext \\
    --good 7ead614 \\
    --bad HEAD \\
    --reps 5 \\
    --threshold 0.8

  # Preview without launching:
  python3 tools/eval_bisect.py --task X --good A --bad B --dry-run
"""

import argparse
import importlib.util
import os
import shutil
import subprocess
import sys
import time

# Paths that affect the shipped binary or agent behavior.
# Commits that only touch other files can't cause eval regressions.
AGENT_RELEVANT_PATHS = [
    "agent/",
    "cmd/",
    "llm/",
    "go.mod",
    "go.sum",
    "tools/serf_agent.py",
    "tools/install-serf.sh.j2",
]

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HARBOR_DIR_DEFAULT = os.path.expanduser("~/prime-radiant/harbor-runner")


def log(msg=""):
    """Print with immediate flush so output appears in real time."""
    print(msg, flush=True)


def get_commit_range(good, bad, filter_agent=True, repo=None):
    """Return commits between good (exclusive) and bad (inclusive), oldest first.

    If filter_agent is True, only include commits that touch agent-relevant paths.
    """
    repo = repo or REPO_ROOT
    result = subprocess.run(
        ["git", "rev-list", "--ancestry-path", f"{good}..{bad}"],
        capture_output=True, text=True, cwd=repo,
    )
    if result.returncode != 0:
        raise RuntimeError(f"git rev-list failed: {result.stderr.strip()}")

    # rev-list returns newest-first; reverse to get oldest-first
    commits = [line.strip() for line in result.stdout.strip().split("\n") if line.strip()]
    commits.reverse()

    if not filter_agent:
        return commits

    filtered = []
    for sha in commits:
        diff_result = subprocess.run(
            ["git", "diff-tree", "--no-commit-id", "--name-only", "-r", sha],
            capture_output=True, text=True, cwd=repo,
        )
        paths = diff_result.stdout.strip().split("\n")
        if any(p.startswith(prefix) for p in paths for prefix in AGENT_RELEVANT_PATHS):
            filtered.append(sha)

    return filtered


def make_run_id(task, sha, step):
    """Generate a bisect run ID: bisect-{task}-{sha7}-s{step}."""
    short = sha[:7]
    return f"bisect-{task}-{short}-s{step}"


def compute_score(rep_scores, expected_reps):
    """Compute mean score from rep results. Returns None if no scores."""
    if not rep_scores:
        return None
    values = list(rep_scores.values())
    return sum(values) / len(values)


def commit_summary(sha, repo=None):
    """Return one-line summary for a commit."""
    repo = repo or REPO_ROOT
    result = subprocess.run(
        ["git", "log", "--oneline", "-1", sha],
        capture_output=True, text=True, cwd=repo,
    )
    return result.stdout.strip()


def commit_date(sha, repo=None):
    """Return commit date in YYYY-MM-DD format."""
    repo = repo or REPO_ROOT
    result = subprocess.run(
        ["git", "log", "-1", "--format=%cd", "--date=short", sha],
        capture_output=True, text=True, cwd=repo,
    )
    return result.stdout.strip()


def bisect_search(commits, threshold, test_fn):
    """Binary search for the first bad commit.

    Args:
        commits: List of SHAs, oldest first. All are between good (exclusive)
                 and bad (inclusive). The commit BEFORE commits[0] is known good,
                 and the last commit is known bad.
        threshold: Score >= this is "good".
        test_fn: Callable(sha) → float score or None (build/infra failure).

    Returns:
        {"culprit": sha, "tested": [(sha, score), ...]}
    """
    tested = {}  # sha → score

    # lo..hi is the range of candidate culprits (inclusive on both ends)
    lo = 0
    hi = len(commits) - 1

    while lo < hi:
        mid = (lo + hi) // 2
        sha = commits[mid]

        if sha not in tested:
            score = test_fn(sha)
            tested[sha] = score

        score = tested[sha]

        if score is None:
            # Build failure — can't test this commit.
            # Try to find a nearby testable commit.
            # Search outward from mid for one we can test.
            found = False
            for offset in range(1, hi - lo + 1):
                for candidate in [mid - offset, mid + offset]:
                    if lo <= candidate <= hi and commits[candidate] not in tested:
                        alt_sha = commits[candidate]
                        alt_score = test_fn(alt_sha)
                        tested[alt_sha] = alt_score
                        if alt_score is not None:
                            if alt_score >= threshold:
                                lo = candidate + 1
                            else:
                                hi = candidate
                            found = True
                            break
                if found:
                    break
            if not found:
                # All commits in range are untestable — give up
                break
        elif score >= threshold:
            lo = mid + 1
        else:
            hi = mid

    culprit = commits[lo] if lo <= len(commits) - 1 else commits[-1]

    # Test the culprit if we haven't already
    if culprit not in tested:
        score = test_fn(culprit)
        tested[culprit] = score

    # If the culprit is untestable (build failure), find the nearest testable
    # bad commit scanning forward from lo.
    if tested.get(culprit) is None:
        for i in range(lo, len(commits)):
            sha = commits[i]
            if sha in tested and tested[sha] is not None and tested[sha] < threshold:
                culprit = sha
                break

    return {
        "culprit": culprit,
        "tested": [(sha, tested[sha]) for sha in commits if sha in tested],
    }


def build_at_commit(sha, task, repo=None):
    """Create a worktree at sha, build the linux binary.

    Returns (worktree_dir, binary_path) on success, or (None, None) on failure.
    """
    repo = repo or REPO_ROOT
    short = sha[:7]
    worktree_dir = f"/tmp/bisect-{task}-{short}"

    # Clean up any leftover worktree from a previous run
    if os.path.exists(worktree_dir):
        subprocess.run(
            ["git", "worktree", "remove", "--force", worktree_dir],
            cwd=repo, capture_output=True,
        )

    try:
        subprocess.run(
            ["git", "worktree", "add", "--detach", worktree_dir, sha],
            check=True, cwd=repo, capture_output=True, text=True,
        )
    except subprocess.CalledProcessError as e:
        log(f"  [!] Failed to create worktree: {e.stderr.strip()}")
        return None, None

    try:
        result = subprocess.run(
            ["make", "build-linux"],
            cwd=worktree_dir, capture_output=True, text=True, timeout=120,
        )
        if result.returncode != 0:
            log(f"  [!] Build failed: {result.stderr.strip()[:200]}")
            return None, None
    except subprocess.TimeoutExpired:
        log(f"  [!] Build timed out")
        return None, None

    binary = os.path.join(worktree_dir, "serf-linux-amd64")
    if not os.path.exists(binary):
        log(f"  [!] Binary not found after build")
        return None, None

    return worktree_dir, binary


def stage_agent(worktree_dir, run_id):
    """Copy binary and support files to a staging directory."""
    agent_dir = f"/tmp/eval-{run_id}/agent"
    os.makedirs(agent_dir, exist_ok=True)
    shutil.copy2(os.path.join(worktree_dir, "serf-linux-amd64"), agent_dir)
    shutil.copy2(os.path.join(worktree_dir, "tools", "serf_agent.py"), agent_dir)
    shutil.copy2(os.path.join(worktree_dir, "tools", "install-serf.sh.j2"), agent_dir)
    return agent_dir


def launch_eval(run_id, agent_dir, task, reps, model, instance_type, max_vcpu, harbor_dir):
    """Launch eval via wave_launcher.py. Streams output to stdout."""
    cmd = [
        sys.executable, "-u",  # unbuffered
        os.path.join(REPO_ROOT, "tools", "wave_launcher.py"),
        "--run-id", run_id,
        "--agent-dir", agent_dir,
        "--model", model,
        "--tasks", task,
        "--reps", str(reps),
        "--instance-type", instance_type,
        "--max-vcpu", str(max_vcpu),
        "--harbor-dir", harbor_dir,
    ]
    log(f"  Launching: {run_id} ({reps} reps)")
    result = subprocess.run(cmd)
    if result.returncode != 0:
        log(f"  [!] Launch failed (exit {result.returncode})")
        return False
    return True


def poll_scores(run_id, task, expected_reps, timeout=3600, interval=30):
    """Poll S3 for scores until all reps complete or timeout."""
    # Import wave_scores from the tools directory
    spec = importlib.util.spec_from_file_location(
        "wave_scores", os.path.join(REPO_ROOT, "tools", "wave_scores.py"))
    ws = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(ws)

    start = time.time()
    last_count = 0
    while time.time() - start < timeout:
        try:
            scores = ws.get_scores(run_id)
            task_scores = scores.get(task, {})
            if len(task_scores) != last_count:
                elapsed = int(time.time() - start)
                log(f"  [{elapsed}s] {len(task_scores)}/{expected_reps} reps scored")
                last_count = len(task_scores)
            if len(task_scores) >= expected_reps:
                return task_scores
        except Exception as e:
            log(f"  [!] Poll error: {e}")
        time.sleep(interval)

    # Return whatever we have at timeout
    try:
        scores = ws.get_scores(run_id)
        return scores.get(task, {})
    except Exception:
        return {}


def cleanup_worktree(worktree_dir, repo=None):
    """Remove a git worktree."""
    repo = repo or REPO_ROOT
    if worktree_dir and os.path.exists(worktree_dir):
        subprocess.run(
            ["git", "worktree", "remove", "--force", worktree_dir],
            cwd=repo, capture_output=True,
        )


def test_commit_live(sha, task, reps, step, model, instance_type, max_vcpu, harbor_dir):
    """Full pipeline: worktree → build → stage → launch → poll → score → cleanup.

    Returns the mean score, or None on build/launch failure.
    """
    summary = commit_summary(sha)
    log(f"\nStep {step}: {summary}")

    worktree_dir, binary = build_at_commit(sha, task)
    if worktree_dir is None:
        return None

    run_id = make_run_id(task, sha, step)
    try:
        agent_dir = stage_agent(worktree_dir, run_id)
        if not launch_eval(run_id, agent_dir, task, reps, model, instance_type,
                           max_vcpu, harbor_dir):
            return None

        log(f"  Polling for results (timeout 60m)...")
        rep_scores = poll_scores(run_id, task, reps)
        score = compute_score(rep_scores, reps)

        if score is not None:
            passing = sum(1 for v in rep_scores.values() if v >= 0.999)
            log(f"  Score: {score:.3f} ({passing}/{len(rep_scores)})")
        else:
            log(f"  Score: NO RESULTS (infra failure?)")

        return score
    finally:
        cleanup_worktree(worktree_dir)


def parse_args():
    p = argparse.ArgumentParser(
        description="Binary search for the commit that caused an eval regression.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    p.add_argument("--task", required=True, help="Task name to bisect")
    p.add_argument("--good", required=True, help="Known-good commit SHA")
    p.add_argument("--bad", default="HEAD", help="Known-bad commit SHA (default: HEAD)")
    p.add_argument("--reps", type=int, default=5, help="Reps per test point (default: 5)")
    p.add_argument("--threshold", type=float, default=0.8,
                    help="Score >= this means 'good' (default: 0.8)")
    p.add_argument("--model", default="openai/gpt-5.4-mini", help="Model to eval with")
    p.add_argument("--instance-type", default="r6i.large", help="EC2 instance type")
    p.add_argument("--max-vcpu", type=int, default=16,
                    help="vCPU quota ceiling (default: 16)")
    p.add_argument("--harbor-dir", default=HARBOR_DIR_DEFAULT,
                    help="Path to harbor-runner")
    p.add_argument("--no-filter", action="store_true",
                    help="Test ALL commits, not just agent-relevant ones")
    p.add_argument("--dry-run", action="store_true",
                    help="Show commit range and plan, don't launch anything")
    return p.parse_args()


def main():
    args = parse_args()

    # Resolve SHAs
    good_sha = subprocess.run(
        ["git", "rev-parse", args.good],
        capture_output=True, text=True, cwd=REPO_ROOT,
    ).stdout.strip()
    bad_sha = subprocess.run(
        ["git", "rev-parse", args.bad],
        capture_output=True, text=True, cwd=REPO_ROOT,
    ).stdout.strip()

    filter_agent = not args.no_filter
    commits = get_commit_range(good_sha, bad_sha, filter_agent=filter_agent)
    all_commits = get_commit_range(good_sha, bad_sha, filter_agent=False)

    # Print summary
    log(f"=== eval_bisect: {args.task} ===")
    log(f"Good: {good_sha[:7]} ({commit_date(good_sha)})")
    log(f"Bad:  {bad_sha[:7]} ({commit_date(bad_sha)})")
    if filter_agent:
        log(f"Agent-relevant commits: {len(commits)} of {len(all_commits)} total")
    else:
        log(f"Commits: {len(commits)}")
    log(f"Threshold: {args.threshold:.2f}")
    log(f"Reps: {args.reps}")
    log(f"Estimated steps: ~{len(commits).bit_length()} (binary search)")
    log()

    if args.dry_run:
        log("Commits to test (oldest first):")
        for i, sha in enumerate(commits):
            log(f"  {i+1}. {commit_summary(sha)}")
        log(f"\n(dry run — no evals launched)")
        return

    if not commits:
        log("No commits in range. Nothing to bisect.")
        return

    step_counter = [0]

    def test_fn(sha):
        step_counter[0] += 1
        return test_commit_live(
            sha, args.task, args.reps, step_counter[0],
            args.model, args.instance_type, args.max_vcpu, args.harbor_dir,
        )

    result = bisect_search(commits, args.threshold, test_fn)

    # Print final report
    log("\n" + "=" * 60)
    log(f"RESULT: Regression introduced by {result['culprit'][:7]}")
    log()
    log("Bisect log:")
    for sha, score in result["tested"]:
        verdict = "GOOD" if score is not None and score >= args.threshold else "BAD"
        if score is None:
            verdict = "SKIP (build failed)"
        score_str = f"{score:.3f}" if score is not None else "N/A"
        log(f"  {sha[:7]} → {score_str} → {verdict}  {commit_summary(sha)}")
    log()
    log("Culprit commit:")
    show = subprocess.run(
        ["git", "show", "--stat", result["culprit"]],
        capture_output=True, text=True, cwd=REPO_ROOT,
    )
    log(show.stdout)


if __name__ == "__main__":
    main()
