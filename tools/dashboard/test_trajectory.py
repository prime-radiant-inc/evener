"""Tests for trajectory parser: round classification and trajectory building."""

import pytest

from trajectory import classify_tool, classify_round, build_trajectory


class TestClassifyTool:
    """classify_tool() maps a single tool name to a category via patterns."""

    # Serf's native tool names
    def test_serf_read_file(self):
        assert classify_tool("read_file") == "EXPLORE"

    def test_serf_glob(self):
        assert classify_tool("glob") == "EXPLORE"

    def test_serf_grep(self):
        assert classify_tool("grep") == "EXPLORE"

    def test_serf_apply_patch(self):
        assert classify_tool("apply_patch") == "EDIT"

    def test_serf_edit_file(self):
        assert classify_tool("edit_file") == "EDIT"

    def test_serf_write_file(self):
        assert classify_tool("write_file") == "EDIT"

    def test_serf_shell(self):
        assert classify_tool("shell") == "EXEC"

    def test_serf_spawn_agent(self):
        assert classify_tool("spawn_agent") == "SPAWN"

    def test_serf_communicate(self):
        assert classify_tool("communicate") == "SUBMIT"

    def test_serf_submit_result(self):
        assert classify_tool("submit_result") == "SUBMIT"

    # gpt-5.3-codex tool names
    def test_codex_exec_command(self):
        assert classify_tool("exec_command") == "EXEC"

    def test_codex_list_dir(self):
        assert classify_tool("list_dir") == "EXPLORE"

    def test_codex_grep_files(self):
        assert classify_tool("grep_files") == "EXPLORE"

    def test_codex_read_many_files(self):
        assert classify_tool("read_many_files") == "EXPLORE"

    # Claude Code tool names
    def test_claude_bash(self):
        assert classify_tool("bash") == "EXEC"

    def test_claude_replace(self):
        assert classify_tool("replace") == "EDIT"

    def test_claude_insert(self):
        assert classify_tool("insert") == "EDIT"

    # Review tools
    def test_approve(self):
        assert classify_tool("approve") == "REVIEW"

    def test_reject(self):
        assert classify_tool("reject") == "REVIEW"

    # Task tools
    def test_task_list(self):
        assert classify_tool("task_list") == "TASK"

    # Unknown tools return None
    def test_unknown_tool(self):
        assert classify_tool("completely_custom_xyz") is None

    # Case insensitivity
    def test_case_insensitive(self):
        assert classify_tool("ReadFile") == "EXPLORE"
        assert classify_tool("EXEC_COMMAND") == "EXEC"
        assert classify_tool("WriteFile") == "EDIT"


class TestClassifyRound:
    """classify_round() maps tool names to action categories."""

    def test_explore_tools(self):
        assert classify_round(["read_file"], has_text=False) == "EXPLORE"
        assert classify_round(["glob"], has_text=False) == "EXPLORE"
        assert classify_round(["grep"], has_text=False) == "EXPLORE"

    def test_edit_tools(self):
        assert classify_round(["apply_patch"], has_text=False) == "EDIT"
        assert classify_round(["edit_file"], has_text=False) == "EDIT"
        assert classify_round(["write_file"], has_text=False) == "EDIT"

    def test_exec_tool(self):
        assert classify_round(["shell"], has_text=False) == "EXEC"

    def test_spawn_tool(self):
        assert classify_round(["spawn_agent"], has_text=False) == "SPAWN"

    def test_submit_tools(self):
        assert classify_round(["communicate"], has_text=False) == "SUBMIT"
        assert classify_round(["submit_result"], has_text=False) == "SUBMIT"

    def test_plan_text_only(self):
        assert classify_round([], has_text=True) == "PLAN"

    def test_error_no_tools_no_text(self):
        assert classify_round([], has_text=False) == "ERROR"

    def test_mixed_submit_wins(self):
        """SUBMIT priority beats EXPLORE."""
        assert classify_round(["read_file", "communicate"], has_text=True) == "SUBMIT"

    def test_mixed_spawn_wins_over_edit(self):
        assert classify_round(["spawn_agent", "apply_patch"], has_text=False) == "SPAWN"

    def test_mixed_edit_wins_over_exec(self):
        assert classify_round(["shell", "apply_patch"], has_text=False) == "EDIT"

    def test_mixed_exec_wins_over_explore(self):
        assert classify_round(["read_file", "shell"], has_text=False) == "EXEC"

    def test_text_with_tools_uses_tool_classification(self):
        """Text + tools -> classified by tools, not PLAN."""
        assert classify_round(["glob"], has_text=True) == "EXPLORE"

    # New tests for generic classification
    def test_codex_exec_command(self):
        assert classify_round(["exec_command"], has_text=False) == "EXEC"

    def test_codex_list_dir(self):
        assert classify_round(["list_dir"], has_text=False) == "EXPLORE"

    def test_codex_grep_files(self):
        assert classify_round(["grep_files"], has_text=False) == "EXPLORE"

    def test_review_tools(self):
        assert classify_round(["approve"], has_text=False) == "REVIEW"
        assert classify_round(["reject"], has_text=False) == "REVIEW"

    def test_task_tools(self):
        assert classify_round(["task_list"], has_text=False) == "TASK"

    def test_unknown_tool_returns_tool_not_error(self):
        """Unrecognized tools are TOOL, never ERROR."""
        assert classify_round(["completely_custom_xyz"], has_text=False) == "TOOL"

    def test_unknown_tool_with_text_returns_tool(self):
        """Unrecognized tools with text are still TOOL."""
        assert classify_round(["completely_custom_xyz"], has_text=True) == "TOOL"

    def test_mixed_known_and_unknown(self):
        """Known tool category wins over unknown."""
        assert classify_round(["completely_custom_xyz", "read_file"], has_text=False) == "EXPLORE"


class TestBuildTrajectory:
    """build_trajectory() converts session entries into rounds."""

    def _make_session(self, entries):
        """Wrap entries with a header to form a session dict."""
        return {
            "session_id": "test-sess",
            "model": "gpt-5.3-codex",
            "depth": 0,
            "entries": entries,
        }

    def _assistant_entry(self, seq, text=None, tool_calls=None, usage=None):
        content = []
        if text:
            content.append({"kind": "text", "text": text})
        if tool_calls:
            for tc in tool_calls:
                content.append({"kind": "tool_call", "tool_call": tc})
        return {
            "kind": "entry", "seq": seq,
            "turn": {
                "kind": "ASSISTANT",
                "message": {"role": "assistant", "content": content},
                "usage": usage or {"input_tokens": 100, "output_tokens": 20},
            },
        }

    def _tool_results_entry(self, seq, results):
        content = []
        for r in results:
            content.append({"kind": "tool_result", "tool_result": r})
        return {
            "kind": "entry", "seq": seq,
            "turn": {
                "kind": "TOOL_RESULTS",
                "message": {"role": "tool", "content": content},
            },
        }

    def _user_entry(self, seq, text="Do something."):
        return {
            "kind": "entry", "seq": seq,
            "turn": {
                "kind": "USER_INPUT",
                "message": {"role": "user", "content": [
                    {"kind": "text", "text": text},
                ]},
            },
        }

    def test_single_explore_round(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, text="Let me look.", tool_calls=[
                {"id": "tc-1", "name": "glob", "arguments": '{"pattern": "*"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "glob",
                 "content": "main.py", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert len(rounds) == 1
        assert rounds[0]["action"] == "EXPLORE"
        assert rounds[0]["round"] == 1

    def test_plan_round_text_only(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, text="I need to think about this approach."),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert len(rounds) == 1
        assert rounds[0]["action"] == "PLAN"

    def test_multiple_rounds(self):
        entries = [
            self._user_entry(0),
            # Round 1: explore
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "glob", "arguments": '{"pattern": "*.py"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "glob",
                 "content": "main.py", "is_error": False},
            ]),
            # Round 2: edit
            self._assistant_entry(3, tool_calls=[
                {"id": "tc-2", "name": "apply_patch",
                 "arguments": '{"patch": "..."}'},
            ]),
            self._tool_results_entry(4, [
                {"tool_call_id": "tc-2", "name": "apply_patch",
                 "content": "Applied.", "is_error": False},
            ]),
            # Round 3: submit
            self._assistant_entry(5, tool_calls=[
                {"id": "tc-3", "name": "communicate",
                 "arguments": '{"kind": "final", "message": "Done."}'},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert len(rounds) == 3
        assert rounds[0]["action"] == "EXPLORE"
        assert rounds[1]["action"] == "EDIT"
        assert rounds[2]["action"] == "SUBMIT"

    def test_round_numbers_sequential(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, text="Planning..."),
            self._assistant_entry(2, tool_calls=[
                {"id": "tc-1", "name": "shell",
                 "arguments": '{"command": "ls"}'},
            ]),
            self._tool_results_entry(3, [
                {"tool_call_id": "tc-1", "name": "shell",
                 "content": "file.py", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["round"] == 1
        assert rounds[1]["round"] == 2

    def test_round_has_usage(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, text="Thinking.",
                                  usage={"input_tokens": 500, "output_tokens": 42}),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["usage"]["output_tokens"] == 42

    def test_round_has_tool_calls(self):
        tc = {"id": "tc-1", "name": "glob", "arguments": '{"pattern": "*"}'}
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[tc]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "glob",
                 "content": "a.py", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert len(rounds[0]["tool_calls"]) == 1
        assert rounds[0]["tool_calls"][0]["name"] == "glob"

    def test_round_has_tool_results(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "shell",
                 "arguments": '{"command": "echo hi"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "shell",
                 "content": "hi", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert len(rounds[0]["tool_results"]) == 1
        assert rounds[0]["tool_results"][0]["content"] == "hi"

    def test_steering_entries_skipped(self):
        entries = [
            self._user_entry(0),
            {"kind": "entry", "seq": 1, "turn": {
                "kind": "STEERING",
                "message": {"role": "user", "content": [
                    {"kind": "text", "text": "[SESSION ORIENTATION]"},
                ]},
            }},
            self._assistant_entry(2, text="Got it."),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert len(rounds) == 1
        assert rounds[0]["action"] == "PLAN"

    def test_empty_entries(self):
        session = self._make_session([])
        rounds = build_trajectory(session)
        assert rounds == []

    def test_user_input_only_no_rounds(self):
        entries = [self._user_entry(0)]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds == []

    def test_round_has_raw_entries(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, text="Plan only."),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert len(rounds[0]["raw_entries"]) >= 1

    def test_codex_exec_command_classified_correctly(self):
        """gpt-5.3-codex exec_command should classify as EXEC, not ERROR."""
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "exec_command",
                 "arguments": '{"command": "ls -la /app"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "exec_command",
                 "content": "total 32\ndrwxr-xr-x...", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["action"] == "EXEC"
        assert "ls -la /app" in rounds[0]["summary"]

    def test_nonfinal_communicate_round_is_plan_not_submit(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "communicate",
                 "arguments": '{"kind": "ask", "message": "Which file should I edit?"}'},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["action"] == "PLAN"
        assert rounds[0]["summary"] == 'communicate:ask("Which file should I edit?")'

    def test_nonfinal_communicate_does_not_override_other_tool_action(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "read_file",
                 "arguments": '{"path": "main.py"}'},
                {"id": "tc-2", "name": "communicate",
                 "arguments": '{"kind": "message", "message": "I am checking main.py next."}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "read_file",
                 "content": "def widget(): return 0", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["action"] == "EXPLORE"
        assert "main.py" in rounds[0]["summary"]


class TestSummaryGeneration:
    """Round summaries are generated based on action type."""

    def _make_session(self, entries):
        return {
            "session_id": "test-sess",
            "model": "gpt-5.3-codex",
            "depth": 0,
            "entries": entries,
        }

    def _user_entry(self, seq):
        return {
            "kind": "entry", "seq": seq,
            "turn": {
                "kind": "USER_INPUT",
                "message": {"role": "user", "content": [
                    {"kind": "text", "text": "task"},
                ]},
            },
        }

    def _assistant_entry(self, seq, text=None, tool_calls=None):
        content = []
        if text:
            content.append({"kind": "text", "text": text})
        if tool_calls:
            for tc in tool_calls:
                content.append({"kind": "tool_call", "tool_call": tc})
        return {
            "kind": "entry", "seq": seq,
            "turn": {
                "kind": "ASSISTANT",
                "message": {"role": "assistant", "content": content},
                "usage": {"input_tokens": 100, "output_tokens": 20},
            },
        }

    def _tool_results_entry(self, seq, results):
        content = []
        for r in results:
            content.append({"kind": "tool_result", "tool_result": r})
        return {
            "kind": "entry", "seq": seq,
            "turn": {
                "kind": "TOOL_RESULTS",
                "message": {"role": "tool", "content": content},
            },
        }

    def test_plan_summary_quotes_text(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, text="I need to think about the approach carefully."),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["summary"].startswith('"')
        assert "I need to think" in rounds[0]["summary"]

    def test_plan_summary_truncated(self):
        long_text = "A" * 200
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, text=long_text),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert len(rounds[0]["summary"]) <= 90  # ~80 + quotes + ellipsis

    def test_submit_summary(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "communicate",
                 "arguments": '{"kind": "final", "message": "Widget built successfully."}'},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert "communicate:final" in rounds[0]["summary"]
        assert "Widget built" in rounds[0]["summary"]

    def test_plan_summary_uses_nonfinal_communicate_message(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "communicate",
                 "arguments": '{"kind": "message", "message": "I am still validating the fix."}'},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["action"] == "PLAN"
        assert rounds[0]["summary"] == 'communicate:message("I am still validating the fix.")'

    def test_spawn_summary(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "spawn_agent",
                 "arguments": '{"agent": "test-engineer", "task": "Write tests for the widget module."}'},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert "test-engineer" in rounds[0]["summary"]
        assert "Write tests" in rounds[0]["summary"]

    def test_exec_summary_shell(self):
        """Serf's native shell tool."""
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "shell",
                 "arguments": '{"command": "python -m pytest tests/"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "shell",
                 "content": "3 passed", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert "python -m pytest" in rounds[0]["summary"]

    def test_exec_summary_exec_command(self):
        """gpt-5.3-codex exec_command tool."""
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "exec_command",
                 "arguments": '{"command": "apt-get install git"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "exec_command",
                 "content": "installed", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert "apt-get install" in rounds[0]["summary"]

    def test_edit_summary_shows_filename(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "apply_patch",
                 "arguments": '{"patch": "--- a/main.py\\n+++ b/main.py\\n"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "apply_patch",
                 "content": "Applied.", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert "main.py" in rounds[0]["summary"]

    def test_edit_summary_write_file_with_path(self):
        """write_file with path arg shows the file path."""
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "write_file",
                 "arguments": '{"path": "/app/hello.py", "content": "print(1)"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "write_file",
                 "content": "Written.", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert "/app/hello.py" in rounds[0]["summary"]

    def test_explore_summary_shows_pattern(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "grep",
                 "arguments": '{"pattern": "def widget", "path": "src/"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "grep",
                 "content": "main.py:1:def widget():", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        summary = rounds[0]["summary"]
        assert "def widget" in summary or "src/" in summary

    def test_explore_summary_read_file(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "read_file",
                 "arguments": '{"path": "config.yaml"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "read_file",
                 "content": "key: value", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert "config.yaml" in rounds[0]["summary"]

    def test_explore_summary_list_dir(self):
        """gpt-5.3-codex list_dir tool."""
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "list_dir",
                 "arguments": '{"path": "/app/src"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "list_dir",
                 "content": "main.py\nutils.py", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert "/app/src" in rounds[0]["summary"]

    def test_multiple_tools_in_summary(self):
        """Multiple edit tools show multiple files."""
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "apply_patch",
                 "arguments": '{"patch": "--- a/foo.py\\n+++ b/foo.py\\n"}'},
                {"id": "tc-2", "name": "apply_patch",
                 "arguments": '{"patch": "--- a/bar.py\\n+++ b/bar.py\\n"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "apply_patch",
                 "content": "Applied.", "is_error": False},
                {"tool_call_id": "tc-2", "name": "apply_patch",
                 "content": "Applied.", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert "foo.py" in rounds[0]["summary"]
        assert "bar.py" in rounds[0]["summary"]

    def test_review_summary(self):
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "reject",
                 "arguments": '{"reason": "Missing test coverage for edge cases"}'},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["action"] == "REVIEW"
        assert "Missing test" in rounds[0]["summary"]

    def test_unknown_tool_summary(self):
        """Unrecognized tools show name and first interesting arg."""
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "custom_fancy_tool",
                 "arguments": '{"query": "find all widgets"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "custom_fancy_tool",
                 "content": "found 3", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["action"] == "TOOL"
        assert "custom_fancy_tool" in rounds[0]["summary"]
        assert "find all widgets" in rounds[0]["summary"]

    def test_unknown_tool_no_args(self):
        """Unrecognized tool with no interesting args shows just the name."""
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "mystery_tool",
                 "arguments": '{"foo": "bar"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "mystery_tool",
                 "content": "ok", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["action"] == "TOOL"
        assert "mystery_tool" in rounds[0]["summary"]

    def test_error_only_for_truly_empty(self):
        """ERROR only when no tools AND no text."""
        entries = [
            self._user_entry(0),
            self._assistant_entry(1),  # no text, no tools
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["action"] == "ERROR"
        assert "(empty response)" in rounds[0]["summary"]

    def test_round_has_duration_ms(self):
        """duration_ms from tool results is summed per round."""
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "shell",
                 "arguments": '{"command": "echo hi"}'},
                {"id": "tc-2", "name": "read_file",
                 "arguments": '{"path": "x.py"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "shell",
                 "content": "hi", "is_error": False, "duration_ms": 1500},
                {"tool_call_id": "tc-2", "name": "read_file",
                 "content": "code", "is_error": False, "duration_ms": 30},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["duration_ms"] == 1530

    def test_round_duration_ms_zero_when_missing(self):
        """Rounds without duration_ms in tool results default to 0."""
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, tool_calls=[
                {"id": "tc-1", "name": "glob",
                 "arguments": '{"pattern": "*"}'},
            ]),
            self._tool_results_entry(2, [
                {"tool_call_id": "tc-1", "name": "glob",
                 "content": "main.py", "is_error": False},
            ]),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["duration_ms"] == 0

    def test_plan_round_duration_ms_zero(self):
        """Text-only rounds (no tool calls) have duration_ms 0."""
        entries = [
            self._user_entry(0),
            self._assistant_entry(1, text="Let me think about this."),
        ]
        session = self._make_session(entries)
        rounds = build_trajectory(session)
        assert rounds[0]["duration_ms"] == 0
