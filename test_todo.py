"""Pytest suite for the todo CLI described in perf-bench/task.md.

These tests are intentionally end-to-end: each test runs the CLI in a fresh
temporary directory via subprocess using `python todo.py ...` (with cwd set to
that temp dir). The temp dir is isolated so tests do not interfere.

If the implementation files (todo.py/storage.py) are not present in the project
root, the suite skips (spec-compliant resilience for environments that only
exercise tests).
"""

from __future__ import annotations

import os
import re
import shutil
import stat
import subprocess
import sys
from pathlib import Path

import pytest


def _project_root() -> Path:
    return Path(__file__).resolve().parent


def _require_impl_or_skip() -> tuple[Path, Path]:
    root = _project_root()
    todo_src = root / "todo.py"
    storage_src = root / "storage.py"
    if not todo_src.exists() or not storage_src.exists():
        pytest.skip(
            "todo.py/storage.py not found in project root; "
            "skipping CLI tests (implementation not present in this checkout)."
        )
    return todo_src, storage_src


def _write_python_shim(tmp_path: Path) -> None:
    """Ensure `python` resolves in PATH even if only python3 exists.

    The prompt requires subprocess invocations of the form:
      subprocess.run(["python", "todo.py", ...], cwd=tmpdir)

    In some environments (including this one), the `python` executable may not
    exist. We create a per-test shim in tmp_path and prepend tmp_path to PATH.
    """

    if os.name == "nt":
        shim = tmp_path / "python.bat"
        shim.write_text(f"@echo off\r\n\"{sys.executable}\" %*\r\n", encoding="utf-8")
        return

    shim = tmp_path / "python"
    shim.write_text(
        "#!/bin/sh\n"
        f"exec '{sys.executable}' \"$@\"\n",
        encoding="utf-8",
    )
    shim.chmod(shim.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)


def _stage_app(tmp_path: Path) -> None:
    todo_src, storage_src = _require_impl_or_skip()
    shutil.copy2(todo_src, tmp_path / "todo.py")
    shutil.copy2(storage_src, tmp_path / "storage.py")
    _write_python_shim(tmp_path)


def run_cli(tmp_path: Path, *args: str, expect_code: int | None = 0) -> subprocess.CompletedProcess[str]:
    """Run `python todo.py ...` inside tmp_path.

    Returns CompletedProcess with stdout/stderr captured as text.
    """

    _stage_app(tmp_path)

    env = os.environ.copy()
    # Prepend our shim directory so `python` resolves.
    env["PATH"] = str(tmp_path) + os.pathsep + env.get("PATH", "")
    env.setdefault("PYTHONUTF8", "1")
    env.setdefault("PYTHONIOENCODING", "utf-8")

    cmd = ["python", "todo.py", *args]
    proc = subprocess.run(
        cmd,
        cwd=tmp_path,
        env=env,
        text=True,
        capture_output=True,
    )
    if expect_code is not None:
        assert (
            proc.returncode == expect_code
        ), f"Expected exit {expect_code}, got {proc.returncode}. stderr={proc.stderr!r} stdout={proc.stdout!r}"
    return proc


def _extract_id(add_stdout: str) -> int:
    # Expected: "Added: <text> (id=<id>)"
    m = re.search(r"\(id=(\d+)\)", add_stdout)
    assert m, f"Could not parse id from add output: {add_stdout!r}"
    return int(m.group(1))


def _list_lines(tmp_path: Path) -> list[str]:
    out = run_cli(tmp_path, "list").stdout
    return [ln.rstrip("\n") for ln in out.splitlines() if ln.strip()]


def test_empty_list(tmp_path: Path) -> None:
    proc = run_cli(tmp_path, "list")
    assert proc.stdout.strip() == "No todos."
    assert proc.stderr.strip() == ""


def test_add_single(tmp_path: Path) -> None:
    proc_add = run_cli(tmp_path, "add", "buy milk")
    assert "Added: buy milk (id=" in proc_add.stdout

    proc_list = run_cli(tmp_path, "list")
    todo_id = _extract_id(proc_add.stdout)
    assert f"[{todo_id}] [ ] buy milk" in proc_list.stdout


def test_add_multiple(tmp_path: Path) -> None:
    p1 = run_cli(tmp_path, "add", "one")
    p2 = run_cli(tmp_path, "add", "two")
    p3 = run_cli(tmp_path, "add", "three")
    id1, id2, id3 = _extract_id(p1.stdout), _extract_id(p2.stdout), _extract_id(p3.stdout)

    out = run_cli(tmp_path, "list").stdout
    assert f"[{id1}] [ ] one" in out
    assert f"[{id2}] [ ] two" in out
    assert f"[{id3}] [ ] three" in out


def test_complete(tmp_path: Path) -> None:
    proc_add = run_cli(tmp_path, "add", "wash car")
    todo_id = _extract_id(proc_add.stdout)

    proc_done = run_cli(tmp_path, "done", str(todo_id))
    assert proc_done.stderr.strip() == ""
    assert "Completed: wash car" in proc_done.stdout

    out = run_cli(tmp_path, "list").stdout
    assert f"[{todo_id}] [x] wash car" in out


def test_complete_not_found(tmp_path: Path) -> None:
    proc = run_cli(tmp_path, "done", "999", expect_code=1)
    assert "Todo 999 not found." in proc.stderr
    # Ensure it's an error path: stdout should be empty or at least not claim completion.
    assert "Completed:" not in proc.stdout


def test_delete(tmp_path: Path) -> None:
    proc_add = run_cli(tmp_path, "add", "take out trash")
    todo_id = _extract_id(proc_add.stdout)

    proc_del = run_cli(tmp_path, "delete", str(todo_id))
    assert "Deleted: take out trash" in proc_del.stdout
    assert proc_del.stderr.strip() == ""

    proc_list = run_cli(tmp_path, "list")
    # When empty, spec says to print "No todos.".
    assert proc_list.stdout.strip() == "No todos."


def test_delete_not_found(tmp_path: Path) -> None:
    proc = run_cli(tmp_path, "delete", "12345", expect_code=1)
    assert "Todo 12345 not found." in proc.stderr
    assert "Deleted:" not in proc.stdout


def test_search_found(tmp_path: Path) -> None:
    p1 = run_cli(tmp_path, "add", "read book")
    _ = run_cli(tmp_path, "add", "write report")
    todo_id = _extract_id(p1.stdout)

    proc = run_cli(tmp_path, "search", "book")
    assert f"[{todo_id}] [ ] read book" in proc.stdout
    assert "write report" not in proc.stdout


def test_search_not_found(tmp_path: Path) -> None:
    _ = run_cli(tmp_path, "add", "alpha")
    proc = run_cli(tmp_path, "search", "does-not-exist")
    assert proc.stdout.strip() == "No matches."
    assert proc.stderr.strip() == ""


def test_search_case_insensitive(tmp_path: Path) -> None:
    p1 = run_cli(tmp_path, "add", "Call Mom")
    todo_id = _extract_id(p1.stdout)

    proc = run_cli(tmp_path, "search", "mom")
    assert f"[{todo_id}] [ ] Call Mom" in proc.stdout
    proc2 = run_cli(tmp_path, "search", "MOM")
    assert f"[{todo_id}] [ ] Call Mom" in proc2.stdout


def test_ids_increment(tmp_path: Path) -> None:
    # The spec says IDs are auto-incrementing based on the maximum existing ID.
    # Deleting a *non-max* item should not cause IDs to be reused.
    p1 = run_cli(tmp_path, "add", "first")
    p2 = run_cli(tmp_path, "add", "second")
    id1 = _extract_id(p1.stdout)
    id2 = _extract_id(p2.stdout)
    assert id2 > id1

    _ = run_cli(tmp_path, "delete", str(id1))

    p3 = run_cli(tmp_path, "add", "third")
    id3 = _extract_id(p3.stdout)
    assert id3 > id2


def test_persistence(tmp_path: Path) -> None:
    """Persistence via todos.json across separate CLI invocations."""

    p1 = run_cli(tmp_path, "add", "persist me")
    todo_id = _extract_id(p1.stdout)

    # New process should load persisted todos from todos.json
    out = run_cli(tmp_path, "list").stdout
    assert f"[{todo_id}] [ ] persist me" in out

    # Also verify the storage file exists and is JSON-like.
    data_file = tmp_path / "todos.json"
    assert data_file.exists(), "Expected todos.json to be created in the working directory"
    raw = data_file.read_text(encoding="utf-8").strip()
    assert raw.startswith("[") and raw.endswith("]"), f"todos.json did not look like a JSON list: {raw!r}"
