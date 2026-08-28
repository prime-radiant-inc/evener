# Session rename over AppWire

## Context

The navigation rail still renames sessions through
`POST /api/sessions/{ref}/rename`, even though the typed
`evener/thread/name/set` AppWire method and frontend thread-store action already
exist. The REST handler contains the authoritative hub behavior for local live
and ended sessions, while the current hub AppWire handler always routes through
a live source and may resume an ended session.

This change migrates the used rename flow and retires only the superseded REST
surface. It does not change the AppWire request or response types, session
deletion, or any other session mutation.

## Contract

Keep `appwire.ThreadNameSetParams` and the existing empty response unchanged.
The hub must validate the ref, trim the requested name, reject an empty
normalized name, and pass the canonical ref and normalized name to live
sources. AppWire errors remain visible to the caller; the web UI toasts the
error and leaves the rename dialog open.

## Server behavior

The hub handler will preserve the REST implementation's distinction between
live and ended local sessions while retaining the deletion fence:

1. A live local session or a non-local session is renamed through its AppWire
   source. The hub then refreshes the persisted metadata projection.
2. An ended local session is renamed by editing its existing session metadata.
   The edit sets `Name`, `NameSource = "user"`, and `NameUpdatedAt`, preserves
   the remaining metadata, and updates the past index without resuming a
   daemon.
3. Before an ended-session metadata write, the hub rechecks liveness. If the
   session became live, the rename is routed through the source; a source
   failure is returned and the metadata is not edited.
4. An unknown local session is rejected as unavailable. Load and save failures
   are returned as internal errors.

After either successful path, the handler performs the same attention poke and
navigation refresh used by the REST handler. The AppWire response stays empty;
navigation mutation receipts continue through the navigation publication
stream, including precise project targets for indexed local sessions and the
existing all-loaded-projects fallback. Repeating an unchanged rename produces
no new navigation publication.

## Frontend flow

The rail and session chrome will call `threadsStore.rename(ref, name)`, which
already sends `evener/thread/name/set` with the ref and name unchanged. Their
existing optimistic title, toast, rejection propagation, and dialog behavior
remain in place. The navigation-mode branch used only to select the REST rename
path will be removed; there is no REST fallback.

## Removal and verification

Remove the REST route dispatch, handler file, route-only frontend helper, and
tests or fuzz cases that exist only to exercise HTTP method, JSON decoding, URL
escaping, or REST response parsing. Do not add a test asserting that the old
route is absent.

Migrate the meaningful coverage to the AppWire boundary:

- the rail sends the exact ref and name through `evener/thread/name/set`;
- session chrome preserves successful and rejected rename behavior;
- live and ended hub renames preserve metadata semantics and navigation
  publications, including repeat no-ops;
- an ended session that becomes live does not receive a fallback metadata edit
  after a source failure;
- server validation covers invalid refs and empty normalized names.

Run Biome on touched frontend files, the focused frontend and Go tests,
`make test-web`, `make test-web-browser` on a Chrome-capable host, and the
proportional Go lint, vet, and test gates before opening the PR.
