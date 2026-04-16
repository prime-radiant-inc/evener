"""Shared test fixtures for dashboard tests."""

import json
import os
import sys

import pytest

# Ensure dashboard modules are importable by bare name.
sys.path.insert(0, os.path.dirname(__file__))


@pytest.fixture
def harbor_job_dir(tmp_path):
    """Create a harbor-style job directory with two tasks.

    Structure:
        full-test/full-test/
            build-widget__abc123/
                verifier/reward.txt (1.0 - pass)
                agent/serf-state/sessions/sess-main.transcript.jsonl
                agent/command-0/stdout.txt
                result.json
            fix-bug__def456/
                verifier/reward.txt (0.0 - fail)
                agent/serf-state/sessions/sess-main2.transcript.jsonl
                agent/command-0/stdout.txt
                result.json
    """
    job_root = tmp_path / "full-test" / "full-test"

    # Task 1: build-widget (PASS)
    t1 = job_root / "build-widget__abc123"
    _make_task(t1, reward=1.0, transcript_entries=_passing_transcript(),
               command_txt="serf --state-dir /logs/agent/serf-state -- 'Build a widget that returns 42.'")

    # Task 2: fix-bug (FAIL, wrong answer)
    t2 = job_root / "fix-bug__def456"
    _make_task(t2, reward=0.0, transcript_entries=_failing_transcript(),
               agent_stdout="[communicate:final] submitted\n",
               command_txt="serf --state-dir /logs/agent/serf-state -- 'Fix the bug in widget.'")

    # Run-level metadata files
    (job_root / "manifest.json").write_text(json.dumps({
        "job_name": "full-test",
        "git_sha": "abc1234",
        "model": "openai/gpt-5.3-codex",
        "reps": 1,
        "started_at": "2026-03-01T12:00:00Z",
        "adapter": "serf_agent:SerfAgent",
        "git_branch": "main",
    }))
    (job_root / "config.json").write_text(json.dumps({
        "datasets": [{"name": "terminal-bench", "version": "2.0"}],
    }))
    (job_root / "result.json").write_text(json.dumps({
        "started_at": "2026-03-01T12:00:00Z",
        "finished_at": "2026-03-01T13:30:00Z",
        "n_total_trials": 2,
    }))

    return tmp_path / "full-test"


@pytest.fixture
def harbor_job_dir_with_reps(tmp_path):
    """Job directory where build-widget has 2 trials (reps=2).

    Structure:
        reps-test/reps-test/
            build-widget__abc123/  (reward=1.0, best)
            build-widget__xyz789/  (reward=0.0, duplicate from reps)
            fix-bug__def456/       (reward=0.0, single trial)
    """
    job_root = tmp_path / "reps-test" / "reps-test"

    # Trial 1: build-widget (PASS)
    t1 = job_root / "build-widget__abc123"
    _make_task(t1, reward=1.0, transcript_entries=_passing_transcript())

    # Trial 2: build-widget (FAIL, duplicate from reps)
    t2 = job_root / "build-widget__xyz789"
    _make_task(t2, reward=0.0, transcript_entries=_failing_transcript())

    # Single trial: fix-bug (FAIL)
    t3 = job_root / "fix-bug__def456"
    _make_task(t3, reward=0.0, transcript_entries=_failing_transcript(),
               agent_stdout="[communicate:final] submitted\n")

    return tmp_path / "reps-test"


def _make_task(task_dir, reward, transcript_entries, agent_stdout="",
               result_json=None, api_log_entries=None, command_txt=None):
    """Create a single task directory with required files."""
    # Verifier
    verifier = task_dir / "verifier"
    verifier.mkdir(parents=True)
    (verifier / "reward.txt").write_text(str(reward))
    if reward < 1.0:
        (verifier / "test-stdout.txt").write_text(
            "FAIL: test_widget (expected 42, got 0)\n"
        )

    # Sessions
    sessions = task_dir / "agent" / "serf-state" / "sessions"
    sessions.mkdir(parents=True)

    # Write transcript
    tf = sessions / "sess-main.transcript.jsonl"
    lines = [json.dumps(e) for e in transcript_entries]
    tf.write_text("\n".join(lines) + "\n")

    # Agent stdout
    cmd_dir = task_dir / "agent" / "command-0"
    cmd_dir.mkdir(parents=True)
    (cmd_dir / "stdout.txt").write_text(agent_stdout)

    # command.txt (optional)
    if command_txt is not None:
        (cmd_dir / "command.txt").write_text(command_txt)

    # result.json
    result_data = result_json or {
        "config": {"model": "gpt-5.3-codex"},
        "started_at": "2026-03-01T12:00:00Z",
        "finished_at": "2026-03-01T12:05:30Z",
    }
    (task_dir / "result.json").write_text(json.dumps(result_data))

    # api.jsonl (optional)
    if api_log_entries is not None:
        api_file = task_dir / "agent" / "serf-state" / "api.jsonl"
        lines = [json.dumps(e) for e in api_log_entries]
        api_file.write_text("\n".join(lines) + "\n")


def _make_task_with_artifacts(task_dir, reward, transcript_entries,
                              artifacts=None, **kwargs):
    """Create a task directory with optional artifact files.

    artifacts: dict mapping relative paths to content strings,
    e.g. {"main.py": "print(42)", "lib/util.py": "# util"}
    """
    _make_task(task_dir, reward, transcript_entries, **kwargs)
    if artifacts:
        artifacts_dir = task_dir / "agent" / "artifacts"
        for relpath, content in artifacts.items():
            f = artifacts_dir / relpath
            f.parent.mkdir(parents=True, exist_ok=True)
            f.write_text(content)


@pytest.fixture
def harbor_job_dir_with_api(tmp_path):
    """Create a harbor-style job directory with api.jsonl data.

    Structure mirrors harbor_job_dir but the passing task has 3 api.jsonl
    entries (one empty response) for testing API metrics.
    """
    job_root = tmp_path / "full-test" / "full-test"

    api_entries = [
        {"ts": "2026-03-01T12:00:01Z", "session_id": "sess-main", "round": 1,
         "request": {"model": "gpt-5.3-codex", "provider": "openai",
                     "message_count": 3, "tool_count": 5},
         "response": {"model": "gpt-5.3-codex", "finish_reason": "tool_calls",
                      "text_length": 50, "tool_call_count": 1,
                      "usage": {"input_tokens": 500, "output_tokens": 30}},
         "latency_ms": 1200},
        {"ts": "2026-03-01T12:00:02Z", "session_id": "sess-main", "round": 2,
         "request": {"model": "gpt-5.3-codex", "provider": "openai",
                     "message_count": 4, "tool_count": 5},
         "response": {"model": "gpt-5.3-codex", "finish_reason": "tool_calls",
                      "text_length": 0, "tool_call_count": 0,
                      "usage": {"input_tokens": 600, "output_tokens": 4}},
         "latency_ms": 800},
        {"ts": "2026-03-01T12:00:03Z", "session_id": "sess-main", "round": 3,
         "request": {"model": "gpt-5.3-codex", "provider": "openai",
                     "message_count": 5, "tool_count": 5},
         "response": {"model": "gpt-5.3-codex", "finish_reason": "tool_calls",
                      "text_length": 100, "tool_call_count": 1,
                      "usage": {"input_tokens": 700, "output_tokens": 25}},
         "latency_ms": 1500},
    ]

    # Task 1: build-widget (PASS) with api.jsonl
    t1 = job_root / "build-widget__abc123"
    _make_task(t1, reward=1.0, transcript_entries=_passing_transcript(),
               api_log_entries=api_entries)

    # Task 2: fix-bug (FAIL, no api.jsonl)
    t2 = job_root / "fix-bug__def456"
    _make_task(t2, reward=0.0, transcript_entries=_failing_transcript(),
               agent_stdout="[communicate:final] submitted\n")

    return tmp_path / "full-test"


def _passing_transcript():
    """Transcript entries for a passing task: explore, edit, exec, submit."""
    return [
        {"kind": "header", "format_version": 1, "session_id": "sess-main",
         "created_at": "2026-03-01T12:00:00Z", "model": "gpt-5.3-codex",
         "profile_id": "openai", "depth": 0},
        {"kind": "entry", "seq": 0, "turn": {
            "kind": "USER_INPUT",
            "message": {"role": "user", "content": [
                {"kind": "text", "text": "Build a widget that returns 42."}
            ]},
            "timestamp": "2026-03-01T12:00:01Z",
        }},
        {"kind": "entry", "seq": 1, "turn": {
            "kind": "ASSISTANT",
            "message": {"role": "assistant", "content": [
                {"kind": "text", "text": "I'll start by reading the workspace."},
                {"kind": "tool_call", "tool_call": {
                    "id": "tc-1", "name": "glob",
                    "arguments": "{\"pattern\": \"*\"}",
                }},
            ]},
            "timestamp": "2026-03-01T12:00:02Z",
            "usage": {"input_tokens": 500, "output_tokens": 30},
        }},
        {"kind": "entry", "seq": 2, "turn": {
            "kind": "TOOL_RESULTS",
            "message": {"role": "tool", "content": [
                {"kind": "tool_result", "tool_result": {
                    "tool_call_id": "tc-1", "name": "glob",
                    "content": "main.py\ntest_main.py", "is_error": False,
                }},
            ]},
        }},
        {"kind": "entry", "seq": 3, "turn": {
            "kind": "ASSISTANT",
            "message": {"role": "assistant", "content": [
                {"kind": "tool_call", "tool_call": {
                    "id": "tc-2", "name": "apply_patch",
                    "arguments": "{\"patch\": \"--- a/main.py\\n+++ b/main.py\\n@@ -1 +1 @@\\n-pass\\n+def widget(): return 42\"}",
                }},
            ]},
            "timestamp": "2026-03-01T12:00:03Z",
            "usage": {"input_tokens": 600, "output_tokens": 40},
        }},
        {"kind": "entry", "seq": 4, "turn": {
            "kind": "TOOL_RESULTS",
            "message": {"role": "tool", "content": [
                {"kind": "tool_result", "tool_result": {
                    "tool_call_id": "tc-2", "name": "apply_patch",
                    "content": "Applied.", "is_error": False,
                }},
            ]},
        }},
        {"kind": "entry", "seq": 5, "turn": {
            "kind": "ASSISTANT",
            "message": {"role": "assistant", "content": [
                {"kind": "tool_call", "tool_call": {
                    "id": "tc-3", "name": "shell",
                    "arguments": "{\"command\": \"python -c 'from main import widget; print(widget())'\"}",
                }},
            ]},
            "timestamp": "2026-03-01T12:00:04Z",
            "usage": {"input_tokens": 700, "output_tokens": 25},
        }},
        {"kind": "entry", "seq": 6, "turn": {
            "kind": "TOOL_RESULTS",
            "message": {"role": "tool", "content": [
                {"kind": "tool_result", "tool_result": {
                    "tool_call_id": "tc-3", "name": "shell",
                    "content": "42", "is_error": False,
                }},
            ]},
        }},
        {"kind": "entry", "seq": 7, "turn": {
            "kind": "ASSISTANT",
            "message": {"role": "assistant", "content": [
                {"kind": "tool_call", "tool_call": {
                    "id": "tc-4", "name": "communicate",
                    "arguments": "{\"kind\": \"final\", \"message\": \"Widget implemented.\"}",
                }},
            ]},
            "timestamp": "2026-03-01T12:00:05Z",
            "usage": {"input_tokens": 750, "output_tokens": 20},
        }},
    ]


def _failing_transcript():
    """Transcript for a failing task: explore, wrong submit."""
    return [
        {"kind": "header", "format_version": 1, "session_id": "sess-main2",
         "created_at": "2026-03-01T13:00:00Z", "model": "gpt-5.3-codex",
         "profile_id": "openai", "depth": 0},
        {"kind": "entry", "seq": 0, "turn": {
            "kind": "USER_INPUT",
            "message": {"role": "user", "content": [
                {"kind": "text", "text": "Fix the bug in widget."}
            ]},
            "timestamp": "2026-03-01T13:00:01Z",
        }},
        {"kind": "entry", "seq": 1, "turn": {
            "kind": "ASSISTANT",
            "message": {"role": "assistant", "content": [
                {"kind": "tool_call", "tool_call": {
                    "id": "tc-10", "name": "read_file",
                    "arguments": "{\"path\": \"main.py\"}",
                }},
            ]},
            "timestamp": "2026-03-01T13:00:02Z",
            "usage": {"input_tokens": 400, "output_tokens": 15},
        }},
        {"kind": "entry", "seq": 2, "turn": {
            "kind": "TOOL_RESULTS",
            "message": {"role": "tool", "content": [
                {"kind": "tool_result", "tool_result": {
                    "tool_call_id": "tc-10", "name": "read_file",
                    "content": "def widget(): return 0", "is_error": False,
                }},
            ]},
        }},
        {"kind": "entry", "seq": 3, "turn": {
            "kind": "ASSISTANT",
            "message": {"role": "assistant", "content": [
                {"kind": "tool_call", "tool_call": {
                    "id": "tc-11", "name": "communicate",
                    "arguments": "{\"kind\": \"final\", \"message\": \"Looks fine to me.\"}",
                }},
            ]},
            "timestamp": "2026-03-01T13:00:03Z",
            "usage": {"input_tokens": 500, "output_tokens": 10},
        }},
    ]


# ------------------------------------------------------------------
# Experiment data fixtures
# ------------------------------------------------------------------

@pytest.fixture
def experiment_dir(tmp_path):
    """Create an experiment data directory with runs, tasks, and scoreboard.

    Structure:
        runs/
            wave-abc1234-20260401-0800.json  (wave run, 2 tasks)
            v10-deleg-goldplate.json          (experiment run, 1 task)
        tasks/
            chess-best-move.json
        scoreboard.json
    """
    runs_dir = tmp_path / "runs"
    runs_dir.mkdir()
    tasks_dir = tmp_path / "tasks"
    tasks_dir.mkdir()

    # Wave run: 2 tasks, one perfect and one partial
    wave_run = {
        "run_id": "wave-abc1234-20260401-0800",
        "date": "2026-04-01",
        "git_sha": "abc1234",
        "model": "openai/gpt-5.4-mini",
        "variant": "baseline",
        "results": {
            "chess-best-move": {
                "score": 0.667,
                "reps": [1.0, 0.0, 1.0],
                "reps_pass": 2,
                "reps_total": 3,
            },
            "build-cython-ext": {
                "score": 1.0,
                "reps": [1.0, 1.0, 1.0],
                "reps_pass": 3,
                "reps_total": 3,
            },
        },
        "s3_prefix": "s3://bucket/runs/wave-abc1234-20260401-0800/",
    }
    (runs_dir / "wave-abc1234-20260401-0800.json").write_text(
        json.dumps(wave_run)
    )

    # Experiment run: 1 task, older date
    exp_run = {
        "run_id": "v10-deleg-goldplate",
        "date": "2026-03-25",
        "git_sha": "def5678",
        "model": "openai/gpt-5.4-mini",
        "variant": "delegation/goldplating fixes",
        "results": {
            "chess-best-move": {
                "score": 0.0,
                "reps": [0.0, 0.0, 0.0],
                "reps_pass": 0,
                "reps_total": 3,
            },
        },
        "s3_prefix": "s3://bucket/runs/v10-deleg-goldplate/",
    }
    (runs_dir / "v10-deleg-goldplate.json").write_text(
        json.dumps(exp_run)
    )

    # Task file
    task_data = {
        "task": "chess-best-move",
        "current_score": 1.0,
        "current_run": "wave-abc1234-20260401-0800",
        "current_date": "2026-04-02",
        "status": "tested",
        "notes": "",
        "history": [
            {
                "run_id": "v10-deleg-goldplate",
                "date": "2026-03-25",
                "git_sha": "def5678",
                "model": "openai/gpt-5.4-mini",
                "score": 0.0,
                "reps": [0.0, 0.0, 0.0],
            },
            {
                "run_id": "wave-abc1234-20260401-0800",
                "date": "2026-04-01",
                "git_sha": "abc1234",
                "model": "openai/gpt-5.4-mini",
                "score": 0.667,
                "reps": [1.0, 0.0, 1.0],
            },
        ],
    }
    (tasks_dir / "chess-best-move.json").write_text(
        json.dumps(task_data)
    )

    # Scoreboard
    scoreboard = {
        "model": "openai/gpt-5.4-mini",
        "total_tasks": 3,
        "tested_tasks": 3,
        "mean_score": 0.556,
        "tasks": {
            "chess-best-move": {
                "score": 0.667,
                "last_run": "wave-abc1234-20260401-0800",
                "last_date": "2026-04-01",
                "reps": [1.0, 0.0, 1.0],
                "status": "tested",
            },
            "build-cython-ext": {
                "score": 1.0,
                "last_run": "wave-abc1234-20260401-0800",
                "last_date": "2026-04-01",
                "reps": [1.0, 1.0, 1.0],
                "status": "tested",
            },
            "fix-bug": {
                "score": 0.0,
                "last_run": "wave-abc1234-20260401-0800",
                "last_date": "2026-04-01",
                "reps": [0.0, 0.0, 0.0],
                "status": "tested",
            },
        },
    }
    (tmp_path / "scoreboard.json").write_text(json.dumps(scoreboard))

    return tmp_path
