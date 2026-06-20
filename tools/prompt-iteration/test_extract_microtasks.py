"""Tests for extract_microtasks.py."""

import json
import os
import tempfile
from pathlib import Path

import pytest

from extract_microtasks import (
    EARLY_QUIT,
    NEVER_SUBMITTED,
    TIMEOUT,
    _categorize_failure,
    _find_decision_point,
    _steps_to_responses_input,
    extract_microtask,
    scan_eval_dir,
)


# --- Helpers ---

def _make_step(source, message="", tool_calls=None, observation=None, step_id=1):
    step = {"step_id": step_id, "source": source, "message": message}
    if tool_calls:
        step["tool_calls"] = tool_calls
    if observation:
        step["observation"] = observation
    return step


def _make_tool_call(name, args=None, call_id="call_1"):
    return {
        "tool_call_id": call_id,
        "function_name": name,
        "arguments": args or {},
    }


def _make_observation(call_id, content):
    return {"results": [{"source_call_id": call_id, "content": content}]}


def _make_trial_dir(tmp_path, task_name, trial_id, steps, reward="0",
                    duration_seconds=300, agent_info=None):
    """Create a fake trial directory matching the eval data layout."""
    trial_name = f"{task_name}__{trial_id}"
    trial_dir = tmp_path / trial_name
    trial_dir.mkdir()

    # verifier/reward.txt
    (trial_dir / "verifier").mkdir()
    (trial_dir / "verifier" / "reward.txt").write_text(reward)

    # agent/trajectory.json
    (trial_dir / "agent").mkdir()
    trajectory = {
        "schema_version": "ATIF-v1.6",
        "session_id": "test-session",
        "agent": agent_info or {
            "name": "lace",
            "version": "0.1.0",
            "model_name": "openai/gpt-5.2-codex",
        },
        "steps": steps,
    }
    (trial_dir / "agent" / "trajectory.json").write_text(json.dumps(trajectory))

    # result.json with timing
    from datetime import datetime, timedelta
    start = datetime(2026, 3, 7, 5, 0, 0)
    end = start + timedelta(seconds=duration_seconds)
    result = {
        "task_name": task_name,
        "trial_name": trial_name,
        "agent_execution": {
            "started_at": start.isoformat() + "Z",
            "finished_at": end.isoformat() + "Z",
        },
    }
    (trial_dir / "result.json").write_text(json.dumps(result))

    return trial_dir


# --- _categorize_failure tests ---

class TestCategorizeFailure:
    def test_timeout(self):
        steps = [_make_step("agent", step_id=i) for i in range(20)]
        cat, desc = _categorize_failure(steps, 900)
        assert cat == TIMEOUT
        assert "900" in desc

    def test_early_quit(self):
        steps = [_make_step("agent", step_id=i) for i in range(3)]
        cat, desc = _categorize_failure(steps, 30)
        assert cat == EARLY_QUIT
        assert "3" in desc

    def test_never_submitted(self):
        steps = [_make_step("agent", step_id=i) for i in range(15)]
        cat, desc = _categorize_failure(steps, 300)
        assert cat == NEVER_SUBMITTED

    def test_no_duration_not_timeout(self):
        steps = [_make_step("agent", step_id=i) for i in range(20)]
        cat, _ = _categorize_failure(steps, None)
        assert cat == NEVER_SUBMITTED  # can't be timeout without duration


# --- _find_decision_point tests ---

class TestFindDecisionPoint:
    def test_early_quit_finds_first_agent(self):
        steps = [
            _make_step("system", "system prompt", step_id=1),
            _make_step("user", "task description", step_id=2),
            _make_step("agent", "I give up", step_id=3),
        ]
        idx, desc = _find_decision_point(steps, EARLY_QUIT)
        assert idx == 2  # 0-indexed, the agent step

    def test_never_submitted_finds_last_tool_call(self):
        steps = [
            _make_step("system", "prompt", step_id=1),
            _make_step("user", "task", step_id=2),
            _make_step("agent", "", tool_calls=[_make_tool_call("bash")], step_id=3),
            _make_step("agent", "", tool_calls=[_make_tool_call("file_read")], step_id=4),
            _make_step("agent", "Done.", step_id=5),
        ]
        idx, _ = _find_decision_point(steps, NEVER_SUBMITTED)
        assert idx == 3  # last step with tool_calls

    def test_timeout_finds_midpoint(self):
        steps = [_make_step("agent", "", tool_calls=[_make_tool_call("bash")], step_id=i)
                 for i in range(20)]
        idx, _ = _find_decision_point(steps, TIMEOUT)
        assert 5 <= idx <= 15  # somewhere in the middle


# --- _steps_to_responses_input tests ---

class TestStepsToResponsesInput:
    def test_system_step(self):
        steps = [_make_step("system", "You are a benchmark agent")]
        result = _steps_to_responses_input(steps, 0)
        assert len(result) == 1
        assert result[0] == {
            "type": "message",
            "role": "developer",
            "content": "You are a benchmark agent",
        }

    def test_user_step(self):
        steps = [_make_step("user", "Fix the bug")]
        result = _steps_to_responses_input(steps, 0)
        assert len(result) == 1
        assert result[0]["type"] == "message"
        assert result[0]["role"] == "user"

    def test_agent_text_only(self):
        steps = [_make_step("agent", "I'll look at the code")]
        result = _steps_to_responses_input(steps, 0)
        assert len(result) == 1
        assert result[0]["type"] == "message"
        assert result[0]["role"] == "assistant"

    def test_agent_with_tool_call(self):
        steps = [_make_step("agent", "Let me check",
                            tool_calls=[_make_tool_call("bash", {"command": "ls"}, "call_1")],
                            observation=_make_observation("call_1", "file.txt"))]
        result = _steps_to_responses_input(steps, 0)
        # Should produce: assistant message, function_call, function_call_output
        assert len(result) == 3
        assert result[0]["type"] == "message"
        assert result[0]["role"] == "assistant"
        assert result[1]["type"] == "function_call"
        assert result[1]["name"] == "bash"
        assert result[1]["call_id"] == "call_1"
        assert json.loads(result[1]["arguments"]) == {"command": "ls"}
        assert result[2]["type"] == "function_call_output"
        assert result[2]["call_id"] == "call_1"
        assert result[2]["output"] == "file.txt"

    def test_agent_empty_message_skipped(self):
        steps = [_make_step("agent", "",
                            tool_calls=[_make_tool_call("bash", {"command": "ls"}, "c1")])]
        result = _steps_to_responses_input(steps, 0)
        # Empty message should be skipped, only function_call
        assert len(result) == 1
        assert result[0]["type"] == "function_call"

    def test_up_to_index(self):
        steps = [
            _make_step("system", "prompt", step_id=1),
            _make_step("user", "task", step_id=2),
            _make_step("agent", "step 1", step_id=3),
            _make_step("agent", "step 2", step_id=4),
            _make_step("agent", "step 3", step_id=5),
        ]
        result = _steps_to_responses_input(steps, 2)  # up to index 2 (step 3)
        assert len(result) == 3  # system, user, agent step 1
        assert result[2]["content"] == "step 1"

    def test_multiple_tool_calls_in_one_step(self):
        steps = [_make_step("agent", "",
                            tool_calls=[
                                _make_tool_call("bash", {"command": "ls"}, "c1"),
                                _make_tool_call("file_read", {"path": "/app/x"}, "c2"),
                            ],
                            observation={"results": [
                                {"source_call_id": "c1", "content": "files"},
                                {"source_call_id": "c2", "content": "code"},
                            ]})]
        result = _steps_to_responses_input(steps, 0)
        assert len(result) == 4  # 2 function_calls + 2 function_call_outputs
        assert result[0]["type"] == "function_call"
        assert result[1]["type"] == "function_call"
        assert result[2]["type"] == "function_call_output"
        assert result[3]["type"] == "function_call_output"


# --- scan_eval_dir tests ---

class TestScanEvalDir:
    def test_finds_failures(self, tmp_path):
        steps = [
            _make_step("system", "prompt", step_id=1),
            _make_step("user", "task", step_id=2),
            _make_step("agent", "", tool_calls=[_make_tool_call("bash")], step_id=3),
            _make_step("agent", "Done.", step_id=4),
        ]
        _make_trial_dir(tmp_path, "test-task", "abc123", steps, reward="0")
        _make_trial_dir(tmp_path, "test-task", "def456", steps, reward="1")

        trials = scan_eval_dir(str(tmp_path))
        assert len(trials) == 1
        assert trials[0]["task_name"] == "test-task"
        assert trials[0]["trial_id"] == "abc123"

    def test_categorizes_timeout(self, tmp_path):
        steps = [_make_step("agent", "", tool_calls=[_make_tool_call("bash")], step_id=i)
                 for i in range(20)]
        _make_trial_dir(tmp_path, "slow-task", "t1", steps, duration_seconds=1000)

        trials = scan_eval_dir(str(tmp_path))
        assert trials[0]["category"] == TIMEOUT

    def test_categorizes_early_quit(self, tmp_path):
        steps = [
            _make_step("system", "prompt", step_id=1),
            _make_step("agent", "Can't do this", step_id=2),
        ]
        _make_trial_dir(tmp_path, "easy-task", "eq1", steps, duration_seconds=10)

        trials = scan_eval_dir(str(tmp_path))
        assert trials[0]["category"] == EARLY_QUIT


# --- extract_microtask tests ---

class TestExtractMicrotask:
    def test_produces_valid_microtask(self, tmp_path):
        steps = [
            _make_step("system", "You are an agent", step_id=1),
            _make_step("user", "Fix the bug", step_id=2),
            _make_step("agent", "Let me check",
                       tool_calls=[_make_tool_call("bash", {"command": "ls"}, "c1")],
                       observation=_make_observation("c1", "file.txt"),
                       step_id=3),
            _make_step("agent", "Reading file",
                       tool_calls=[_make_tool_call("file_read", {"path": "/app/a"}, "c2")],
                       observation=_make_observation("c2", "code1"),
                       step_id=4),
            _make_step("agent", "Editing",
                       tool_calls=[_make_tool_call("file_write", {"path": "/app/b"}, "c3")],
                       observation=_make_observation("c3", "ok"),
                       step_id=5),
            _make_step("agent", "I see the issue",
                       tool_calls=[_make_tool_call("file_read", {"path": "/app/x"}, "c4")],
                       observation=_make_observation("c4", "code2"),
                       step_id=6),
            _make_step("agent", "Done.", step_id=7),
        ]
        _make_trial_dir(tmp_path, "fix-bug", "xyz", steps, duration_seconds=200)
        trials = scan_eval_dir(str(tmp_path))
        mt = extract_microtask(trials[0])

        assert mt["id"] == "fix-bug__xyz__step6"
        assert mt["task_name"] == "fix-bug"
        assert mt["trial_id"] == "xyz"
        assert mt["failure_category"] == NEVER_SUBMITTED
        assert mt["decision_step"] == 6  # 1-indexed
        assert mt["total_steps"] == 7
        assert mt["model"] == "gpt-5.2-codex"
        assert mt["provider"] == "openai"
        assert len(mt["input"]) > 0
        assert mt["input"][0]["type"] == "message"
        assert mt["input"][0]["role"] == "developer"

    def test_input_is_valid_responses_api_format(self, tmp_path):
        steps = [
            _make_step("system", "prompt", step_id=1),
            _make_step("user", "task", step_id=2),
            _make_step("agent", "thinking",
                       tool_calls=[_make_tool_call("bash", {"command": "echo hi"}, "c1")],
                       observation=_make_observation("c1", "hi"),
                       step_id=3),
            _make_step("agent", "Done", step_id=4),
        ]
        _make_trial_dir(tmp_path, "t", "id1", steps)
        trials = scan_eval_dir(str(tmp_path))
        mt = extract_microtask(trials[0])

        # Validate each input item has required fields
        valid_types = {"message", "function_call", "function_call_output"}
        for item in mt["input"]:
            assert item["type"] in valid_types
            if item["type"] == "message":
                assert "role" in item
                assert "content" in item
                assert item["role"] in ("developer", "user", "assistant")
            elif item["type"] == "function_call":
                assert "name" in item
                assert "arguments" in item
                assert "call_id" in item
            elif item["type"] == "function_call_output":
                assert "call_id" in item
                assert "output" in item
