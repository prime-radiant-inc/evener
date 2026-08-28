# AppWire Session Delete Design

## Goal

Replace `POST /api/sessions/{ref}/delete` with one hub-scoped typed AppWire
method while preserving the existing single-session deletion behavior. Project
deletion remains on its current path.

## Wire contract

Add `evener/session/delete` to the AppWire catalog with hub scope.

Request:

```json
{"ref":"local:02wMz5Txv1C3Hut0M8GCeB"}
```

Response:

```json
{
  "deleted": ["02wMz5Txv1C3Hut0M8GCeB"],
  "skipped": [],
  "navigation": {"generation_id":"...", "targets":[]}
}
```

`deleted` and `skipped` remain arrays because a live or concurrently reserved
target is a completed destructive decision, not a transport failure. Such a
target appears in `skipped` with the existing reason and never appears in
`deleted`. A missing already-deleted target is an idempotent success with both
arrays empty after stale session decisions are scrubbed.

The typed error contract is:

- `invalidParams` for an empty, malformed, or non-local ref, including an
  invalid local session ID.
- `internal` when the Past index is unavailable, decision cleanup fails, or
  the Past rebuild cannot complete.
- `actionUnavailable` when the committed navigation receipt cannot be built.

An artifact-removal failure preserves the current completed-outcome semantics:
it returns the target in `skipped` with the cleanup reason instead of raising a
wire error.

No REST fallback or compatibility path is added.

## Server flow

Keep the destructive implementation on `WebServer` so the AppWire handler uses
the same configured stores, per-session locks, roster, navigation service, and
test seams as project deletion. Convert the current HTTP-oriented session
delete method into a context-taking typed operation and register a thin typed
handler for `evener/session/delete`.

The operation continues to:

1. Parse and validate a local qualified ref before deriving a session ID.
2. Resolve the state directory only through the trusted Past index.
3. Acquire the existing deletion ownership lock and API-log reservation.
4. Re-check liveness under ownership, treating confirmed crash markers as
   deletable and live or concurrently reserved targets as skipped.
5. Reuse `cleanupProjectDeletionTargetAndDecisions` for artifact, rendezvous,
   archive, favorite, and pin-assignment cleanup.
6. After a successful artifact removal, rebuild Past, refresh the roster, bump
   navigation inputs, notify attention, and return the navigation mutation
   produced by `NavigationService.Refresh`.

The `/api/sessions/{ref}/delete` dispatch branch, HTTP handler file, and tests
whose only subject is HTTP routing or status translation are removed. Shared
deletion machinery stays in place for project deletion.

## Frontend flow

Change `deleteSession` to require the connected typed AppWire client and call
`evener/session/delete`. Pass the rail's existing client directly. Session
chrome obtains the client through the existing `useClient()` context before
invoking the action. Both consumers keep the current navigation convergence,
pane-closing, warning-toast, and error-toast behavior based on the typed
response.

Project deletion stays REST-backed in this change.

## Tests and generated artifacts

- Move meaningful session deletion behavior tests from the HTTP route to the
  typed AppWire operation/handler: validation, target-only cleanup, live and
  reserved skips, crashed-session cleanup, idempotency, decision cleanup,
  roster freshness, and navigation receipts.
- Replace the frontend fetch-shape test with a typed-client request/response
  test and retain the skipped-result and error propagation assertions.
- Do not add a test whose only purpose is proving the removed REST route is
  absent.
- Run AppWire generation for the protocol reference and TypeScript types, then
  run focused Go/frontend tests, formatting, `make test-web`, `make vet`, and
  the canonical merge approval gate. Run the browser gate because production
  rail behavior changes, even though geometry is not expected to change.
