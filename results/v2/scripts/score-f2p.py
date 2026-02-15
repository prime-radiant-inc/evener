#!/usr/bin/env python3
"""Offline F2P scoring: apply agent diffs + test patches, run tests, update results."""
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

BASE = Path("/tmp/serfeval-v2")
RESULTS_DIR = BASE / "results" / "f2p"
REPOS_DIR = BASE / "repos"
PATCHES_DIR = BASE / "patches"

# Per-repo test commands from experiment design
TEST_COMMANDS = {
    "django__django-11276": "python3 tests/runtests.py --verbosity 2 --settings=test_sqlite --parallel 1 utils_tests.test_html template_tests.filter_tests.test_make_list template_tests.filter_tests.test_addslashes template_tests.filter_tests.test_title template_tests.filter_tests.test_urlize template_tests.syntax_tests.test_url auth_tests.test_forms forms_tests.tests.test_forms forms_tests.widget_tests.test_clearablefileinput model_forms.tests admin_docs.test_views view_tests.tests.test_csrf view_tests.tests.test_debug",
    "astropy__astropy-13977": "python3 -m pytest astropy/units/tests/test_quantity_ufuncs.py -rA --tb=short -q",
    "pylint-dev__pylint-4604": "python3 -m pytest tests/checkers/unittest_variables.py -rA --tb=short -q",
    "pydata__xarray-6992": "python3 -m pytest xarray/tests/test_dataset.py xarray/tests/test_dataarray.py --no-header -rA --tb=no -p no:cacheprovider -q",
    "sympy__sympy-13091": "python3 -m pytest sympy/core/tests/test_relational.py -rA --tb=short -q",
    "pytest-dev__pytest-5787": "python3 -m pytest testing/test_reports.py -rA --tb=short -q",
    "django__django-11138": "python3 tests/runtests.py --verbosity 2 --settings=test_sqlite --parallel 1 timezones",
    "scikit-learn__scikit-learn-25102": "python3 -m pytest sklearn/feature_selection/tests/test_base.py sklearn/feature_selection/tests/test_feature_select.py -rA --tb=short -q",
    "sphinx-doc__sphinx-11510": "python3 -m pytest tests/test_directive_other.py -rA --tb=short -q",
    "astropy__astropy-13398": "python3 -m pytest astropy/coordinates/tests/test_intermediate_transformations.py -rA --tb=short -q",
}


def run_f2p_for_result(result_path: Path) -> dict:
    """Apply agent diff + test patch, run tests, return F2P result."""
    with open(result_path) as f:
        result = json.load(f)

    diff = result.get("diff", "")
    if not diff:
        return {"resolved": False, "error": "no diff captured", "tests_passed": [], "tests_failed": []}

    # Parse task ID from filename
    fname = result_path.stem
    parts = fname.split("_", 1)
    task_id = parts[1] if len(parts) > 1 else fname

    repo_dir = REPOS_DIR / task_id
    patch_file = PATCHES_DIR / f"{task_id}.patch"
    test_cmd = TEST_COMMANDS.get(task_id)
    metadata_file = BASE / f"{task_id}.json"

    if not repo_dir.exists():
        return {"resolved": False, "error": f"repo not found: {repo_dir}"}
    if not patch_file.exists():
        return {"resolved": False, "error": f"test patch not found: {patch_file}"}
    if not test_cmd:
        return {"resolved": False, "error": f"no test command for {task_id}"}

    # Load F2P test names
    with open(metadata_file) as f:
        meta = json.load(f)
    f2p_names = meta.get("fail_to_pass", [])

    # Clean repo
    subprocess.run(["git", "reset", "HEAD", "--", "."], cwd=repo_dir, capture_output=True)
    subprocess.run(["git", "checkout", "--", "."], cwd=repo_dir, capture_output=True)
    subprocess.run(["git", "clean", "-fd"], cwd=repo_dir, capture_output=True)

    # Apply agent's diff first (captured against clean base commit).
    agent_apply = subprocess.run(
        ["git", "apply", "--allow-empty", "-"],
        input=diff.encode(),
        cwd=repo_dir,
        capture_output=True,
    )
    if agent_apply.returncode != 0:
        return {
            "resolved": False,
            "error": f"agent diff failed to apply: {agent_apply.stderr.decode()[:500]}",
            "tests_passed": [],
            "tests_failed": f2p_names,
        }

    # Apply test patch on top. Use `patch --forward` to skip already-applied hunks
    # (agent and test patch often modify the same test files with the same changes).
    with open(patch_file) as pf:
        patch_content = pf.read()
    test_apply = subprocess.run(
        ["patch", "-p1", "--forward", "--no-backup-if-mismatch", "--batch"],
        input=patch_content.encode(),
        cwd=repo_dir,
        capture_output=True,
    )
    # --forward returns 1 for "already applied" hunks, which is fine
    if test_apply.returncode > 1:
        return {
            "resolved": False,
            "error": f"test patch failed to apply: {test_apply.stderr.decode()[:500]}",
            "tests_passed": [],
            "tests_failed": f2p_names,
        }

    # Run tests
    test_result = subprocess.run(
        test_cmd,
        shell=True,
        cwd=repo_dir,
        capture_output=True,
        timeout=300,
    )
    test_output = test_result.stdout.decode() + test_result.stderr.decode()

    # Check F2P tests
    passed = []
    failed = []
    for name in f2p_names:
        # Check various test output patterns
        if check_test_passed(test_output, name):
            passed.append(name)
        else:
            failed.append(name)

    resolved = test_result.returncode == 0 or (len(failed) == 0 and len(passed) == len(f2p_names))

    # Clean up
    subprocess.run(["git", "reset", "HEAD", "--", "."], cwd=repo_dir, capture_output=True)
    subprocess.run(["git", "checkout", "--", "."], cwd=repo_dir, capture_output=True)
    subprocess.run(["git", "clean", "-fd"], cwd=repo_dir, capture_output=True)

    return {
        "resolved": resolved,
        "tests_passed": passed,
        "tests_failed": failed,
        "exit_code": test_result.returncode,
        "test_output_tail": test_output[-2000:] if len(test_output) > 2000 else test_output,
    }


def check_test_passed(output: str, test_name: str) -> bool:
    """Check if a specific test passed in the output, handling multiple frameworks."""
    # Pytest: "test_name PASSED" or "path::test_name PASSED"
    if f"{test_name} PASSED" in output:
        return True
    # Pytest verbose: line containing test name + PASSED
    for line in output.split("\n"):
        if test_name in line and "PASSED" in line:
            return True
    # Django runtests.py: "test_name ... ok" or just test name without FAILED/ERROR
    # Django format: "test_name (module.Class) ... ok"
    for line in output.split("\n"):
        if test_name in line and "... ok" in line:
            return True
    # SymPy: "test_name ... ok" or just passed
    # If test name appears and no FAIL/ERROR on the same line
    return False


def main():
    results_files = sorted(RESULTS_DIR.glob("*.json"))
    if not results_files:
        print("No result files found in", RESULTS_DIR)
        sys.exit(1)

    print(f"Scoring {len(results_files)} results...")
    print()

    summary = {}
    for rf in results_files:
        if rf.name.endswith(".log"):
            continue
        print(f"Scoring: {rf.name}...", end=" ", flush=True)
        try:
            f2p = run_f2p_for_result(rf)
            print(f"{'RESOLVED' if f2p['resolved'] else 'FAILED'} "
                  f"({len(f2p.get('tests_passed',[]))}/{len(f2p.get('tests_passed',[]))+len(f2p.get('tests_failed',[]))} F2P passed)")

            # Update the result JSON with F2P scores
            with open(rf) as f:
                result = json.load(f)
            result["f2p_results"] = f2p
            with open(rf, "w") as f:
                json.dump(result, f, indent=2)

            summary[rf.name] = f2p["resolved"]
        except Exception as e:
            print(f"ERROR: {e}")
            summary[rf.name] = False

    print()
    print("=== F2P SUMMARY ===")
    resolved = sum(1 for v in summary.values() if v)
    print(f"Resolved: {resolved}/{len(summary)}")

    # By strategy
    from collections import defaultdict
    by_strategy = defaultdict(list)
    for name, res in summary.items():
        strat = name.split("_")[0]
        by_strategy[strat].append(res)
    for strat, results in sorted(by_strategy.items()):
        r = sum(results)
        print(f"  {strat}: {r}/{len(results)} resolved ({r/len(results)*100:.0f}%)")


if __name__ == "__main__":
    main()
