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


def test_directory_deduplicates_api_jsonl_and_transcript_api_call(tmp_path):
    call = {
        "session_id": "sess-dupe",
        "round": 7,
        "ts": "2026-05-24T12:00:00Z",
        "latency_ms": 500,
        "request": {"provider": "openai", "model": "gpt-5.2"},
        "response": {
            "usage": {"input_tokens": 100, "output_tokens": 20},
            "finish_reason": "stop",
            "text_length": 8,
            "tool_call_count": 0,
        },
    }
    write_jsonl(tmp_path / "api.jsonl", [call])
    transcript_call = dict(call)
    transcript_call["kind"] = "api_call"
    write_jsonl(
        tmp_path / "sessions" / "sess-dupe.transcript.jsonl",
        [
            {"kind": "header", "session_id": "sess-dupe", "model": "gpt-5.2"},
            transcript_call,
        ],
    )

    result = run_analyzer(tmp_path, "--summary")

    assert result.returncode == 0, result.stderr
    assert "sess-dupe" in result.stdout
    row = next(line for line in result.stdout.splitlines() if line.startswith("sess-dupe"))
    assert row.split()[2] == "1"


def test_summary_hit_percent_includes_cache_write_tokens(tmp_path):
    api_log = tmp_path / "api.jsonl"
    write_jsonl(
        api_log,
        [
            {
                "session_id": "sess-cache-write",
                "round": 1,
                "request": {"model": "gpt-5.2"},
                "response": {
                    "usage": {
                        "input_tokens": 100,
                        "cache_read_tokens": 300,
                        "cache_write_tokens": 100,
                        "cache_write_1h_tokens": 100,
                        "output_tokens": 20,
                    },
                    "finish_reason": "stop",
                    "text_length": 3,
                    "tool_call_count": 0,
                },
            }
        ],
    )

    result = run_analyzer(tmp_path, "--summary")

    assert result.returncode == 0, result.stderr
    assert "sess-cache-wri" in result.stdout
    assert "50.0%" in result.stdout


def test_non_object_jsonl_rows_warn_and_skip(tmp_path):
    api_log = tmp_path / "api.jsonl"
    api_log.write_text(
        "\n".join(
            [
                json.dumps(42),
                json.dumps([]),
                json.dumps(
                    {
                        "session_id": "sess-valid",
                        "round": 1,
                        "request": {"model": "gpt-5.2"},
                        "response": {
                            "usage": {"input_tokens": 10, "output_tokens": 1},
                            "finish_reason": "stop",
                            "text_length": 1,
                            "tool_call_count": 0,
                        },
                    }
                ),
            ]
        )
        + "\n"
    )

    result = run_analyzer(tmp_path)

    assert result.returncode == 0, result.stderr
    assert "sess-valid" in result.stdout
    assert "malformed JSON row, skipping" in result.stderr
