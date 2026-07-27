# Add Test credentials actions to Serf UIs

## Goal

Add a `Test credentials` action to Web Settings > Credentials and the TUI
credentials/provider surface. The action verifies the effective credentials for
one configured provider instance without spawning or persisting a session. Both
interfaces consume one appwire request/response contract, and the result is
safe to display because it never contains credential material, raw provider
errors, or endpoint secrets.

## Architecture

The backend will run the probe through the same instance-aware provider config
and credential resolution path used by runtime client creation. An explicit
config-path loader will be factored from `cmdutil.LoadProviderConfig` so the
hub can pass its configured `providers.toml` path without changing process
environment. The loader will still colocate and load the matching credentials
store, inject instance keys and supported type-environment fallbacks, resolve
OAuth and configured credential headers through the normal provider factories,
and preserve custom base URLs.

The smallest harmless authenticated operation is `ListModels` for an instance.
The probe will first identify missing effective credentials where the provider
requires them, then call `ListModels` with a bounded context. It will map the
outcome to exactly one of `success`, `missing`, `auth_rejected`,
`endpoint_failure`, or `unsupported`, with fixed human-readable messages. The
backend will classify provider status and capability failures without returning
the original error text. Requests for the same instance will share one
in-flight operation, so duplicate concurrent UI actions do not create duplicate
provider calls.

The appwire response will contain only the selected instance name, a status,
and a safe message. Web state will keep testing/result state locally per
instance. TUI state will do the same in the credentials panel and render a
result beside the selected row. Both clients will suppress a second action for
an instance while its first request is pending. No session, transcript, or
credential-store mutation is part of this action.

Unsupported verification is a first-class result when an adapter does not
implement `ModelLister`; it is not reported as a generic network failure.

## Tech Stack

- Go provider/runtime plumbing, hub appwire handlers, and deterministic Go tests
- Generated appwire protocol documentation and TypeScript types
- React/TypeScript web credentials store and settings components
- Bubble Tea TUI credentials panel and appwire command routing
- Fake HTTP/provider boundaries only; default tests remain offline

## Global Constraints

- Work only in the current `kata-zr5r-test-credentials` worktree and leave the
  kata open; never merge into another worktree or branch.
- Read and follow `AGENTS.md`, `docs/testing.md`, and the worker prompt.
- Keep changes minimal and preserve existing credential, OAuth, instance, and
  session contracts.
- Never echo, log, serialize, snapshot, or render API keys, OAuth tokens,
  credential-header values, authorization values, endpoint credentials, or raw
  provider error text.
- Default tests must be deterministic and offline. Credentials or ambient
  environment variables must never make a default test issue a live request.
- Use strict RED -> GREEN -> refactor for every behavior layer; tests must
  exercise real controller/UI behavior below fake HTTP/provider boundaries.
- Regenerate appwire outputs with `go generate ./appwire/...`; do not hand-edit
  generated files.
- Do not add compatibility behavior, broad refactors, or unrelated fixes.
- Run formatting and repository hooks normally; never bypass a hook.

## Implementation Tasks

### Task 1: Add the shared contract and backend probe

**Files:** `appwire/types.go`, `appwire/protocol.go`, `cmdutil/load_client.go`,
`cmd/serf-hub/app_auth.go` or a focused credential-probe file,
`cmd/serf-hub/app_rpc.go`, and their focused tests.

**Tests first:** Add RED coverage for explicit config-path loading, stored
instance credentials, supported type-environment fallback, OAuth/header/base-URL
resolution, missing credentials, successful fake-provider verification, 401/403
auth rejection, unreachable endpoint, unsupported model listing, safe messages,
and same-instance duplicate suppression. Use a real configured controller and
provider adapter below a fake HTTP/provider boundary; assert that requests carry
the resolved credential/header values only inside the fake boundary and that no
result or captured log contains them.

**Implementation:**

1. Factor `LoadProviderConfigAt`/`LoadClientAt` (or equivalent focused helpers)
   from the current loader while keeping existing callers behaviorally
   identical. The explicit path must use the same colocated credential store
   and provider factory resolution as runtime spawning.
2. Add typed appwire params/response data and the method catalog entry. Keep
   wire fields limited to instance, status, and safe message; use stable status
   values for the five taxonomy outcomes.
3. Add the hub controller operation and RPC registration. Resolve the selected
   configured instance, perform the bounded `ListModels` probe, classify only
   safe error properties, close the client, and share one in-flight result per
   normalized instance name. Treat unsupported `ModelLister` explicitly.
4. Regenerate `docs/appwire-protocol.md` and
   `cmd/serf-hub/frontend/src/protocol/types.gen.ts`.

**Commit:** `feat(credentials): add shared credential verification RPC`

### Task 2: Add the web Credentials action

**Files:**
`cmd/serf-hub/frontend/src/stores/credentials.ts`,
`cmd/serf-hub/frontend/src/panes/settings/credentials/CredentialsSection.tsx`,
`cmd/serf-hub/frontend/src/panes/settings/credentials/InstanceRow.tsx`, and
their tests.

**Tests first:** Add RED tests against the real settings components and
`FakeClient` for a custom named instance. Verify the per-instance action sends
the exact selected name, renders local in-progress state, suppresses duplicate
clicks while a deferred request is pending, renders safe success and each safe
failure class, and never renders a supplied secret or raw error string.

**Implementation:** Add the typed store method, a per-instance pending/result
map in the settings section, and an action in each instance row. Keep action
state local to the row/section so one instance’s test does not disable another
instance’s controls. Display only the response status/message.

**Commit:** `feat(web): add per-instance credential verification action`

### Task 3: Add the TUI Credentials action

**Files:**
`cmd/serf-tui/internal/launchconfig/credentials_panel.go`,
`cmd/serf-tui/internal/launchconfig/launchconfig_client.go`,
`cmd/serf-tui/hub_update_config.go`, `cmd/serf-tui/hub_update.go`, and focused
TUI tests.

**Tests first:** Add RED tests for the credentials panel action key and exact
instance, local in-progress rendering, same-instance duplicate suppression,
success/failure rendering, unsupported rendering, and redaction. Add a command
boundary test proving the TUI invokes the shared appwire method and routes the
typed response back to the panel.

**Implementation:** Add a `test` credentials action, typed client command,
message routing, and panel-local pending/result state. Preserve all existing
set-key, OAuth, logout, default, edit, and remove behavior. Keep transport
errors represented by the same safe fixed result message rather than showing a
raw provider error.

**Commit:** `feat(tui): add credential verification action`

### Task 4: Cross-layer verification and hardening

**Scope:** Review the complete branch against the kata and this plan; make only
small fixes required by evidence. Add no new feature surface.

**Verification:** Run focused Go, appwire, web, and TUI tests; frontend lint and
typecheck from `cmd/serf-hub/frontend`; `go test ./...`; `make build-runtime`
when the repository environment permits it; `go generate ./appwire/...` and
generated-file checks; `git diff --check`; and a secret-redaction scan over
the implementation and tests. Run the final independent whole-branch review
with the required `gpt-5.6-luna`/`xhigh` nested-agent settings and fix every
Critical or Important finding through a fresh implementer/review loop.

**Commit:** Any evidence-backed fixes use focused commits with detailed
messages; otherwise leave the task commits intact.

## Completion Contract

Before reporting completion, leave the worktree on this branch with no
uncommitted changes, do not merge, append the implementation/review/verification
summary to kata `zr5r` without closing it, and write the ignored final report to
`.superpowers/sdd/zr5r/implementation-report.md`. The final response must state
`DONE`, `DONE_WITH_CONCERNS`, or `BLOCKED`, list the plan and commits, give a
one-line verification summary, state the final review verdict, link the report
path, and name any honest limitations.
