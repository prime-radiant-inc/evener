# Bug: Duplicate tool names sent to Anthropic API

**Date:** 2026-03-18
**Severity:** Blocking
**Provider:** anthropic
**Model:** claude-sonnet-4-6

## Summary

Serf sends a tool list with duplicate names to the Anthropic Messages API, causing an `invalid_request_error` on the first turn. The session starts and immediately fails — no work is performed.

## Error

```
anthropic error (status=400): messages.create failed:
{
  "type": "error",
  "error": {
    "type": "invalid_request_error",
    "message": "tools: Tool names must be unique."
  }
}
```

## Reproduction

Triggered via toil's `plan_and_build` workflow, which invokes serf as the `surgeon` node runner:

```bash
export SERF_PROVIDER=anthropic
export SERF_MODEL=claude-sonnet-4-6
export ANTHROPIC_API_KEY=sk-ant-...

serf --provider anthropic --model claude-sonnet-4-6 --verbose \
  --dir /tmp/test-project \
  "Build a hello world Go CLI"
```

The error occurs on the very first API call (turn 1), before any tool use happens.

## Context

Observed in toil deliver workflow runs. The serf stderr NDJSON shows:

```json
{"kind":"SESSION_START","session_id":"01KM220AW9WT47H7MARM87KJZV",...}
{"kind":"ERROR","data":{"error":"anthropic error (status=400): messages.create failed: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"tools: Tool names must be unique.\"},...}"}}
{"kind":"SESSION_END","data":{"reason":"session_closed","state":"CLOSED","turns":1}}
```

Session starts, first messages.create call fails, session closes immediately.

## Likely cause

The tool registry is including the same tool name more than once in the `tools` array sent to Anthropic. Possible sources:
- Built-in tools registered twice (e.g., `communicate` registered both as a default and via a skill/prompt)
- MCP tools or project-level tool definitions conflicting with built-in names
- Agent mode (`--agent worker`) adding worker tools that overlap with base tools

## Workaround

None found yet. Switching to `openai` provider may avoid this if OpenAI's API is more lenient about duplicate tool names, but untested.

## Affected toil runs

- `willow-lagoon-mesa` → child `banyan-valley-echo` (surgeon node)
- `barley-topaz-dune` → child `raven-copper-crane` (surgeon node)
