"""Tests for replay_microtask.py."""

import json
import tempfile
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from replay_microtask import (
    _load_tool_definitions,
    _replace_system_prompt,
    replay_microtask,
)


# --- Helpers ---

def _make_microtask(n_input_items=5):
    """Create a minimal microtask dict."""
    input_items = [
        {"type": "message", "role": "developer", "content": "You are a benchmark agent"},
        {"type": "message", "role": "user", "content": "Fix the bug in /app/main.py"},
    ]
    for i in range(n_input_items - 2):
        input_items.append({
            "type": "function_call",
            "name": "bash",
            "arguments": json.dumps({"command": f"echo step{i}"}),
            "call_id": f"call_{i}",
        })
    return {
        "id": "test-task__abc__step5",
        "task_name": "test-task",
        "trial_id": "abc",
        "failure_category": "NEVER_SUBMITTED",
        "decision_step": 5,
        "total_steps": 6,
        "input": input_items,
        "actual_next_action": "Said: Done.",
        "model": "gpt-5.2-codex",
        "provider": "openai",
    }


# --- _replace_system_prompt tests ---

class TestReplaceSystemPrompt:
    def test_replaces_first_developer_message(self):
        items = [
            {"type": "message", "role": "developer", "content": "Old prompt"},
            {"type": "message", "role": "user", "content": "Task"},
        ]
        result = _replace_system_prompt(items, "New prompt")
        assert result[0]["content"] == "New prompt"
        assert result[1]["content"] == "Task"

    def test_only_replaces_first(self):
        items = [
            {"type": "message", "role": "developer", "content": "First"},
            {"type": "message", "role": "user", "content": "Task"},
            {"type": "message", "role": "developer", "content": "Second"},
        ]
        result = _replace_system_prompt(items, "Replaced")
        assert result[0]["content"] == "Replaced"
        assert result[2]["content"] == "Second"  # untouched

    def test_no_developer_message(self):
        items = [
            {"type": "message", "role": "user", "content": "Task"},
        ]
        result = _replace_system_prompt(items, "New prompt")
        assert len(result) == 1
        assert result[0]["content"] == "Task"  # unchanged


# --- _load_tool_definitions tests ---

class TestLoadToolDefinitions:
    def test_default_tools(self):
        tools = _load_tool_definitions()
        assert len(tools) > 0
        names = {t["name"] for t in tools}
        assert "bash" in names
        assert "file_read" in names
        assert "file_write" in names

    def test_all_tools_have_required_fields(self):
        tools = _load_tool_definitions()
        for tool in tools:
            assert tool["type"] == "function"
            assert "name" in tool
            assert "parameters" in tool
            assert tool["parameters"]["type"] == "object"

    def test_loads_from_file(self, tmp_path):
        custom_tools = [{"type": "function", "name": "custom", "parameters": {"type": "object"}}]
        tool_file = tmp_path / "tools.json"
        tool_file.write_text(json.dumps(custom_tools))

        tools = _load_tool_definitions(str(tool_file))
        assert len(tools) == 1
        assert tools[0]["name"] == "custom"


# --- replay_microtask tests ---

class TestReplayMicrotask:
    def test_dry_run(self):
        mt = _make_microtask()
        result = replay_microtask(mt, dry_run=True)

        assert result["dry_run"] is True
        assert result["microtask_id"] == "test-task__abc__step5"
        assert result["model"] == "gpt-5.2-codex"
        assert result["n_input_items"] == 5
        assert result["n_tools"] > 0

    def test_dry_run_with_persona(self):
        mt = _make_microtask()
        result = replay_microtask(mt, persona_text="New persona", dry_run=True)
        assert result["persona_replaced"] is True

    def test_model_override(self):
        mt = _make_microtask()
        result = replay_microtask(mt, model_override="gpt-5.3-codex", dry_run=True)
        assert result["model"] == "gpt-5.3-codex"

    @patch("replay_microtask.openai")
    def test_api_call_with_function_call_response(self, mock_openai):
        """Test that API response with function_call is correctly parsed."""
        # Mock the API response
        mock_fc = MagicMock()
        mock_fc.type = "function_call"
        mock_fc.name = "bash"
        mock_fc.arguments = '{"command": "grep -r deprecated /app"}'

        mock_usage = MagicMock()
        mock_usage.input_tokens = 1000
        mock_usage.output_tokens = 50

        mock_response = MagicMock()
        mock_response.output = [mock_fc]
        mock_response.usage = mock_usage

        mock_client = MagicMock()
        mock_client.responses.create.return_value = mock_response
        mock_openai.OpenAI.return_value = mock_client

        mt = _make_microtask()
        result = replay_microtask(mt)

        assert result["microtask_id"] == "test-task__abc__step5"
        assert len(result["new_actions"]) == 1
        assert "bash" in result["new_actions"][0]
        assert result["input_tokens"] == 1000
        assert result["output_tokens"] == 50

    @patch("replay_microtask.openai")
    def test_api_call_with_text_response(self, mock_openai):
        """Test that API response with text message is correctly parsed."""
        mock_content = MagicMock()
        mock_content.text = "I'll search for the issue"
        mock_content.type = "text"

        mock_msg = MagicMock()
        mock_msg.type = "message"
        mock_msg.content = [mock_content]

        mock_usage = MagicMock()
        mock_usage.input_tokens = 500
        mock_usage.output_tokens = 20

        mock_response = MagicMock()
        mock_response.output = [mock_msg]
        mock_response.usage = mock_usage

        mock_client = MagicMock()
        mock_client.responses.create.return_value = mock_response
        mock_openai.OpenAI.return_value = mock_client

        mt = _make_microtask()
        result = replay_microtask(mt)

        assert "search for the issue" in result["response_text"]
        assert result["new_actions"][0].startswith("Said:")
