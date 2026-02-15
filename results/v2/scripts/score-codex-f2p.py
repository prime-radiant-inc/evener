#!/usr/bin/env python3
"""F2P scoring for Codex baseline diffs.

Standard SWE-bench methodology:
1. Apply agent's source-code-only changes (strip test file changes)
2. Apply the gold test patch
3. Run the tests from the test patch
4. If all tests pass, the task is "resolved"
"""
import json
import os
import re
import subprocess
import sys
import tempfile

BASE = "/tmp/serfeval-v2"
CODEX_DIR = f"{BASE}/results/codex"
PATCHES_DIR = f"{BASE}/patches"
RESULTS_DIR = f"{BASE}/results/codex-f2p"
os.makedirs(RESULTS_DIR, exist_ok=True)

# Test commands per task (run from repo root)
TEST_CMDS = {
    "django__django-11276": [
        "python3", "tests/runtests.py",
        "utils_tests.test_html",
        "admin_docs.test_views",
        "auth_tests.test_forms",
        "forms_tests.tests.test_forms",
        "forms_tests.widget_tests.test_clearablefileinput",
        "model_forms.tests",
        "template_tests.filter_tests.test_addslashes",
        "template_tests.filter_tests.test_make_list",
        "template_tests.filter_tests.test_title",
        "template_tests.filter_tests.test_urlize",
        "template_tests.syntax_tests.test_url",
        "view_tests.tests.test_csrf",
        "view_tests.tests.test_debug",
    ],
    "django__django-11138": [
        "python3", "tests/runtests.py", "timezones",
    ],
    "astropy__astropy-13398": [
        "python3", "-m", "pytest",
        "astropy/coordinates/tests/test_intermediate_transformations.py",
        "-x", "--no-header", "-rN",
    ],
    "astropy__astropy-13977": [
        "python3", "-m", "pytest",
        "astropy/units/tests/test_quantity_ufuncs.py",
        "astropy/units/tests/test_quantity.py",
        "-x", "--no-header", "-rN",
    ],
    "pylint-dev__pylint-4604": [
        "python3", "-m", "pytest",
        "tests/checkers/unittest_variables.py",
        "-x", "--no-header", "-rN",
    ],
    "pydata__xarray-6992": [
        "python3", "-m", "pytest",
        "xarray/tests/test_dataset.py",
        "-x", "--no-header", "-rN",
        "-k", "test_set_index_deindexed_coords or test_reset_index_drop_dims or test_reset_index_drop_convert or test_reset_index",
    ],
    "sympy__sympy-13091": [
        "python3", "-m", "pytest",
        "sympy/core/tests/test_basic.py",
        "sympy/core/tests/test_numbers.py",
        "-x", "--no-header", "-rN",
    ],
    "pytest-dev__pytest-5787": [
        "python3", "-m", "pytest",
        "testing/test_reports.py",
        "testing/code/test_excinfo.py",
        "-x", "--no-header", "-rN",
    ],
    "scikit-learn__scikit-learn-25102": [
        "python3", "-m", "pytest",
        "sklearn/feature_selection/tests/test_base.py",
        "sklearn/feature_selection/tests/test_feature_select.py",
        "-x", "--no-header", "-rN",
    ],
    "sphinx-doc__sphinx-11510": [
        "python3", "-m", "pytest",
        "tests/test_directive_other.py",
        "-x", "--no-header", "-rN",
    ],
}

# Test directories/patterns to strip from agent diffs (SWE-bench methodology)
TEST_PATTERNS = [
    r"^tests/",
    r"^testing/",
    r"/tests/",
    r"/test_",
    r"test_.*\.py$",
    r"conftest\.py$",
]


def is_test_file(filepath):
    """Check if a file path looks like a test file."""
    for pat in TEST_PATTERNS:
        if re.search(pat, filepath):
            return True
    return False


def extract_files_from_diff(diff_text):
    """Extract the list of files modified in a unified diff."""
    files = []
    for line in diff_text.split("\n"):
        if line.startswith("diff --git"):
            parts = line.split()
            if len(parts) >= 4:
                files.append(parts[2].removeprefix("a/"))
    return files


def strip_test_files_from_diff(diff_text, patch_files):
    """Remove hunks for files that overlap with the test patch.

    Only strips files that are in both the agent diff AND the test patch.
    This preserves agent changes to test files not touched by the patch.
    """
    chunks = re.split(r"(?=^diff --git )", diff_text, flags=re.MULTILINE)
    kept = []
    stripped = []
    for chunk in chunks:
        if not chunk.strip():
            continue
        m = re.match(r"diff --git a/(\S+)", chunk)
        if m:
            filepath = m.group(1)
            if filepath in patch_files:
                stripped.append(filepath)
                continue
        kept.append(chunk)
    return "".join(kept), stripped


def reset_repo(repo_dir):
    subprocess.run(["git", "checkout", "--", "."], cwd=repo_dir,
                   capture_output=True)
    subprocess.run(["git", "clean", "-fd"], cwd=repo_dir,
                   capture_output=True)


def apply_patch(repo_dir, patch_path):
    """Try to apply a patch file. Returns (success, error_msg)."""
    for flags in [[], ["--whitespace=fix"], ["--3way"]]:
        r = subprocess.run(
            ["git", "apply", "--allow-empty"] + flags + [patch_path],
            cwd=repo_dir, capture_output=True, text=True,
        )
        if r.returncode == 0:
            return True, ""
    return False, r.stderr


def apply_diff_text(repo_dir, diff_text):
    """Apply a diff from a string. Returns (success, error_msg)."""
    with tempfile.NamedTemporaryFile(mode="w", suffix=".patch",
                                      delete=False) as tf:
        tf.write(diff_text)
        path = tf.name
    try:
        ok, err = apply_patch(repo_dir, path)
        return ok, err
    finally:
        os.unlink(path)


def score_task(task_id):
    print(f"\n=== {task_id} ===")

    repo_dir = f"{BASE}/repos/{task_id}"
    codex_json = f"{CODEX_DIR}/codex_{task_id}.json"
    patch_file = f"{PATCHES_DIR}/{task_id}.patch"
    out_file = f"{RESULTS_DIR}/codex-f2p_{task_id}.json"

    if os.path.exists(out_file):
        with open(out_file) as f:
            data = json.load(f)
        print(f"  SKIP (already scored: resolved={data.get('resolved')})")
        return data.get("resolved", False)

    # Load Codex diff
    with open(codex_json) as f:
        codex_data = json.load(f)
    diff_text = codex_data.get("diff", "")
    if not diff_text:
        print("  ERROR: No diff")
        write_result(out_file, task_id, False, error="no diff")
        return False

    # Load test patch to find its files
    with open(patch_file) as f:
        patch_text = f.read()
    patch_files = set(extract_files_from_diff(patch_text))

    # Strip overlapping test files from Codex diff
    agent_diff, stripped = strip_test_files_from_diff(diff_text, patch_files)
    if stripped:
        print(f"  Stripped {len(stripped)} overlapping test files from agent diff")

    # Reset repo
    reset_repo(repo_dir)

    # Apply agent's source-code changes
    if agent_diff.strip():
        ok, err = apply_diff_text(repo_dir, agent_diff)
        if not ok:
            print(f"  ERROR: Could not apply agent diff: {err[:200]}")
            write_result(out_file, task_id, False, error="agent diff failed")
            reset_repo(repo_dir)
            return False
        print("  Applied agent source diff")
    else:
        print("  WARNING: No source changes after stripping test files")

    # Apply the gold test patch
    ok, err = apply_patch(repo_dir, patch_file)
    if not ok:
        print(f"  ERROR: Could not apply test patch: {err[:200]}")
        write_result(out_file, task_id, False, error="test patch failed")
        reset_repo(repo_dir)
        return False
    print("  Applied gold test patch")

    # Run tests
    test_cmd = TEST_CMDS[task_id]
    print(f"  Running: {' '.join(test_cmd)}")
    try:
        r = subprocess.run(test_cmd, cwd=repo_dir, capture_output=True,
                           text=True, timeout=600)
        exit_code = r.returncode
        output = r.stdout + r.stderr
    except subprocess.TimeoutExpired:
        exit_code = -1
        output = "TIMEOUT after 600s"

    resolved = exit_code == 0
    tail = output[-3000:] if len(output) > 3000 else output
    print(f"  Exit code: {exit_code}  Resolved: {resolved}")

    if not resolved:
        # Show last few lines for debugging
        last_lines = output.strip().split("\n")[-5:]
        for line in last_lines:
            print(f"    {line[:120]}")

    write_result(out_file, task_id, resolved, exit_code=exit_code,
                 test_output_tail=tail, stripped_files=stripped)

    reset_repo(repo_dir)
    return resolved


def write_result(path, task_id, resolved, exit_code=None, error=None,
                 test_output_tail=None, stripped_files=None):
    result = {
        "task": task_id,
        "strategy": "codex",
        "model": "gpt-5.3-codex",
        "resolved": resolved,
    }
    if exit_code is not None:
        result["test_exit_code"] = exit_code
    if error:
        result["error"] = error
    if test_output_tail:
        result["test_output_tail"] = test_output_tail
    if stripped_files:
        result["stripped_test_files"] = stripped_files
    with open(path, "w") as f:
        json.dump(result, f, indent=2)
        f.write("\n")


def main():
    completed = 0
    failed = 0
    resolved_count = 0

    for task_id in sorted(TEST_CMDS.keys()):
        result = score_task(task_id)
        if result is None:
            failed += 1
        else:
            completed += 1
            if result:
                resolved_count += 1

    print(f"\n=== CODEX F2P SCORING COMPLETE ===")
    print(f"Scored: {completed}")
    print(f"Errors: {failed}")
    print(f"Resolved: {resolved_count} / {completed}")

    # Comparison with our strategies
    print(f"\n=== F2P RESOLUTION RATES ===")
    f2p_dir = f"{BASE}/results/f2p"
    if os.path.isdir(f2p_dir):
        for strat in ["compact", "recursive-distill", "memory-crystals"]:
            strat_resolved = 0
            strat_total = 0
            for task_id in TEST_CMDS:
                f2p_file = f"{f2p_dir}/{strat}_{task_id}.json"
                if os.path.exists(f2p_file):
                    with open(f2p_file) as f:
                        data = json.load(f)
                    strat_total += 1
                    if data.get("f2p_results", {}).get("resolved"):
                        strat_resolved += 1
            if strat_total:
                print(f"  {strat:20s}: {strat_resolved}/{strat_total} ({100*strat_resolved/strat_total:.0f}%)")
    print(f"  {'codex':20s}: {resolved_count}/{completed} ({100*resolved_count/completed:.0f}%)")


if __name__ == "__main__":
    main()
