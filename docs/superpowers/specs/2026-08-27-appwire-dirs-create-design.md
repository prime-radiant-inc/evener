# AppWire Directory Creation Microproject

## Goal

Move Spawn's "Create & start" working-directory action from the authenticated
hub REST route `POST /api/dirs/create` to the authenticated hub AppWire
connection, then remove the retired REST route and only its route-specific
coverage.

## Findings and scope

The existing `evener/paths/complete` method lists directory suggestions; it
does not create directories. There is no current `evener/dirs/complete`
method to reuse. The replacement is therefore a new, narrow hub-only method:
`evener/dirs/create`.

This slice owns only the Spawn preflight caller and the directory-creation
operation. It does not change path validation, path completion, thread start,
or any other REST route.

## Wire contract

Add `evener/dirs/create` to the AppWire catalog with these typed shapes:

```go
type DirsCreateParams struct {
	Path string `json:"path"`
}

type DirsCreateResponse struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
}
```

The method is `ScopeHub`. It is served by the hub, not forwarded to a daemon,
because the hub owns the Spawn working-directory prompt and has the same
filesystem authority as the existing route.

The hub preserves the current operation exactly:

1. Trim surrounding whitespace.
2. Expand `~` and `~/...` using the configured home directory.
3. Require an absolute path.
4. Clean the path with `filepath.Clean`.
5. If `os.Stat` finds a directory, return the cleaned path with
   `created:false`.
6. If `os.Stat` finds a file, return an AppWire conflict with the existing
   message `a file already exists at that path`.
7. For every other stat error, call the configured `MkdirAll` seam or
   `os.MkdirAll` with mode `0755`, and return the cleaned path with
   `created:true` on success.
8. Return an AppWire internal error carrying the existing `MkdirAll` error
   message when creation fails.

Validation errors use `appwire.InvalidParams`: `path is required` and
`absolute path required`. A malformed typed request is rejected by the shared
AppWire typed-router decoder. The file conflict uses `appwire.Conflict`; a
creation failure uses `appwire.InternalError`.

The browser's `/rpc` endpoint is already behind the hub authentication guard,
so the new call retains the same authenticated, same-origin authority as the
REST request without adding a second auth mechanism.

## Server changes

- Add the method constant, typed params/result, catalog entry, Go client
  wrapper, and regenerated TypeScript/docs catalog output.
- Move the directory operation into a focused hub helper used by the typed
  AppWire handler; retain `WebConfig.MkdirAll` as the deterministic test seam.
- Register the method with `registerMiscHandlers`.
- Remove `handleAPIDirCreate` and the `/api/dirs/create` mux registration.
- Remove only HTTP-route probes, the obsolete HTTP behavior test, and the
  sandbox/fuzz route entries. Keep directory behavior coverage through the
  AppWire server/client path and focused helper seams.

## Frontend changes

- Change `createDir` to accept the existing `AppwireClientLike` and request
  `evener/dirs/create` with `{path}`.
- Pass the shell's client from `Spawn` through the confirmation flow.
- Preserve the current promise rejection and user-visible
  `friendlyLaunchErrorMessage` handling.
- Replace fetch/REST assertions in Spawn preflight tests with typed FakeClient
  request assertions. Do not retain tests whose purpose is proving that the
  old REST route is absent.

## Non-goals

- No REST compatibility fallback or dual request path.
- No protocol-version bump; this is an additive hub method consumed by the
  matching hub/frontend build.
- No changes to directory completion, path validation, spawn payloads, or
  filesystem authority.
- No edits to historical design/parity records solely to rewrite their past
  references.

## Verification

- Watch new Go and frontend behavior tests fail before production changes,
  then make them pass with the smallest implementation.
- Run `make generate` and verify generated output is clean.
- Run focused AppWire/hub and Spawn tests, then `make lint`, `make vet`,
  `make test`, `make test-web`, and `make test-web-browser` where the host
  supports the browser gate.
- Audit current source, tests, and active docs for `/api/dirs/create` before
  publishing the PR; historical records may retain their archival references.
