#!/usr/bin/env python3
"""Unified orchestration for benchmark eval runs.

Subcommands:
    launch   Build, deploy, and start a harbor eval run
    status   Check progress of a running job
    collect  Collect results and generate summary from a finished job

Examples:
    ./tools/run_eval.py launch --ak reasoning_effort=medium
    ./tools/run_eval.py launch --ak reasoning_effort=medium --rep 2
    ./tools/run_eval.py launch --job custom-name --task build-cython-ext --reps 1
    ./tools/run_eval.py launch --task crack-7z-hash --task fix-code-vulnerability --reps 1
    ./tools/run_eval.py launch --plugin ~/git/superpowers --task build-cython-ext --reps 1
    ./tools/run_eval.py launch --harness lace --reps 3
    ./tools/run_eval.py launch --harness lace --task build-cython-ext --reps 1
    ./tools/run_eval.py launch --harness lace --task discriminators --reps 3
    ./tools/run_eval.py launch --list-tasks
    ./tools/run_eval.py status --job serf_gpt-5.3-codex_medium_abc1234_20260302_1
    ./tools/run_eval.py collect --job serf_gpt-5.3-codex_medium_abc1234_20260302_1

Job names are auto-generated as {harness}[+plugin1+plugin2]_{model}_{effort}_{git-sha}_{date}_{rep}
unless --job is provided explicitly.
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile

from eval_lib import (
    REMOTE,
    REMOTE_DIR,
    DEFAULT_ADAPTER,
    DEFAULT_ARCHIVE_ROOT,
    DEFAULT_CONCURRENCY,
    DEFAULT_JOBS_DIR,
    DEFAULT_MODEL,
    DEFAULT_REPS,
    LACE_DEFAULT_ADAPTER,
    LACE_DEFAULT_MODEL,
    LACE_REPO,
    build_harbor_command,
    build_ldflags,
    build_manifest,
    extract_effort,
    git_info,
    make_job_name,
    make_run_id,
)
from task_sets import list_task_sets, resolve_tasks

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.dirname(SCRIPT_DIR)


def cmd_launch(args):
    """Preflight, build, deploy to per-run staging dir, write manifest, launch harbor."""
    if args.list_tasks:
        print("Available task sets:")
        print(list_task_sets())
        return

    # Harness-specific configuration
    is_lace = args.harness == "lace"
    if is_lace:
        repo_root = LACE_REPO
        if not os.path.isdir(repo_root):
            print(f"ERROR: Lace repo not found at {repo_root}. Set LACE_REPO env var.", file=sys.stderr)
            sys.exit(1)
        # Override defaults when user hasn't explicitly changed them
        if args.model == DEFAULT_MODEL:
            args.model = LACE_DEFAULT_MODEL
        if args.adapter == DEFAULT_ADAPTER:
            args.adapter = LACE_DEFAULT_ADAPTER
    else:
        repo_root = REPO_ROOT

    # Resolve named task sets (e.g. --task discriminators → 56 concrete tasks)
    if args.task:
        args.task = resolve_tasks(args.task)

    # Preflight: clean tree check
    if not args.allow_dirty:
        info = git_info(repo_root)
        if info["dirty"]:
            print("ERROR: Dirty working tree. Commit or use --allow-dirty.", file=sys.stderr)
            sys.exit(1)

    info = git_info(repo_root)

    # Extract plugin basenames for job naming
    plugin_names = [os.path.basename(p.rstrip("/")) for p in args.plugin] if args.plugin else []

    # Auto-generate job name if not provided
    if not args.job:
        effort = extract_effort(args.ak)
        args.job = make_job_name(
            harness=args.harness,
            model=args.model,
            effort=effort,
            git_sha=info["sha"],
            rep=args.rep,
            plugins=plugin_names or None,
        )
        print(f"=== Auto-generated job name: {args.job} ===")

    # Per-run staging directory: each run gets its own isolated copy of the
    # binary, adapter, and install template. This prevents concurrent runs
    # from interfering with each other.
    run_stage_dir = f"{REMOTE_DIR}/runs/{args.job}"

    # Build + Deploy (harness-specific)
    if is_lace:
        # Lace: build + deploy handled together by deploy-lace.sh
        if not args.dry_run:
            if not args.no_build:
                print(f"=== Building and deploying lace to {run_stage_dir} ===")
                deploy_script = os.path.join(LACE_REPO, "tools", "harbor-eval", "deploy-lace.sh")
                subprocess.run(
                    [deploy_script, "--staging-dir", run_stage_dir],
                    check=True,
                )
            else:
                print("=== Skipping lace build/deploy (--no-build) ===")
            # Copy .env from base directory
            subprocess.run(
                ["ssh", REMOTE, f"mkdir -p {run_stage_dir} && cp {REMOTE_DIR}/.env {run_stage_dir}/.env"],
                check=True,
            )
    else:
        # Serf: separate build + deploy steps
        binary_path = "/tmp/serf-linux-amd64"
        if not args.no_build and not args.dry_run:
            print("=== Building linux binary ===")
            ldflags = build_ldflags(REPO_ROOT)
            subprocess.run(
                f'GOOS=linux GOARCH=amd64 go build -ldflags "{ldflags}" -o {binary_path} ./cmd/serf/',
                shell=True, check=True, cwd=REPO_ROOT,
            )
        elif args.no_build:
            print("=== Skipping build (--no-build) ===")
            if not args.dry_run and not os.path.isfile(binary_path):
                print(f"ERROR: --no-build but {binary_path} does not exist. Build first.", file=sys.stderr)
                sys.exit(1)

        if not args.dry_run:
            print(f"=== Staging run: {run_stage_dir} ===")
            subprocess.run(
                ["ssh", REMOTE, f"mkdir -p {run_stage_dir}"],
                check=True,
            )
            files_to_deploy = [
                (binary_path, f"{REMOTE}:{run_stage_dir}/serf-linux-amd64"),
                (f"{REPO_ROOT}/tools/serf_agent.py", f"{REMOTE}:{run_stage_dir}/serf_agent.py"),
                (f"{REPO_ROOT}/tools/install-serf.sh.j2", f"{REMOTE}:{run_stage_dir}/install-serf.sh.j2"),
            ]
            for src, dst in files_to_deploy:
                subprocess.run(["scp", src, dst], check=True)
            # Copy .env from base directory
            subprocess.run(
                ["ssh", REMOTE, f"cp {REMOTE_DIR}/.env {run_stage_dir}/.env"],
                check=True,
            )

    # Copy plugins into the staging directory for reproducibility
    if not args.dry_run and args.plugin:
        subprocess.run(
            ["ssh", REMOTE, f"mkdir -p {run_stage_dir}/plugins"],
            check=True,
        )
        for plugin_path in args.plugin:
            plugin_name = os.path.basename(plugin_path.rstrip("/"))
            print(f"  Staging plugin: {plugin_name}")
            subprocess.run(
                ["scp", "-r", plugin_path, f"{REMOTE}:{run_stage_dir}/plugins/{plugin_name}"],
                check=True,
            )

    # Inject plugin_dirs into ak_args, pointing at the staged copies
    ak_args = list(args.ak or [])
    if args.plugin:
        staged_plugins = [
            f"{run_stage_dir}/plugins/{os.path.basename(p.rstrip('/'))}"
            for p in args.plugin
        ]
        ak_args.append(f"plugin_dirs={','.join(staged_plugins)}")

    # Manifest
    run_id = make_run_id(args.job, info["sha"])
    manifest = build_manifest(
        run_id=run_id,
        job_name=args.job,
        git_sha=info["sha"],
        git_dirty=info["dirty"],
        git_branch=info["branch"],
        model=args.model,
        adapter=args.adapter,
        reps=args.reps,
        concurrency=args.concurrency,
        task_names=args.task or None,
        ak_args=ak_args,
        plugins=args.plugin or None,
    )

    print("=== Manifest ===")
    print(json.dumps(manifest, indent=2))

    # Harbor command
    harbor_cmd = build_harbor_command(
        adapter=args.adapter,
        model=args.model,
        reps=args.reps,
        concurrency=args.concurrency,
        job_name=args.job,
        task_names=args.task or None,
        ak_args=ak_args,
    )

    if args.dry_run:
        print()
        print("=== Dry run: would execute ===")
        print(f"  {harbor_cmd}")
        print(f"  Run ID: {run_id}")
        return

    # Force kill existing job
    if args.force:
        print("=== Force: killing existing job ===")
        subprocess.run(
            ["ssh", REMOTE, f"pkill -f 'job-name {args.job}' 2>/dev/null; rm -rf {DEFAULT_JOBS_DIR}/{args.job}"],
            check=False,
        )

    # Launch
    label = ", ".join(args.task) if args.task else "FULL SUITE"
    print(f"\n=== Launching: {label} x{args.reps} (job: {args.job}) ===")

    launch_script = f"""cd {run_stage_dir}
set -a; source .env; set +a
export PATH="$HOME/.local/bin:$PATH"
rm -rf {DEFAULT_JOBS_DIR}/{args.job}
mkdir -p {DEFAULT_JOBS_DIR}/{args.job}

nohup {harbor_cmd} > /tmp/{args.job}.log 2>&1 &
PID=$!
echo "PID: $PID"
sleep 3
if kill -0 $PID 2>/dev/null; then
    echo "Job running."
    tail -5 /tmp/{args.job}.log 2>/dev/null || true
else
    echo "ERROR: Job exited immediately!"
    cat /tmp/{args.job}.log
    exit 1
fi"""

    result = subprocess.run(["ssh", REMOTE, "bash", "-s"], input=launch_script, text=True)
    if result.returncode != 0:
        sys.exit(result.returncode)

    # Upload manifest
    with tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False) as f:
        json.dump(manifest, f, indent=2)
        manifest_path = f.name
    try:
        subprocess.run(
            ["scp", "-q", manifest_path, f"{REMOTE}:{DEFAULT_JOBS_DIR}/{args.job}/manifest.json"],
            check=False,
        )
    finally:
        os.unlink(manifest_path)

    print(f"""
=== Job launched ===
  Job:    {args.job}
  Run ID: {run_id}
  Model:  {args.model}

=== Monitor ===
  {sys.argv[0]} status --job {args.job}

=== Collect when done ===
  {sys.argv[0]} collect --job {args.job}

=== Tail log ===
  ssh {REMOTE} 'tail -f /tmp/{args.job}.log'""")


def cmd_status(args):
    """Check status of a running job."""
    jobs_dir = DEFAULT_JOBS_DIR
    status_script = f"""echo "=== Process ==="
ps aux | grep "job-name {args.job}" | grep -v grep || echo "(not running)"

echo ""
echo "=== Results ==="
pass=0; fail=0; pending=0

RESULTS_DIR="{jobs_dir}/{args.job}"
if [ ! -d "$RESULTS_DIR" ]; then
    echo "  No results directory found for {args.job}"
    exit 0
fi

echo "(results in $RESULTS_DIR)"
echo ""

for d in $RESULTS_DIR/*/; do
    [ -d "$d" ] || continue
    task=$(basename "$d" | sed 's/__.*$//')
    [ -f "$d/result.json" ] || [ -f "$d/verifier/reward.txt" ] || [ -f "$d/reward.txt" ] || continue
    reward=$(cat "$d/verifier/reward.txt" "$d/reward.txt" 2>/dev/null | head -1)
    if [ -z "$reward" ]; then
        echo "  $task: RUNNING"
        pending=$((pending+1))
    elif [ "$reward" = "1.0" ] || [ "$reward" = "1" ]; then
        echo "  $task: PASS"
        pass=$((pass+1))
    else
        echo "  $task: FAIL ($reward)"
        fail=$((fail+1))
    fi
done
total=$((pass+fail+pending))
echo ""
echo "=== Summary: $pass/$total pass, $fail fail, $pending running ==="

echo ""
echo "=== Build ==="
cat {jobs_dir}/{args.job}/manifest.json 2>/dev/null \
  || echo "(no manifest)"

echo ""
echo "=== Recent log ==="
tail -10 {jobs_dir}/{args.job}/job.log 2>/dev/null \
  || tail -10 /tmp/{args.job}.log 2>/dev/null \
  || echo "(no log)"
"""
    subprocess.run(["ssh", REMOTE, "bash", "-s"], input=status_script, text=True)


def cmd_collect(args):
    """Collect results and generate summary from a finished job."""
    info = git_info(REPO_ROOT)
    run_id = make_run_id(args.job, info["sha"])
    run_dir = os.path.join(args.archive_dir, "runs", run_id)

    print(f"=== Collecting job: {args.job} ===")
    print(f"  Run ID: {run_id}")

    with tempfile.TemporaryDirectory() as tmpdir:
        print("=== Syncing harbor output from remote ===")
        subprocess.run(
            ["rsync", "-az", f"{REMOTE}:{DEFAULT_JOBS_DIR}/{args.job}/", f"{tmpdir}/"],
            check=True,
        )

        print("=== Running collect-run.sh ===")
        subprocess.run(
            [f"{SCRIPT_DIR}/collect-run.sh",
             "--harbor-dir", tmpdir,
             "--archive-dir", run_dir,
             "--run-id", run_id],
            check=True,
        )

    print("=== Generating summary ===")
    result = subprocess.run(
        [sys.executable, f"{SCRIPT_DIR}/generate_summary.py", run_dir, run_id],
        capture_output=True, text=True, check=True,
    )
    summary_path = os.path.join(run_dir, "summary.json")
    with open(summary_path, "w") as f:
        f.write(result.stdout)

    summary = json.loads(result.stdout)
    tc = summary["task_count"]
    ci = summary["pass_rate_majority_ci_95"]
    fc = summary.get("failure_categories", {})

    print(f"""
=== Summary ===
  Tasks:    {tc}
  Majority: {summary['pass_count_majority']}/{tc} ({summary['pass_rate_majority']:.1%})
  Strict:   {summary['pass_count_strict']}/{tc} ({summary['pass_rate_strict']:.1%})
  Any:      {summary['pass_count_any']}/{tc} ({summary['pass_rate_any']:.1%})
  95% CI:   [{ci[0]:.1%}, {ci[1]:.1%}]""")
    if fc:
        print(f"  Failures: {fc}")
    print(f"""
  Archive: {run_dir}
  Summary: {summary_path}""")


def main():
    parser = argparse.ArgumentParser(
        description="Unified orchestration for benchmark eval runs.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""examples:
  %(prog)s launch --ak reasoning_effort=medium
  %(prog)s launch --ak reasoning_effort=medium --rep 2
  %(prog)s launch --job custom-name --task build-cython-ext --reps 1
  %(prog)s launch --task crack-7z-hash --task fix-code-vulnerability --reps 1
  %(prog)s launch --plugin ~/git/superpowers --task build-cython-ext --reps 1
  %(prog)s launch --harness lace --reps 3
  %(prog)s launch --harness lace --task build-cython-ext --reps 1
  %(prog)s launch --harness lace --task discriminators --reps 3
  %(prog)s launch --list-tasks
  %(prog)s status --job serf_gpt-5.3-codex_medium_abc1234_20260302_1
  %(prog)s collect --job serf_gpt-5.3-codex_medium_abc1234_20260302_1""",
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    # launch
    launch_p = subparsers.add_parser("launch", help="Build, deploy, and start a harbor eval run")
    launch_p.add_argument("--job", default="", help="Job name (auto-generated if omitted)")
    launch_p.add_argument("--harness", default="serf", help="Harness name for auto-generated job names (default: serf)")
    launch_p.add_argument("--rep", type=int, default=1, help="Rep number for auto-generated job names (default: 1)")
    launch_p.add_argument("--model", default=DEFAULT_MODEL, help=f"Model (default: {DEFAULT_MODEL})")
    launch_p.add_argument("--task", action="append", default=[], help="Task name or named set (repeatable, omit for full suite)")
    launch_p.add_argument("--reps", type=int, default=DEFAULT_REPS, help=f"Reps (default: {DEFAULT_REPS})")
    launch_p.add_argument("--concurrency", type=int, default=DEFAULT_CONCURRENCY, help=f"Concurrency (default: {DEFAULT_CONCURRENCY})")
    launch_p.add_argument("--plugin", action="append", default=[], help="Plugin host path (repeatable)")
    launch_p.add_argument("--ak", action="append", help="Agent kwarg (repeatable)")
    launch_p.add_argument("--adapter", default=DEFAULT_ADAPTER, help=f"Adapter (default: {DEFAULT_ADAPTER})")
    launch_p.add_argument("--no-build", action="store_true", help="Skip cross-compile")
    launch_p.add_argument("--allow-dirty", action="store_true", help="Allow dirty git tree")
    launch_p.add_argument("--force", action="store_true", help="Kill existing job first")
    launch_p.add_argument("--dry-run", action="store_true", help="Print what would be done")
    launch_p.add_argument("--list-tasks", action="store_true", help="List available named task sets and exit")
    launch_p.set_defaults(func=cmd_launch)

    # status
    status_p = subparsers.add_parser("status", help="Check progress of a running job")
    status_p.add_argument("--job", required=True, help="Job name")
    status_p.set_defaults(func=cmd_status)

    # collect
    collect_p = subparsers.add_parser("collect", help="Collect and summarize a finished job")
    collect_p.add_argument("--job", required=True, help="Job name")
    collect_p.add_argument("--archive-dir", default=DEFAULT_ARCHIVE_ROOT, help=f"Archive root (default: {DEFAULT_ARCHIVE_ROOT})")
    collect_p.set_defaults(func=cmd_collect)

    args = parser.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
