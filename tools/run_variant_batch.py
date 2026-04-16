#!/usr/bin/env python3
"""Build and launch a batch of implementer.md variants.

Usage: python3 tools/run_variant_batch.py [--dry-run]

Reads variants from gen_variants.py, creates branches, builds binaries,
and launches wave_launcher for each.
"""
import subprocess, os, sys, shutil, time

REPO = os.path.expanduser("~/prime-radiant/serf")
HARBOR = os.path.expanduser("~/prime-radiant/harbor-runner")
REPS = 3
INSTANCE_TYPE = "c6i.xlarge"
IMPL_PATH = "agent/bundled_plugins/workflow/agents/implementer.md"
COORD_PATH = "agent/bundled_plugins/workflow/agents/coordinator.md"

# Distribute vCPU across variants — total 128
VCPU_PER_VARIANT = 16  # 4 concurrent instances each

def run(cmd, **kwargs):
    kwargs.setdefault("cwd", REPO)
    kwargs.setdefault("capture_output", True)
    kwargs.setdefault("text", True)
    r = subprocess.run(cmd, shell=True, **kwargs)
    if r.returncode != 0 and kwargs.get("check", False):
        print(f"FAILED: {cmd}")
        print(r.stderr)
        sys.exit(1)
    return r

def main():
    dry_run = "--dry-run" in sys.argv

    # Import variants
    sys.path.insert(0, os.path.join(REPO, "tools"))
    # Support --batch2 flag to use batch 2 variants
    if "--batch2" in sys.argv:
        from gen_variants_b2 import VARIANTS
        sys.argv.remove("--batch2")
    else:
        from gen_variants import VARIANTS

    # Save current branch
    orig_branch = run("git rev-parse --abbrev-ref HEAD").stdout.strip()

    # Read current file contents
    with open(os.path.join(REPO, IMPL_PATH)) as f:
        orig_impl = f.read()
    with open(os.path.join(REPO, COORD_PATH)) as f:
        orig_coord = f.read()
    with open(os.path.join(REPO, "agent/prompts/sections/communicate.md.tmpl")) as f:
        orig_comm = f.read()

    built = []

    for v in VARIANTS:
        name = v["name"]
        branch = f"exp/v33-{name}"
        print(f"\n=== {name} ===")

        # Create branch from main
        run(f"git checkout main -q")
        run(f"git branch -D {branch} 2>/dev/null; true")
        run(f"git checkout -b {branch} main -q")

        # Apply implementer.md edits
        impl_content = orig_impl
        for old, new in v.get("edits", []):
            if old not in impl_content:
                print(f"  WARNING: old_string not found for {name}")
                print(f"  Looking for: {old[:80]}...")
                continue
            impl_content = impl_content.replace(old, new)

        with open(os.path.join(REPO, IMPL_PATH), "w") as f:
            f.write(impl_content)

        # Apply coordinator.md edits if any
        coord_content = orig_coord
        for old, new in v.get("coordinator_edits", []):
            if old not in coord_content:
                print(f"  WARNING: coordinator old_string not found for {name}")
                continue
            coord_content = coord_content.replace(old, new)

        if v.get("coordinator_edits"):
            with open(os.path.join(REPO, COORD_PATH), "w") as f:
                f.write(coord_content)

        # Apply communicate.md.tmpl edits if any
        comm_content = orig_comm
        for old, new in v.get("communicate_edits", []):
            if old not in comm_content:
                print(f"  WARNING: communicate old_string not found for {name}")
                continue
            comm_content = comm_content.replace(old, new)

        if v.get("communicate_edits"):
            with open(os.path.join(REPO, "agent/prompts/sections/communicate.md.tmpl"), "w") as f:
                f.write(comm_content)

        # Commit
        run(f"git add {IMPL_PATH} {COORD_PATH} agent/prompts/sections/communicate.md.tmpl")
        run(f'git commit -m "exp/v33: {name}" -q')
        sha = run("git rev-parse --short HEAD").stdout.strip()

        if dry_run:
            print(f"  Branch: {branch} @ {sha}")
            print(f"  Target: {v['target']}")
            # Restore files
            with open(os.path.join(REPO, IMPL_PATH), "w") as f:
                f.write(orig_impl)
            with open(os.path.join(REPO, COORD_PATH), "w") as f:
                f.write(orig_coord)
            continue

        # Build
        print(f"  Building...")
        r = run("make build-linux 2>&1 | tail -1")
        print(f"  {r.stdout.strip()}")

        # Stage
        stage_dir = f"/tmp/v33-{name}/agent"
        os.makedirs(stage_dir, exist_ok=True)
        for f_name in ["serf-linux-amd64", "tools/serf_agent.py", "tools/install-serf.sh.j2"]:
            src = os.path.join(REPO, f_name)
            shutil.copy2(src, os.path.join(stage_dir, os.path.basename(f_name)))

        # Verify binary has the change (spot check)
        r = run(f"strings /tmp/v33-{name}/agent/serf-linux-amd64 | head -5000 | wc -l")

        built.append({
            "name": name,
            "sha": sha,
            "target": v["target"],
            "stage_dir": stage_dir,
            "run_id": f"v33-{name}-{sha}",
        })

    # Restore original branch
    run(f"git checkout {orig_branch} -q")
    # Restore original files
    with open(os.path.join(REPO, IMPL_PATH), "w") as f:
        f.write(orig_impl)
    with open(os.path.join(REPO, COORD_PATH), "w") as f:
        f.write(orig_coord)
    with open(os.path.join(REPO, "agent/prompts/sections/communicate.md.tmpl"), "w") as f:
        f.write(orig_comm)

    if dry_run:
        print(f"\n=== DRY RUN: {len(VARIANTS)} variants would be built and launched ===")
        return

    # Launch all
    print(f"\n=== Launching {len(built)} variants ===")
    procs = []
    for b in built:
        cmd = (
            f"python3 {REPO}/tools/wave_launcher.py"
            f" --run-id {b['run_id']}"
            f" --agent-dir {b['stage_dir']}"
            f" --model openai/gpt-5.4-mini"
            f" --tasks {b['target']}"
            f" --reps {REPS}"
            f" --instance-type {INSTANCE_TYPE}"
            f" --concurrency 1"
            f" --max-vcpu {VCPU_PER_VARIANT}"
            f" --harbor-dir {HARBOR}"
        )
        print(f"  {b['name']:25s} -> {b['target']} (run: {b['run_id']})")
        p = subprocess.Popen(cmd, shell=True, cwd=REPO)
        procs.append((b["name"], p))
        time.sleep(2)  # Stagger launches

    print(f"\n=== All {len(procs)} launched ===")
    print("Monitor with:")
    print("  python3 tools/check_variant_batch.py")

    # Write run IDs for the checker
    with open("/tmp/v33-runs.txt", "w") as f:
        for b in built:
            f.write(f"{b['run_id']} {b['target']}\n")


if __name__ == "__main__":
    main()
