# spawn-failure-ux-post-ws5: re-verify spawn-failure surfaces are legible, not raw 500s

**What this covers**: Investigate T2 from the 2026-07-05 consistency-sweep
Track D plan. WS5 previously fixed the main MCP-fatal cause of
buried-stderr HTTP 500s on spawn. This card re-checks, live, the three
remaining named failure classes: a bogus model id, a working dir that
doesn't exist, and a harness binary the hub can't execute — against the
real `POST /api/spawn` handler (`handleApiSpawn`,
`cmd/serf-hub/web_spawn.go:63-102` → `hubThreadStart` in
`cmd/serf-hub/app_threadlifecycle.go` → `HubSpawner.Spawn` in
`cmd/serf-hub/spawn.go`).

**Surface**: this card is **fully browser-free** — every assertion is a
`curl` against `/api/spawn` and a read of the JSON body. It needs no
`data-testid`, no Chrome, and no frontend build. The runbook section it
depends on is `docs/agentic-testing.md`'s "Setup checklist" (isolated
`$HOME`, kernel-assigned port) and "The REST surface, and what is no
longer on it".

## Pre-state

- Fresh binaries (`make build-hub && make build`).
- Isolated fake `$HOME` per attempt (real `~/.serf` untouched), and a
  kernel-assigned port — the hub binds `127.0.0.1:0` and logs the port
  it actually got, so nothing here collides with a concurrent run:
  `env -i HOME="$FAKE_HOME" PATH="$PATH" ./serf-hub -addr 127.0.0.1:0
  -serf <serf-binary-path> 2>"$FAKE_HOME/hub.log"`. Read `$PORT` back
  out of that log per the Setup checklist.
- `TOKEN=$(cat "$FAKE_HOME/.serf/auth-token")` (auto-generated on first
  launch, no pre-existing file needed).

## Steps + Observed (all via real `curl -w "HTTP_STATUS:%{http_code}"` against `/api/spawn`)

1. **(a) Bogus provider entirely** — `model:
   "totallyfakeprovider/nonexistent-model-xyz"`:
   ```
   HTTP 503
   {"error":"model provider is not reported by the Serf launch harness: totallyfakeprovider","code":-32014,"serf_error_info":"hubLaunch"}
   ```
   Rejected in `validateSerfLaunchModel` (`cmd/serf-hub/app_models.go:104-125`,
   message at `:122`) before any subprocess is spawned. Legible, named,
   structured JSON.

2. **(a2) Real provider, bogus model** — `model:
   "ollama/does-not-exist-blah-9999"` (ollama IS enumerated by the
   harness, this specific model is not):
   ```
   HTTP 503
   {"error":"model is not configured for Serf launch: ollama/does-not-exist-blah-9999","code":-32014,"serf_error_info":"hubLaunch"}
   ```
   Same gate, message at `cmd/serf-hub/app_models.go:124`. Legible.

3. **(b) Working dir that doesn't exist** — `working_dir:
   "/does/not/exist/proj-nonexistent-xyz"`:
   ```
   HTTP 400
   {"error":"cwd: resolve: lstat /does: no such file or directory","code":-32602,"serf_error_info":"invalidParams"}
   ```
   Caught in `hubThreadStart`'s `hubCanonicalizeDir` call — the
   `fspaths.CanonicalizeDir` seam at
   `cmd/serf-hub/app_threadlifecycle.go:23`, invoked at `:71-77` — before
   any subprocess spawn is attempted. Legible: the raw OS `lstat` error
   is preserved but wrapped in a structured, addressed message
   (`"cwd: ..."`), not dumped as a stack trace.

4. **(c) Harness binary the hub can't execute** — a second hub instance
   launched with `-serf /nonexistent/path/to/serf-binary-xyz`, spawn
   attempted against it:
   ```
   HTTP 503
   {"error":"serf launch-check failed: fork/exec /nonexistent/path/to/serf-binary-xyz: no such file or directory","code":-32014,"serf_error_info":"hubLaunch"}
   ```
   Caught in `HubSpawner.Spawn` → `validateSerfLaunchContract`
   (`cmd/serf-hub/spawn.go:712-735`, message at `:733`), which execs
   `<serf-binary> launch-check --protocol <v> --json` before ever
   attempting the real daemon spawn. The Go `exec` error (`fork/exec
   ...: no such file or directory`) surfaces verbatim inside the
   structured wire error — legible and precise about the actual cause.

## Expected / Verdict

**All three classes: legible, structured, actionable — no raw 500, no
silent hang.** Every failure is caught pre-spawn (model validation, cwd
canonicalization, or the `launch-check` subprocess probe) and returned
as a structured JSON body (`hubapi.ErrorResponse`, `hubapi/types.go:268-272`:
`{"error": ..., "code": ..., "serf_error_info": ...}`) with an HTTP
status appropriate to the failure kind — 400 for caller input errors via
`appwire.CodeInvalidParams` (`-32602`), 503 for launch/environment-side
failures via `appwire.CodeUnavailable` (`-32014`), both in
`appwire/errors.go:7,10`. The mapping is `writeSpawnError`
(`cmd/serf-hub/web_spawn.go:104-121`) → `writeAPIWireError`
(`cmd/serf-hub/web_api.go:89-100`), with the `serf_error_info` string
lifted off the wire error's data by `serfErrorInfoFromData`
(`web_api.go:127-137`; the values are `appwire.ErrorInvalidParams` /
`ErrorHubLaunch`, `appwire/errors.go:16,22`). WS5's fix holds: no
regression to buried-stderr raw 500s was found for any of the three
named classes. **No code change made — this card is the
re-verification record.**

Falsification (what would have failed this card): a bare `HTTP 500`
with an empty or non-JSON body, or a request that never returns
(`curl` hanging past the launch-check timeout with no eventual
response). Neither was observed.

## Cleanup

- Both test hub processes killed by the PIDs you captured (never a
  `pkill -f serf-hub` pattern, which would take out any concurrent
  agent's test hub too); both fake `$HOME` tmpdirs removed (`rm -r`).
- No sessions/daemons were actually spawned in this card — every
  attempt was rejected before `SpawnDaemon` ran, so there was nothing
  to shut down.

## Sharp edges

- **The ollama credential-gate bug this card originally surfaced is
  fixed.** A hub with an auto-materialized `providers.toml` used to
  reject a credential-free `ollama` spawn, because
  `validateProviderCredentials`'s config-aware branch had no escape
  hatch matching the no-config branch's `credentials.SourceNone` case.
  Commit `1b717fe72` added one: the branch now returns nil for any
  instance whose *behavior tag* declares auth mode `none`
  (`cmd/serf-hub/spawn.go:567-573`, `envvars.RequiresNoCredential`,
  `envvars/providers.go:65-68`; ollama's `AuthModes: []string{"none"}`
  at `envvars/providers.go:155-160`), and the rule is pinned by
  `TestValidateProviderCredentials_ConfigInstanceAuthModeNone`
  (`cmd/serf-hub/spawn_test.go:1243`), which also checks that the
  bypass keys off the type and not the instance *name*. The old
  workaround for isolating test (c) — `mv providers.toml
  providers.toml.bak` after the fake hub's first launch, or seeding a
  placeholder `api_key` — is no longer needed. If a credential-free
  ollama spawn is refused again with `"provider credentials missing for
  ollama: ..."`, that is a fresh regression of `1b717fe72`, not this
  card's known ground.
- `validateSerfLaunchModel` **fails open** when the harness binary
  can't even be executed to enumerate models
  (`cmd/serf-hub/app_models.go:105-107`, `//nolint:nilerr // fail
  open`) — so a missing-binary hub does NOT reject at the model-check
  stage; the real, correct rejection happens one step later inside
  `HubSpawner.Spawn`'s own `validateSerfLaunchContract` call. Both
  stages ultimately converge on a legible error either way, but if you
  are tracing *which* validation caught a given failure, don't assume
  the model check is authoritative when the binary itself is broken.
- Test (c) requires a **second** hub instance (its own kernel-assigned
  port, its own fake `$HOME`) since `-serf` is fixed at hub startup,
  not overridable per spawn request.
- Two failure classes never reach any of the three gates above, so
  don't confuse them with a regression here: an oversized or
  over-counted image payload is rejected by `validateAppWireInputItems`
  with a plain-text **413** before `hubThreadStart` is called at all
  (`web_spawn.go:74-77`, limits in
  `cmd/serf-hub/internal/hubcore/types.go:12-14`), and a malformed JSON
  body is a plain-text **400** from the decoder (`web_spawn.go:70-73`).
  Both are `http.Error`, not the structured envelope.
