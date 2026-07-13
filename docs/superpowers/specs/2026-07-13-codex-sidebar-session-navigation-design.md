# Codex Sidebar Session Navigation Design

**Date:** 2026-07-13
**Status:** Approved

## Problem

The Web UI lists external Codex app-server threads in the sidebar, but a click can open a local-session URL and return not found.

The tree API supplies two identities:

- `ref`: the globally qualified AppWire identity, such as `codex-local:thread-id`
- `session_id`: the source's bare session identifier

The sidebar currently builds every primary session URL from `session_id`. The server interprets a bare route ID as a local Serf session. It therefore looks for a local session instead of dispatching the request to the Codex source.

The backend already supports source-qualified route IDs and can read and drive Codex threads through AppWire. The defect lies in client-side route construction.

## Goals

- Open external Codex threads from the sidebar.
- Preserve current canonical URLs for local Serf sessions.
- Use one route-identity rule for row links, HTMX partial requests, pushed URLs, menu actions, active-row matching, and reveal logic.
- Add deterministic regression coverage.

## Non-goals

- Change the Codex AppWire adapter or launcher.
- Search all sources when a bare session ID is unknown.
- Add Codex-specific routing branches.
- Change session capabilities or controls.

## Design

### Canonical route identity

Add one sidebar helper that derives a route ID from a tree node:

- For a valid local ref, return the bare `session_id` to preserve `/s/<session-id>`.
- For a valid non-local ref, return the full qualified `ref`, such as `codex-local:thread-id`.
- For missing or malformed refs, retain the existing bare `session_id` behavior. The server remains responsible for returning not found.

The helper must not infer a source from the title, project, or other presentation fields.

### Navigation data flow

Use the canonical route ID in every primary-navigation path:

1. Sidebar row `href`
2. Sidebar row `hx-get`
3. Sidebar row `hx-push-url`
4. Row-menu **Open** action
5. Active-row URL matching
6. Hidden-row reveal lookup for direct links

For an external Codex node, the row requests:

```text
/_partials/s/codex-local:thread-id/workspace
```

The existing session partial handler preserves the non-local ref. Workspace loading then selects the registered or managed Codex AppWire source, reads the thread, and renders controls according to its advertised capabilities.

For a local node, navigation remains:

```text
/s/session-id
```

### Error handling

Do not add a backend fallback that searches all sources by bare ID. Different sources can contain the same thread ID, so such a lookup would be ambiguous.

A missing, malformed, or unknown identity retains the current not-found response. This fix changes only which established identity the sidebar sends.

## Testing

Add a deterministic jsdom test around the real sidebar `buildRow` behavior. The test must fail before the implementation change and assert:

- A local node keeps `/s/<session-id>` and its matching workspace partial URL.
- A Codex node uses `/s/<source>:<thread-id>` and its matching workspace partial URL.
- The row-menu **Open** action uses the same canonical route.
- Active-row matching and reveal logic recognize the qualified Codex route.

Run the existing Go tests that exercise source-qualified Codex workspace rendering and controls, then run the full `cmd/serf-hub` package tests. No test may require credentials, network access, a live Codex binary, or wall-clock races.

## E2E scenario cards

| Card | Covers | Falsification |
|---|---|---|
| codex-sidebar-open | Clicking a Codex session in the sidebar opens its source-qualified workspace and displays that thread. | The click requests a bare local session URL, shows not found, or opens a different source's thread. |
| codex-sidebar-drive | An opened Codex workspace exposes and routes the controls advertised by the Codex AppWire source. | Sending an available action targets a local Serf session, returns not found because the source was lost, or reaches a different thread. |
| local-sidebar-url-stability | Local Serf sessions retain their existing canonical `/s/<session-id>` URLs. | A local sidebar row changes to a qualified URL or no longer opens its existing workspace. |
