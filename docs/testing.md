# Testing

## Test Reliability Policy

The default test suite must be deterministic. Running `make test` or
`go test ./...` must not depend on provider credentials, model availability,
network access, quota, current model behavior, wall-clock timing outside the
process, or ambient developer machine state.

Use this boundary when adding or fixing tests:

- If the test verifies Serf plumbing, use a scripted provider at the LLM
  boundary and exercise the real Serf code below it. Examples: CLI flag/config
  wiring, appwire RPC, daemon input queues, session loops, tool execution,
  transcript writes, event emission, goal continuation routing, hook dispatch,
  and prompt composition.
- If the test verifies model behavior, keep it live. Examples: whether a
  specific model chooses a tool from a natural-language instruction, follows an
  output contract, supports a provider feature, honors a live API wire shape, or
  behaves well across multi-turn goal prompts.
- Live tests must be explicitly opt-in with a `SERF_*_E2E=1` or
  `SERF_LIVE_TESTS=1` style environment variable in addition to the provider
  credential. A provider key by itself must never make the default suite issue
  live requests.
- Do not use sleeps, polling races, or large string snapshots to prove behavior
  when a structured event, state field, file result, or fake transport script can
  prove the same contract.
- Do not mock Serf internals to make a test pass. Keep the fake boundary at an
  external dependency: LLM provider, network server, filesystem root, clock, or
  process launcher.

When a test needs a model, name that as the behavior under test and keep it out
of the default suite. When the model is only a way to drive Serf, replace it with
a scripted `llm.ProviderAdapter` response and assert the Serf side effects.

## A Test That Never Runs

A test that does not execute is worse than a missing test: it reports the
coverage without providing it, and the suite stays green either way. Two shapes
in this repo produce one, and neither announces itself.

**Registered `check*` functions.** Several packages drive their behavioral
contracts through a fuzz entry point that replays one check selected by the fuzz
input — `FuzzFSPathsBehaviorProgram` and friends in `cmd/serf-hub/internal/`
(`fspaths`, `hostlock`, `hubedge`, `codexlaunch`, `launchconfig`). A
`check*(t *testing.T)` function in those packages runs **only** if it appears in
its `checks := []func(*testing.T){…}` seed table. Write one, forget the table
entry, and `go test` passes without ever calling it.

The reachability proxy is `golangci-lint run ./path/to/pkg/`, whose `unused`
linter reports the unregistered check as a dead function:

```
paths_test.go:411:6: func checkSanitizeDirPrefix_PreservesLoneTrailingDot is unused (unused)
```

`go vet` does **not** catch this — verified by unregistering a real check and
running both. Run the linter after adding a check, and state which table each
new check is registered in when handing work off.

**A stylesheet assertion that matches its own comment.** A test that greps CSS
text (`expect(css).toContain("flex: none")`) will match the declaration quoted
in a doc comment above the rule. One of these passed with its implementation
deleted. Strip comments before matching:

```ts
const css = readFileSync(…, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
```

The general rule: **prove a new test can fail.** Break the thing it covers,
watch it go red, then put it back. A test you have only ever seen pass has not
been tested. Two corollaries, both from real incidents here:

- "No tests" is not "tests passed". A broken file makes vitest print
  `Tests no tests` next to a transform error; a grep for `Tests ` reads that as
  benign. Check exit codes, never a grep of piped output.
- Assert the mechanism, not a side effect a broken implementation also produces.
  An "onAdd called once" assertion passed with validation entirely removed,
  because committing the add unmounted the panel either way. Asserting the
  validate call itself distinguishes them.

## MCP Server E2E

The MCP manager has opt-in live tests against `npx -y
@modelcontextprotocol/server-everything`. They are intentionally not part of the
default suite because they depend on an ambient Node/npm toolchain and may fetch
or use cached packages outside the repository.

Run:

```sh
SERF_MCP_E2E=1 GOCACHE=/tmp/serf-gocache go test ./agent/internal/mcp -run 'TestRealMCP_' -count=1 -v
```

`SERF_LIVE_TESTS=1` also enables these tests with the other live test suites.

## Environment Variable Tests

Supported runtime environment variables are defined in the `envvars` package
and documented in `docs/environment.md`. Production code, help text, and test
helpers should use those rows instead of hard-coded env names. The default test
suite includes an audit that fails when a supported env var is used as a raw Go
string outside `envvars`.

When adding a runtime env var:

- Add one `envvars.Var` row.
- Use the row's `Name`, `Getenv`, `LookupEnv`, `Trimmed`, or `Assignment`
  helper at call sites.
- Document it in `docs/environment.md`.
- Keep live-test opt-in gates explicit; a provider credential alone must not
  make a default test issue network requests.

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
