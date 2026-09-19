# Task A3d — publish ./docContent, inject readDocFile's fetch

Status: done, committed, not pushed.
SHA: e086afcd2 (branch claude/sdk-a3d-doccontent-subpath, off 2a8163eb0)

## The two REDs
1. Runner cross-check: added the `./docContent` entry to `packageExports` before
   the `exports` entry. `make test-api-package` failed with
   `AssertionError: qualification manifest names ./docContent, which package.json
   does not export`. Adding the exports entry turned it green.
2. `docContent.test.ts` rewritten to pass a recording fake fetch: the new first
   case asserts the URL handed to the injected fetch and that the global fetch is
   never called. RED was 10 failing cases, the readDocFile ones erroring
   `TypeError: Failed to parse URL from /doc/file?...` — proof the old body still
   called the real global fetch. Green after `readDocFile` took `fetchDoc`.

## Shape
- `readDocFile(session, path, fetchDoc: DocFetch)` — required third argument, no
  default. `DocFetch = (url: string) => Promise<DocResponseLike>`;
  `DocResponseLike` is the minimal `{ok, status, headers.get, arrayBuffer}` a
  real `Response` satisfies structurally, so the package stays DOM-free (this is
  also why the runner's `tsc` with no DOM lib accepts the new .d.ts).
- Adapter: `cmd/evener-hub/frontend/src/panes/doc/browserDocFetch.ts` (8 lines) —
  `fetch(url, { credentials: "same-origin" })`, byte-for-byte today's call.
  `DocPane.tsx:49` passes it. It is the only `readDocFile` caller.
- Root keeps the pure helpers (`DOC_FILE_MAX_BYTES`, `DocFileError`,
  `docFileRawURL`, `docImageURL`, `DocFileContent`, `DocFileErrorKind`) exactly as
  before; only the stale comment about readDocFile changed. A4 can rewrite the
  four helper-only import lines to the root and the two `readDocFile`/namespace
  lines to `@evener/appwire-client/docContent`.
- Subpath smoke is non-networking (both URL builders, the cap, `typeof
  readDocFile`). Injected-fetch assertions inside the runner stay C24's.

## Tests changed
- `protocol/docContent.test.ts`: the readDocFile cases now build a recording /
  canned `DocFetch` instead of `vi.spyOn(globalThis, "fetch")`; the old
  "same-origin credentials" case became "never touches the global fetch".
- New `panes/doc/browserDocFetch.test.ts`: proves the adapter binds
  `globalThis.fetch` with `credentials: "same-origin"` — the contract deleted
  from docContent.test.ts lands here.
- `DocPane.test.tsx` and `docFile.test.ts` untouched and green.

## Gates
`make test-api-package` PASS · `make test-web` PASS (typecheck, test, lint) ·
`make test-native` PASS (777) · biome clean on the 7 touched web/protocol files.
`make test-web-browser` not run: no guard under `frontend/scripts` renders the
doc pane (grepped DocPane, doc-pane, /doc/file — zero hits).

Lines: 125 insertions / 43 deletions across 9 files (~168, estimate was ~160).

## Concerns
- `DocResponseLike` is a new published type the plan did not name. The
  alternative was `Promise<Response>` in the .d.ts, which would drag DOM types
  into the runner's declaration consumers. C24's native adapter must return this
  shape too (React Native's fetch Response does).
- No app import was rewritten to the package name (A4 owns that); the web keeps
  its relative `../../protocol/docContent` imports.
