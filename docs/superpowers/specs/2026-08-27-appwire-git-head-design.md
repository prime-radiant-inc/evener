# Git HEAD AppWire Migration

## Goal

Replace the Spawn pane's `/api/git/head` request with a typed, hub-scoped
AppWire method. The user-visible behavior remains the same while the HTTP
route and its route-specific tests and wiring are removed.

## Contract

- Method: `evener/git/head`
- Scope: `hub`
- Parameters: `{ "cwd": string }`
- Response: `{ "head": string }`
- The response contains a branch name for a normal checkout, a short commit
  SHA for detached HEAD, or an empty string when the directory cannot be
  inspected or git cannot resolve its HEAD.
- The request is fail-soft: invalid, missing, non-Git, and git-error cases
  return a successful response with an empty `head`.
- Whitespace-only `cwd` skips filesystem and git work and returns an empty
  response. Non-empty paths pass through `fspaths.CanonicalizeDir`, which
  requires an existing absolute directory and resolves symlinks before the
  `resolveGitHead` helper runs.
- `WebConfig.ResolveGitHead` is the injectable seam. A nil seam uses the real
  `resolveGitHead` implementation, including detached HEAD short-SHA and
  `HEAD` fallback behavior.

## Caller and authentication

`Spawn.tsx` continues to resolve the branch only for the display chip. It keeps
the existing empty-cwd guard and active-request cleanup so a response for an
old cwd cannot update the current form. The frontend calls the generated
AppWire client method and does not send the resolved value when starting a
thread.

The method is registered on the hub AppWire server. Browser clients reach it
through the existing authenticated `/rpc` endpoint, so removing the separate
HTTP route does not create an unauthenticated path.

## Removal boundary

Remove the `/api/git/head` route, its HTTP handler, its route-specific coverage
and sandbox probes, and the REST-only frontend branch caller/commentary. Keep
the shared `resolveGitHead` helper, its injectable seam, meaningful git
semantics coverage, and historical documentation that records past transport
behavior. Do not add a test whose purpose is proving that the legacy route is
absent.

## Generated artifacts and verification

Adding the method updates the AppWire catalog, Go types/client, and generated
protocol documentation and TypeScript types through `make generate`. The
implementation will run focused Go and frontend tests, the relevant browser
gate, and the repository gates required by `AGENTS.md` before publication.
