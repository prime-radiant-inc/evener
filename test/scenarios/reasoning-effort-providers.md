# reasoning-effort-providers: reasoning effort works end-to-end on Kimi and Anthropic

**What this covers**: the kimi-effort branch — `llm.ClampReasoningEffort`
(commit 2a6cb30a), the Anthropic forced-`tool_choice`-under-thinking downgrade
(e13b2b3d), and the `max_tokens` > thinking-budget reconciliation (158774ac).
The symptom that started it: `Kimi error (status=400): Unsupported value:
'reasoning_effort' does not support 'xhigh'`. If reasoning effort regresses on
an Anthropic-family provider, this catches it.

This is an end-to-end test against **real** provider APIs. It needs live
credentials and makes billed calls.

## Pre-state

- Kimi coding-plan key at `~/.serf/credentials.toml` under `[providers.kimi]`.
- An Anthropic API key in the repo-root `.env` as `ANTHROPIC_API_KEY` (or
  exported in the environment).
- A `serf` binary built from this branch: `go build -o /tmp/serf-eff ./cmd/serf`.
- An isolated provider config so the live `~/.serf/providers.toml` is untouched.
  Write `/tmp/eff-cfg/providers.toml`:

  ```toml
  default = "anthropic"
  [instances.anthropic]
  type = "anthropic"
  [instances.kimi]
  type = "kimi-anthropic"
  ```

  and `/tmp/eff-cfg/credentials.toml` with `[providers.anthropic] api_key` (from
  `.env`) and `[providers.kimi] api_key` (copied from `~/.serf/credentials.toml`).
  Run serf with `SERF_PROVIDERS_CONFIG=/tmp/eff-cfg/providers.toml`.

A one-shot invocation looks like:

```
SERF_PROVIDERS_CONFIG=/tmp/eff-cfg/providers.toml /tmp/serf-eff \
  --model <provider>/<model> --reasoning-effort <level> \
  --max-rounds 1 --no-project-prompts "reply with the single word OK"
```

## Steps

1. **Reproduce the original enum bug (raw, no serf).** POST to the Kimi
   OpenAI-style route with an out-of-range effort:

   ```
   curl -s -o /dev/null -w "%{http_code}" https://api.kimi.com/coding/v1/chat/completions \
     -H "Authorization: Bearer $KIMI_KEY" -H "Content-Type: application/json" \
     -d '{"model":"kimi-for-coding","max_tokens":16,"reasoning_effort":"xhigh",
          "messages":[{"role":"user","content":"OK"}]}'
   ```

2. **Clamp on the OpenAI route via serf.** Run serf against the `kimi` (OpenAI)
   instance with `--reasoning-effort xhigh`. (This route is User-Agent gated for
   non-coding-agents, so a successful completion is not expected — what matters
   is the *kind* of failure.)

3. **Kimi via the sanctioned anthropic-compatible route.** Run serf against the
   `kimi` (type `kimi-anthropic`) instance with `--reasoning-effort high`.

4. **Real Anthropic, both thinking modes.** Run serf with
   `--reasoning-effort high` against `anthropic/claude-opus-4-6` (adaptive
   thinking) and `anthropic/claude-opus-4-5-20251101` (legacy budget thinking).

5. **Control: no thinking.** Run the kimi-anthropic instance with
   `--reasoning-effort none`.

## Expected

- **Step 1**: `HTTP 400`, body message `Unsupported value: 'reasoning_effort'
  does not support 'xhigh' with this model. Supported values are: 'minimal',
  'low', 'medium', and 'high'.` This is the original bug; it proves the route
  validates the enum.
- **Step 2**: NOT a 400 about `reasoning_effort`/`xhigh`. The clamp turns
  `xhigh` into `high` before sending, so the only remaining failure is the
  unrelated `status=403 ... only available for Coding Agents` User-Agent gate.
  - Falsification: if you see `status=400 ... does not support 'xhigh'`, the
    clamp regressed.
- **Step 3**: exit 0, model reply `OK`.
  - Falsification: `status=400 ... tool_choice 'required' is incompatible with
    thinking enabled` → the forced-tool_choice downgrade regressed.
- **Step 4**: both models exit 0, reply `OK`.
  - Falsification: opus-4-6 → `Thinking may not be enabled when tool_choice
    forces tool use` means the downgrade regressed; opus-4-5 → `max_tokens must
    be greater than thinking.budget_tokens` means the max_tokens reconciliation
    regressed.
- **Step 5**: exit 0, reply `OK` (no thinking, forced tool_choice preserved).

## Cleanup

- `rm -rf /tmp/eff-cfg /tmp/serf-eff` and any one-shot session dirs under
  `~/.local/state/serf/projects/*/sessions/` created by the run.

## Sharp edges

- The Kimi anthropic-compatible endpoint (`https://api.kimi.com/coding`) is
  ungated; the OpenAI-style `/coding/v1` endpoint is User-Agent gated — serf
  cannot complete on it, only validate the request shape. Deploy Kimi as
  `type = "kimi-anthropic"`.
- The enum-validation 400 (step 1) fires *before* the UA-gate 403, so an `xhigh`
  request on the OpenAI route returns 400 while a valid effort returns 403. Don't
  read the 403 in step 2 as a failure — it confirms the param was accepted.
- Provider enforcement of "thinking + forced tool_choice" can be intermittent;
  the request serf builds is what matters. Re-run if a single call flakes.
- `--max-rounds 1` forces the result tool, which is the path that exercises the
  forced-`tool_choice` + thinking interaction. A multi-round natural finish
  exercises the same first-call shape.
