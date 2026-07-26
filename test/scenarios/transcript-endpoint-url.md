# transcript-endpoint-url: API attempts record the sanitized HTTP endpoint

**What this covers**: the canonical API log identifies the resolved endpoint
used for each provider transport attempt while excluding credential-bearing URL
components. Semantic transcripts may retain compact assistant provenance, but
provider forensics come from `sessions/<SID>.api.jsonl` or explicit
`read_session_transcript(source=api_log)` access.

## Pre-state

- Hub running with `--serf` set, on an isolated `$HOME` and a free port
  (never Jesse's port `9180` — see the Setup checklist in
  `docs/agentic-testing.md`).
- OAuth or API-key creds set up so a model call can actually succeed.
- A cheap, fast-responding model available (e.g.
  `anthropic/claude-haiku-4-5-20251001`).

## Steps

1. Spawn a session via `/api/spawn` (or the `/new` form). Use a
   prompt that completes in one turn — e.g. `Reply with the literal
   text OK and stop.`
   ```bash
   curl -s -X POST -H "Content-Type: application/json" \
        -H "Authorization: Bearer $(cat "$HOME/.serf/auth-token")" \
        -d '{"prompt":"Reply with the literal text OK and stop.","model":"anthropic/claude-haiku-4-5-20251001","working_dir":"/tmp","harness":"serf","branch":"","access_mode":"full","agent":"default","launch_overrides":{}}' \
        $HUB/api/spawn
   ```
2. Capture the `session_id` from the response.
3. Wait ~10s for the turn to complete.
4. Resolve the session with Serf's own state model and inspect its API attempts:
   ```bash
   APIFILE=$(serf-doctor locate <session_id> --json | jq -r '.api_log_path')
   jq 'select(.kind == "api_attempt") | {attempt_id, attempt_group_id, attempt_index, endpoint: .request.endpoint, outcome}' "$APIFILE"
   ```
5. Confirm the underlying `sessions/<session_id>.api.jsonl` contains one
   `api_attempt` per transport attempt followed by the outer call's
   `attempt_group_settlement`. Exact bodies are not needed for this scenario;
   if inspecting one, use an explicit `read_session_transcript` attempt/body
   expansion rather than treating transcript JSONL as provider data.

## Expected

- Each `api_attempt` has a non-empty `endpoint` naming the transport URL used.
- For anthropic: `https://api.anthropic.com/v1/messages`.
- For OpenAI via OAuth (ChatGPT path): the URL begins with
  `https://chatgpt.com/backend-api/codex/responses` (or whatever
  `defaultChatGPTBaseURL + defaultCodexResponses` is in the adapter).
- For OpenAI via API key: `https://api.openai.com/v1/responses`.
- For OpenAI fallback path (Responses → Chat Completions empty-
  stream sentinel): `https://<base>/v1/chat/completions`.
- For google: `https://generativelanguage.googleapis.com/...` —
  WITHOUT the `?key=...` query param (deliberately stripped to avoid
  leaking credentials).
- Falsification: any attempt has an empty endpoint, or the URL persists a
  credential-bearing query/userinfo value.

## Cleanup

- Sessions accumulate on disk; not strictly necessary to remove.
  If you want hermetic re-runs: `rm -rf "$HOME/.local/state/serf/projects/<project-id>/sessions/<session_id>"*`
  — that isolated `$HOME`, never Jesse's real one.

## Sharp edges

- Endpoint identity belongs to the attempt, including failed attempts that
  reached a resolved transport boundary.
- The google adapter logs only host + path, NOT the full URL with
  query params, because Google's API key lives in the query string.
  That's a deliberate redaction — don't add a kata about "incomplete
  URL" without remembering this.
- Group finality comes from `attempt_group_settlement`; do not infer it merely
  because an attempt is the last record in a bounded page.
