# spawn-failure-ux-post-ws5: re-verify spawn-failure surfaces are legible, not raw 500s

**What this covers**: Investigate T2 from the 2026-07-05 consistency-sweep
Track D plan. WS5 previously fixed the main MCP-fatal cause of
buried-stderr HTTP 500s on spawn. This card re-checks, live, the three
remaining named failure classes: a bogus model id, a working dir that
doesn't exist, and a harness binary the hub can't execute — against the
real `POST /api/spawn` handler (`cmd/serf-hub/web_spawn.go:87` →
`hubThreadStart` in `cmd/serf-hub/app_threadlifecycle.go` →
`HubSpawner.Spawn` in `cmd/serf-hub/spawn.go`).

## Pre-state

- Fresh binaries (`make build-hub && make build`).
- Isolated fake `$HOME` per attempt (real `~/.serf` untouched):
  `env -i HOME="$FAKE_HOME" PATH="$PATH" ./serf-hub -addr
  127.0.0.1:<port> -serf <serf-binary-path>`.
- `TOKEN=$(cat "$FAKE_HOME/.serf/auth-token")` (auto-generated on first
  launch, no pre-existing file needed).

## Steps + Observed (all via real `curl -w "HTTP_STATUS:%{http_code}"` against `/api/spawn`)

1. **(a) Bogus provider entirely** — `model:
   "totallyfakeprovider/nonexistent-model-xyz"`:
   ```
   HTTP 503
   {"error":"model provider is not reported by the Serf launch harness: totallyfakeprovider","code":-32014,"serf_error_info":"hubLaunch"}
   ```
   Rejected in `validateSerfLaunchModel` (`cmd/serf-hub/app_models.go:104-125`)
   before any subprocess is spawned. Legible, named, structured JSON.

2. **(a2) Real provider, bogus model** — `model:
   "ollama/does-not-exist-blah-9999"` (ollama IS enumerated by the
   harness, this specific model is not):
   ```
   HTTP 503
   {"error":"model is not configured for Serf launch: ollama/does-not-exist-blah-9999","code":-32014,"serf_error_info":"hubLaunch"}
   ```
   Same gate, same shape. Legible.

3. **(b) Working dir that doesn't exist** — `working_dir:
   "/does/not/exist/proj-nonexistent-xyz"`:
   ```
   HTTP 400
   {"error":"cwd: resolve: lstat /does: no such file or directory","code":-32602,"serf_error_info":"invalidParams"}
   ```
   Caught in `hubThreadStart`'s `fspaths.CanonicalizeDir` call
   (`cmd/serf-hub/app_threadlifecycle.go:54-61`) before any subprocess
   spawn is attempted. Legible — the raw OS `lstat` error is preserved
   but wrapped in a structured, addressed message (`"cwd: ..."`), not
   dumped as a stack trace.

4. **(c) Harness binary the hub can't execute** — a second hub instance
   launched with `-serf /nonexistent/path/to/serf-binary-xyz`, spawn
   attempted against it:
   ```
   HTTP 503
   {"error":"serf launch-check failed: fork/exec /nonexistent/path/to/serf-binary-xyz: no such file or directory","code":-32014,"serf_error_info":"hubLaunch"}
   ```
   Caught in `HubSpawner.Spawn` → `validateSerfLaunchContract`
   (`cmd/serf-hub/spawn.go:609-642`), which execs `<serf-binary>
   launch-check` before ever attempting the real daemon spawn. The Go
   `exec` error (`fork/exec ...: no such file or directory`) surfaces
   verbatim inside the structured wire error — legible and precise
   about the actual cause.

## Expected / Verdict

**All three classes: legible, structured, actionable — no raw 500, no
silent hang.** Every failure is caught pre-spawn (model validation, cwd
canonicalization, or the `launch-check` subprocess probe) and returned
as a structured JSON body (`{"error": ..., "code": ..., "serf_error_info":
...}`) with an HTTP status appropriate to the failure kind (400 for
caller input errors via `appwire.CodeInvalidParams`, 503 for
launch/environment-side failures via `appwire.CodeUnavailable`) — see
`writeSpawnError` (`cmd/serf-hub/web_spawn.go:143-155`) and
`writeAPIWireError` (`cmd/serf-hub/web_api.go:75-86`). WS5's fix holds:
no regression to buried-stderr raw 500s was found for any of the three
named classes. **No code change made — this card is the re-verification
record.**

Falsification (what would have failed this card): a bare `HTTP 500`
with an empty or non-JSON body, or a request that never returns
(`curl` hanging past the launch-check timeout with no eventual
response). Neither was observed.

## Cleanup

- Both test hub processes killed; both fake `$HOME` tmpdirs removed
  (`rm -r`).
- No sessions/daemons were actually spawned in this card — every
  attempt was rejected before `SpawnDaemon` ran, so there was nothing
  to shut down.

## Sharp edges

- **Unrelated bug found incidentally while setting up (c), NOT fixed
  here — out of this card's scope** (documented in full in the
  companion card `sidebar-project-order-lastactivity-feel.md`'s Sharp
  edges): a hub with an auto-materialized `providers.toml` present
  incorrectly requires a credential for `ollama` even though it needs
  none (`validateProviderCredentials`'s config-aware branch,
  `cmd/serf-hub/spawn.go:461-509`, has no `SourceNone`-equivalent
  escape hatch the way the no-config branch does at
  `spawn.go:519-527`). This produced a *legible* 503
  (`"provider credentials missing for ollama: ..."`), so it doesn't
  itself falsify this card's "raw 500 / hang" question — but it's a
  real correctness bug (a valid, credential-free spawn is wrongly
  rejected) worth a follow-up kata. Workaround used to isolate the
  binary-missing test: `mv providers.toml providers.toml.bak` after
  the fake hub's first launch, which routes credential validation
  through the (correct) no-config path.
- `validateSerfLaunchModel` **fails open** when the harness binary
  can't even be executed to enumerate models
  (`cmd/serf-hub/app_models.go:104-108`, `//nolint:nilerr // fail
  open`) — so a missing-binary hub does NOT reject at the model-check
  stage; the real, correct rejection happens one step later inside
  `HubSpawner.Spawn`'s own `validateSerfLaunchContract` call. Both
  stages ultimately converge on a legible error either way, but if you
  are tracing *which* validation caught a given failure, don't assume
  the model check is authoritative when the binary itself is broken.
- Test (c) requires a **second** hub instance (bound to a different
  port, with its own fake `$HOME`) since `-serf` is fixed at hub
  startup, not overridable per spawn request.
