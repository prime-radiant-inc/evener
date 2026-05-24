import json
import subprocess
import sys
from pathlib import Path


SCRIPT = Path(__file__).with_name("api-log-analyze.py")


def write_jsonl(path, rows):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(json.dumps(row) for row in rows) + "\n")


def run_analyzer(*args):
    return subprocess.run(
        [sys.executable, str(SCRIPT), *map(str, args)],
        text=True,
        capture_output=True,
        check=False,
    )


def test_summary_reads_transcript_api_calls_and_cache_totals(tmp_path):
    transcript = tmp_path / "sessions" / "sess-123.transcript.jsonl"
    write_jsonl(
        transcript,
        [
            {
                "kind": "header",
                "session_id": "sess-123",
                "profile_id": "default",
                "model": "gpt-5.2",
            },
            {"kind": "entry", "seq": 1, "turn": {"kind": "USER_INPUT"}},
            {
                "kind": "api_call",
                "seq": 2,
                "round": 3,
                "latency_ms": 250,
                "request": {},
                "response": {
                    "usage": {
                        "input_tokens": 100,
                        "cache_read_tokens": 300,
                        "output_tokens": 20,
                    },
                    "finish_reason": "stop",
                    "text_length": 5,
                    "tool_call_count": 0,
                },
            },
        ],
    )

    result = run_analyzer(tmp_path, "--summary")

    assert result.returncode == 0, result.stderr
    assert "sess-123" in result.stdout
    assert "gpt-5.2" in result.stdout
    assert "CacheRead" in result.stdout
    assert "300" in result.stdout
    assert "75.0%" in result.stdout
    assert "USER_INPUT" not in result.stdout


def test_cache_spikes_flags_uncached_api_jsonl_call(tmp_path):
    api_log = tmp_path / "api.jsonl"
    write_jsonl(
        api_log,
        [
            {
                "session_id": "sess-api",
                "round": 4,
                "latency_ms": 99,
                "request": {"model": "gpt-5.2"},
                "response": {
                    "usage": {
                        "input_tokens": 9000,
                        "cache_read_tokens": 0,
                        "output_tokens": 10,
                    },
                    "finish_reason": "stop",
                    "text_length": 2,
                    "tool_call_count": 0,
                },
            }
        ],
    )

    result = run_analyzer(tmp_path, "--cache-spikes", "--spike-threshold", "8000")

    assert result.returncode == 0, result.stderr
    assert "UNCACHED_SPIKE" in result.stdout
    assert "sess-api" in result.stdout
    assert "r  4" in result.stdout
    assert "gpt-5.2" in result.stdout
    assert "input=9000" in result.stdout
    assert "cache=0" in result.stdout
    assert str(api_log) in result.stdout


def test_transcript_non_api_rows_are_ignored(tmp_path):
    transcript = tmp_path / "only.transcript.jsonl"
    write_jsonl(
        transcript,
        [
            {"kind": "header", "session_id": "sess-ignored", "model": "gpt-5.2"},
            {"kind": "entry", "seq": 1, "turn": {"kind": "USER_INPUT"}},
        ],
    )

    result = run_analyzer(tmp_path)

    assert result.returncode == 1
    assert "No entries found." in result.stderr
    assert result.stdout == ""
