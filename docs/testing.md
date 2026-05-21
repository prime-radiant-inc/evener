# Testing

## OpenAI Codex Backend E2E

The OpenAI adapter has opt-in live tests for the ChatGPT/Codex Responses backend.
They are intentionally not part of normal CI because they require stored OpenAI
OAuth credentials and make live requests to `https://chatgpt.com/backend-api/codex/responses`.

Run:

```sh
SERF_OPENAI_CODEX_E2E=1 GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_E2E_Codex' -count=1 -v
```

Optional model override:

```sh
SERF_OPENAI_CODEX_E2E=1 SERF_OPENAI_CODEX_E2E_MODEL=gpt-5.4 GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_E2E_Codex' -count=1 -v
```

Prerequisites:

- `serf openai login` has completed and stored OAuth credentials.
- The active account can use the Codex backend.
- Network access to `chatgpt.com` is available.

The suite currently checks:

- OpenAI env resolution uses the stored OAuth/Codex transport.
- Requests hit `/backend-api/codex/responses`.
- Codex session metadata fields can be sent:
  - `prompt_cache_key`
  - `session-id`
  - `thread-id`
  - `x-client-request-id`
  - `client_metadata` installation ID
- Reasoning requests ask for `reasoning.encrypted_content`.
- Tool-call replay with preserved assistant messages still works.
- Selected public Responses API controls are accepted or explicitly reported as unsupported by the Codex backend.

Observed live result on 2026-05-21:

- The Codex backend accepted the transport/session metadata path.
- The Codex backend accepted explicit `store:false`.
- The Codex backend rejected these public Responses parameters:
  - `safety_identifier`
  - `prompt_cache_retention`
  - `truncation`
  - `max_tool_calls`
  - `background`
- The Codex backend rejected `service_tier:auto` with `Unsupported service_tier: auto`.
- For low-effort `gpt-5.4` prompts tested, responses contained `reasoning.effort` in the raw response but did not include an output `reasoning.encrypted_content` item. The adapter still supports encrypted reasoning round-trip when the backend returns that item, covered by unit tests.

If sandboxed DNS/network blocks the live run, rerun with command escalation for network access.

## Anthropic Messages API E2E

The Anthropic adapter has opt-in live tests for the Anthropic Messages API. They
are intentionally not part of normal CI because they require `ANTHROPIC_API_KEY`
and make live requests to `https://api.anthropic.com/v1/messages`.

Run:

```sh
SERF_ANTHROPIC_E2E=1 GOCACHE=/tmp/serf-gocache go test ./llm/providers/anthropic -run 'TestAdapter_E2E_Anthropic' -count=1 -v
```

Optional model override:

```sh
SERF_ANTHROPIC_E2E=1 SERF_ANTHROPIC_E2E_MODEL=claude-sonnet-4-5-20250929 GOCACHE=/tmp/serf-gocache go test ./llm/providers/anthropic -run 'TestAdapter_E2E_Anthropic' -count=1 -v
```

Prerequisites:

- `ANTHROPIC_API_KEY` is set.
- The active account can use the selected model.
- Network access to `api.anthropic.com` is available.

The suite currently checks:

- Requests hit `/v1/messages`.
- `service_tier: "standard_only"` is serialized and accepted.
- Automatic prompt caching request shape remains enabled through top-level `cache_control`.
- Extended thinking requests work when the selected model emits thinking blocks; returned thinking is replayed into the next request.
- Tool use and tool-result replay work across turns.

Docs-backed behaviors covered by unit tests and this live suite:

- Anthropic documents `service_tier` values `auto` and `standard_only`, and reports the assigned tier in `usage.service_tier`.
- Anthropic documents automatic prompt caching through top-level `cache_control`.
- Anthropic documents `thinking` and `redacted_thinking` blocks, including signatures/data that must be preserved when round-tripping tool-use conversations.

Observed live result on 2026-05-21:

- `service_tier: "standard_only"` was accepted.
- The live transport/service-tier/cache-shape test passed against `api.anthropic.com`.
- The live extended-thinking replay test passed against the default e2e model.
- The live tool-use/tool-result replay test passed against the default e2e model.
- Short prompts may not report cache write/read activity because they do not cross cache thresholds; the test logs this instead of failing.
- Some prompts/models may not emit visible thinking blocks even when reasoning is requested; the test logs this instead of failing. Unit tests cover thinking/signature and redacted-thinking round-trip shapes.

If sandboxed DNS/network blocks the live run, rerun with command escalation for network access.
