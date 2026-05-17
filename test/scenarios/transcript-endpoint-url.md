# transcript-endpoint-url: api_call entries record the HTTP endpoint URL

**What this covers**: kata `v5pm` (commit `fdaf981`) + follow-up kata
`dyph` (commit `583f593`). Before v5pm, api_call transcript entries
recorded `provider` and `model` but no HTTP URL — making it impossible
to confirm from the transcript alone whether an OpenAI call went to
`https://chatgpt.com/backend-api/codex/responses` (OAuth path) vs
`https://api.openai.com/v1/responses` (API-key path). v5pm added
`Raw["endpoint_url"]` stamping in adapters + `EndpointURL` promotion
in `buildLogResponse`. dyph fixed the parallel inline construction in
`agent/session.go` that was bypassing the promotion.

## Pre-state

- Hub running with `--serf` set.
- OAuth or API-key creds set up so a model call can actually succeed.
- A cheap, fast-responding model available (e.g.
  `anthropic/claude-haiku-4-5-20251001`).

## Steps

1. Spawn a session via `/api/spawn` (or the `/new` form). Use a
   prompt that completes in one turn — e.g. `Reply with the literal
   text OK and stop.`
   ```bash
   curl -s -X POST -H "Content-Type: application/json" \
        -H "Authorization: Bearer $(cat ~/.serf/auth-token)" \
        -d '{"prompt":"Reply with the literal text OK and stop.","model":"anthropic/claude-haiku-4-5-20251001","working_dir":"/tmp","harness":"serf","branch":"","access_mode":"full","agent":"default","launch_overrides":{}}' \
        http://localhost:9180/api/spawn
   ```
2. Capture the `session_id` from the response.
3. Wait ~10s for the turn to complete.
4. Find the transcript:
   `find ~/.local/state/serf/projects -name "<session_id>.transcript.jsonl"`.
5. For each `api_call` line, inspect `response.endpoint_url` AND
   `response.raw.endpoint_url`:
   ```bash
   python3 -c "
   import json, sys
   for line in open('<path>'):
       d = json.loads(line)
       if d.get('kind') == 'api_call':
           resp = d.get('response') or {}
           print('top:', resp.get('endpoint_url'), 'raw:', (resp.get('raw') or {}).get('endpoint_url'))
   "
   ```

## Expected

- Each api_call has BOTH `response.endpoint_url` (typed top-level
  field, from `dyph`) AND `response.raw.endpoint_url` (the original
  v5pm stamping inside the Raw map) populated with the same URL.
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
- Falsification: any api_call has `response.endpoint_url == null` or
  empty string. Either v5pm or dyph regressed.

## Cleanup

- Sessions accumulate on disk; not strictly necessary to remove.
  If you want hermetic re-runs: `rm -rf ~/.local/state/serf/projects/<projectHash>/sessions/<session_id>*`.

## Sharp edges

- The `endpoint_url` is captured AFTER the call succeeds. Failed
  turns may have it OR may not, depending on which adapter path
  bailed and whether the synthetic error-response was built with
  `stampEndpointURL`. Worth verifying with a known-failing model.
- The google adapter logs only host + path, NOT the full URL with
  query params, because Google's API key lives in the query string.
  That's a deliberate redaction — don't add a kata about "incomplete
  URL" without remembering this.
- If you see only `response.raw.endpoint_url` but not the top-level
  field, dyph regressed — the session.go inline construction is
  bypassing the promotion again.
