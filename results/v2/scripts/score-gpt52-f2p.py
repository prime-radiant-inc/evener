#!/usr/bin/env python3
"""F2P scoring for all gpt-5.2 runs (strategies + codex).

Standard SWE-bench methodology: strip overlapping test files from agent diff,
apply gold test patch, run tests.
"""
import json
import os
import re
import subprocess
import tempfile

BASE = "/tmp/serfeval-v2"
PATCHES_DIR = f"{BASE}/patches"

TEST_CMDS = {
    "django__django-11276": [
        "python3", "tests/runtests.py",
        "utils_tests.test_html", "admin_docs.test_views", "auth_tests.test_forms",
        "forms_tests.tests.test_forms", "forms_tests.widget_tests.test_clearablefileinput",
        "model_forms.tests", "template_tests.filter_tests.test_addslashes",
        "template_tests.filter_tests.test_make_list", "template_tests.filter_tests.test_title",
        "template_tests.filter_tests.test_urlize", "template_tests.syntax_tests.test_url",
        "view_tests.tests.test_csrf", "view_tests.tests.test_debug",
    ],
    "django__django-11138": ["python3", "tests/runtests.py", "timezones"],
    "astropy__astropy-13398": [
        "python3", "-m", "pytest",
        "astropy/coordinates/tests/test_intermediate_transformations.py",
        "-x", "--no-header", "-rN",
    ],
    "astropy__astropy-13977": [
        "python3", "-m", "pytest",
        "astropy/units/tests/test_quantity_ufuncs.py",
        "astropy/units/tests/test_quantity.py", "-x", "--no-header", "-rN",
    ],
    "pylint-dev__pylint-4604": [
        "python3", "-m", "pytest", "tests/checkers/unittest_variables.py",
        "-x", "--no-header", "-rN",
    ],
    "pydata__xarray-6992": [
        "python3", "-m", "pytest", "xarray/tests/test_dataset.py",
        "-x", "--no-header", "-rN",
        "-k", "test_set_index_deindexed_coords or test_reset_index_drop_dims or test_reset_index_drop_convert or test_reset_index",
    ],
    "sympy__sympy-13091": [
        "python3", "-m", "pytest", "sympy/core/tests/test_basic.py",
        "sympy/core/tests/test_numbers.py", "-x", "--no-header", "-rN",
    ],
    "pytest-dev__pytest-5787": [
        "python3", "-m", "pytest", "testing/test_reports.py",
        "testing/code/test_excinfo.py", "-x", "--no-header", "-rN",
    ],
    "scikit-learn__scikit-learn-25102": [
        "python3", "-m", "pytest", "sklearn/feature_selection/tests/test_base.py",
        "sklearn/feature_selection/tests/test_feature_select.py", "-x", "--no-header", "-rN",
    ],
    "sphinx-doc__sphinx-11510": [
        "python3", "-m", "pytest", "tests/test_directive_other.py",
        "-x", "--no-header", "-rN",
    ],
}


def extract_files_from_diff(diff_text):
    files = []
    for line in diff_text.split("\n"):
        if line.startswith("diff --git"):
            parts = line.split()
            if len(parts) >= 4:
                files.append(parts[2].removeprefix("a/"))
    return files


def strip_test_files_from_diff(diff_text, patch_files):
    chunks = re.split(r"(?=^diff --git )", diff_text, flags=re.MULTILINE)
    kept, stripped = [], []
    for chunk in chunks:
        if not chunk.strip():
            continue
        m = re.match(r"diff --git a/(\S+)", chunk)
        if m and m.group(1) in patch_files:
            stripped.append(m.group(1))
            continue
        kept.append(chunk)
    return "".join(kept), stripped


def reset_repo(repo_dir):
    subprocess.run(["git", "checkout", "--", "."], cwd=repo_dir, capture_output=True)
    subprocess.run(["git", "clean", "-fd"], cwd=repo_dir, capture_output=True)


def apply_patch(repo_dir, patch_path):
    for flags in [[], ["--whitespace=fix"], ["--3way"]]:
        r = subprocess.run(["git", "apply", "--allow-empty"] + flags + [patch_path],
                           cwd=repo_dir, capture_output=True, text=True)
        if r.returncode == 0:
            return True, ""
    return False, r.stderr


def apply_diff_text(repo_dir, diff_text):
    with tempfile.NamedTemporaryFile(mode="w", suffix=".patch", delete=False) as tf:
        tf.write(diff_text)
        path = tf.name
    try:
        return apply_patch(repo_dir, path)
    finally:
        os.unlink(path)


def score_one(strategy, task_id, diff_text, out_file):
    repo_dir = f"{BASE}/repos/{task_id}"
    patch_file = f"{PATCHES_DIR}/{task_id}.patch"

    if os.path.exists(out_file):
        data = json.load(open(out_file))
        return data.get("resolved", False)

    with open(patch_file) as f:
        patch_files = set(extract_files_from_diff(f.read()))

    agent_diff, stripped = strip_test_files_from_diff(diff_text, patch_files)
    reset_repo(repo_dir)

    if agent_diff.strip():
        ok, err = apply_diff_text(repo_dir, agent_diff)
        if not ok:
            write_result(out_file, task_id, strategy, False, error="agent diff failed")
            reset_repo(repo_dir)
            return False

    ok, err = apply_patch(repo_dir, patch_file)
    if not ok:
        write_result(out_file, task_id, strategy, False, error="test patch failed")
        reset_repo(repo_dir)
        return False

    try:
        r = subprocess.run(TEST_CMDS[task_id], cwd=repo_dir, capture_output=True,
                           text=True, timeout=600)
        exit_code = r.returncode
        output = r.stdout + r.stderr
    except subprocess.TimeoutExpired:
        exit_code = -1
        output = "TIMEOUT"

    resolved = exit_code == 0
    write_result(out_file, task_id, strategy, resolved, exit_code=exit_code,
                 test_output_tail=output[-3000:], stripped_files=stripped)
    reset_repo(repo_dir)
    return resolved


def write_result(path, task_id, strategy, resolved, **kwargs):
    result = {"task": task_id, "strategy": strategy, "model": "gpt-5.2", "resolved": resolved}
    result.update({k: v for k, v in kwargs.items() if v})
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(result, f, indent=2)
        f.write("\n")


def main():
    results_map = {}  # strategy -> {task -> resolved}

    # Score strategies
    for strategy in ["compact", "recursive-distill", "memory-crystals"]:
        results_map[strategy] = {}
        strat_dir = f"{BASE}/results/gpt52"
        f2p_dir = f"{BASE}/results/gpt52-f2p"
        os.makedirs(f2p_dir, exist_ok=True)

        for task_id in sorted(TEST_CMDS.keys()):
            src = f"{strat_dir}/{strategy}_{task_id}.json"
            if not os.path.exists(src):
                continue
            data = json.load(open(src))
            diff = data.get("diff", "")
            out = f"{f2p_dir}/{strategy}_{task_id}.json"
            print(f"  {strategy}/{task_id}...", end=" ", flush=True)
            resolved = score_one(strategy, task_id, diff, out)
            print("PASS" if resolved else "FAIL")
            results_map[strategy][task_id] = resolved

    # Score codex
    results_map["codex"] = {}
    codex_dir = f"{BASE}/results/gpt52-codex"
    f2p_dir = f"{BASE}/results/gpt52-codex-f2p"
    os.makedirs(f2p_dir, exist_ok=True)

    for task_id in sorted(TEST_CMDS.keys()):
        src = f"{codex_dir}/codex_{task_id}.json"
        if not os.path.exists(src):
            continue
        data = json.load(open(src))
        diff = data.get("diff", "")
        out = f"{f2p_dir}/codex_{task_id}.json"
        print(f"  codex/{task_id}...", end=" ", flush=True)
        resolved = score_one("codex", task_id, diff, out)
        print("PASS" if resolved else "FAIL")
        results_map["codex"][task_id] = resolved

    # Print summary
    print("\n=== GPT-5.2 F2P RESOLUTION RATES ===\n")
    print(f"{'Strategy':20s} {'Resolved':>10s} {'Rate':>8s}")
    print("-" * 40)
    for strat in ["compact", "recursive-distill", "memory-crystals", "codex"]:
        r = results_map[strat]
        resolved = sum(1 for v in r.values() if v)
        total = len(r)
        print(f"{strat:20s} {resolved}/{total:>8d} {100*resolved/total:7.0f}%")

    print(f"\n{'Per-task':30s} {'compact':>8s} {'RD':>8s} {'MC':>8s} {'codex':>8s}")
    print("-" * 65)
    for task_id in sorted(TEST_CMDS.keys()):
        short = task_id.replace("__", "/")[:28]
        vals = []
        for strat in ["compact", "recursive-distill", "memory-crystals", "codex"]:
            v = results_map[strat].get(task_id)
            vals.append("PASS" if v else "FAIL")
        print(f"{short:30s} {vals[0]:>8s} {vals[1]:>8s} {vals[2]:>8s} {vals[3]:>8s}")


if __name__ == "__main__":
    main()
