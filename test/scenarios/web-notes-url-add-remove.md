# web-notes-url-add-remove: agent-added links render in the Overview Links tab, wire removal sticks

**What this covers**: the session URL list end to end on the web surface.
The agent's `urls_add` tool (`agent/internal/tool/definitions.go:DefUrlsAdd`,
agent tool name `urls_add` — tool names cannot contain slashes, so the wire
RPC `urls/add` and the tool differ in spelling) appends entries the Overview
sidebar's Links tab renders (read-only — the web surface has no removal
control); the human's `urls/remove`
(`appwire/types_thread.go#UrlsRemoveParams`, daemon `agent/session_notes_mutations.go:RemoveSessionURL`)
removes one by id over the wire; and the `evener/urls/updated` push keeps
every client converged (step 5 pins the web client's answer).

The Go layer covers the store and the gate with unit tests
(`agent/session_notes_test.go:TestAddSessionURLDedupsCanonically`,
`TestAddSessionURLRejectsOverCapAndBadScheme`,
`agent/session_tools_notes_test.go:98` `TestUrlsAddRemoveTool`,
`cmd/evener-hub/app_rpc_test.go:1525`
`TestHubRPCUrlsRemoveGatedByCapability`); this is the live-stack counterpart
that proves the add→render→remove→converge loop actually works. It exercises
four things in one run:

- **Agent-side add reaches the browser.** `urls_add` stores
  `{id, url, label, addedBy, addedAt}` (identity pinned by
  `TestAddSessionURLDedupKeepsIdentity` — a re-add keeps the first id) and
  emits `evener/urls/updated`; the Overview sidebar's Links tab renders one
  `about-link-<id>` row per entry (`SessionLinksSection.tsx:190`, `:215`).
  The session header's Notes panel renders none of them (step 3 pins that).
- **Wire-side remove is id-addressed and push-authoritative.**
  `urls/remove` takes `{ref, clientMutationId, expectedInstanceId, id}` and
  its response carries no state at all (`UrlsRemoveResponse` is empty by wire
  design — the `evener/urls/updated` push is the authority, `threads.ts:677`).
  The web client commits no local list edit; the row disappears when the push
  lands.
- **Canonical dedup keeps the list honest.** Re-adding the same URL (modulo
  fragment, case, default port — `TestCanonicalSessionURLNormalizesHTTP`)
  keeps one entry and updates its label; `docs/x.md` and `./docs/x.md`
  collide (`TestAddSessionURLDedupsCanonically`,
  `agent/session_notes_test.go:39`).
- **Removal converges everywhere.** The push folds through the reducer and
  the store (the `evener/urls/updated` case in `reducer.ts:503` replaces
  `sessionUrls` wholesale, and `threads.ts:7095` keeps a urls push from
  retiring a notes response commit), so a second client watching the same
  session sees the same list without reloading.

**Surface**: see `docs/developing-evener/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. There is
no `window` handle to add or remove URLs through (`threadsStore` is a module
import with nothing on `window`), so adding goes through the agent's real
tool and removal goes through `/rpc` only: the web Links tab is read-only by
design and renders no Remove control, and mobile keeps the only human remove
surface.

## Pre-state

- A freshly built hub on an isolated `$HOME` and a kernel-assigned port — see
  the Setup checklist in `docs/developing-evener/agentic-testing.md`. Token at
  `$HOME/.evener/auth-token` (that isolated one).
- **This model-choice variant requires an explicitly chosen live provider
  and its credential.** First verify an ordinary tool round on that provider;
  setup failure is not URL behavior. The standalone `test/e2e/fakellm/cmd`
  used by `scripts/e2e/e2e-webui-turn-controls.sh` emits fixed `read_file`
  calls (`main.go:392`), not `urls_add` selected by prompting. Follow the
  isolated-hub setup guide above with your live provider/model. A deterministic
  variant must instead script `urls_add` at the external LLM boundary and
  drive the real session loop; do not claim the standalone fake does this.
- A real SPA bundle (`make build-web` — a checkout that has never run it serves a
  one-line `frontend/dist/PLACEHOLDER` and no app).
- A hermetic `$WORK` as the session's `working_dir` (the script's
  `$workspace` serves if using a scripted stack; `mktemp -d` otherwise).

## Steps

Spawn through the AppWire-backed UI: visit `$HUB/auth/$TOKEN`, navigate to
`/new`, use working directory `$WORK`, your chosen live provider/model, and a
prompt that makes the agent record two links, e.g. `Call urls_add twice:
once with url "https://example.com/notes-spec" and label "spec", once with
url "https://example.com/notes-mock" and label "mock". Then stop.` Await actual
successful `urls_add` tool results, not just a model claim. Read the session
ref off the resulting `/s/local:<SID>` path, and wait for `idle` (poll `thread/read`'s
`result.thread.status.type` — a running turn is `active`, never
`processing`, which is not a wire value at all).

1. **[browser-free] Confirm the adds landed — the exact store proof.** Dial
   `ws://127.0.0.1:$PORT/rpc` with `Authorization: Bearer $TOKEN`,
   `initialize` (protocolVersion `evener-appwire-v2` — see "Driving AppWire
   directly" in `docs/developing-evener/agentic-testing.md`; frames carry no
   `jsonrpc` field), then call:
   ```json
   {"id":3,"method":"thread/read","params":{"ref":"local:<SID>","includeTurns":true}}
   ```
   **Expected:** `result.thread.evener.sessionUrls` holds two entries with
   distinct non-empty `id`s, `url`s `https://example.com/notes-spec` and
   `https://example.com/notes-mock`, labels `spec` / `mock`, and
   `capabilities.sharedNotes` `true`. Record both ids (call them `U_SPEC`
   and `U_MOCK` below). The `addedBy`/`addedAt` fields ride along
   (`TestAddSessionURLDedupKeepsIdentity` pins their shape); assert presence,
   not values. Falsify: the agent's tool calls errored (read the tool rows
   in `includeTurns` — a `gopher://` scheme, an out-of-scope file path, or a
   51st URL is refused by validation, `TestAddSessionURLRejectsOverCapAndBadScheme`).

2. **[browser] Read the Links tab.** Authenticate and open the session:
   ```text
   navigate $HUB/auth/<TOKEN>?next=/s/local:<SID>
   await_element [data-testid="composer-input-card"]
   ```
   (Use the literal token, not the path. Note the ref form — a bare `/s/<SID>`
   renders "Page not found" by design.) Open the Overview sidebar with the
   session header's Overview button (`[aria-label="Overview"]`,
   `SessionInspectionActions.tsx:79`), switch to its Links tab (the tab
   button whose id ends `-tab-links`), and read the links list:
   ```javascript
   (() => {
     const sidebar = document.querySelector('[data-testid="activity-sidebar"]');
     const rows = sidebar ? [...sidebar.querySelectorAll("li[data-link-url]")] : [];
     return {
       port: location.port,                        // page-identity check, always
       path: decodeURIComponent(location.pathname), // /s/local:<SID>; the literal colon breaks naive === compare
       sidebar: !!sidebar,
       rows: rows.map((li) => ({
         testid: li.querySelector("[data-testid]")?.getAttribute("data-testid") ?? null,
         text: li.textContent,
         href: li.querySelector("a")?.getAttribute("href") ?? null,
         linkUrl: li.getAttribute("data-link-url"),
         button: li.querySelector("button") ? "button-present" : null,
       })),
     };
   })()
   ```
   **Expected:** two rows; each row's `testid` is `about-link-<id>` with the
   `<id>` half byte-identical to the wire id from step 1 (the row's
   `data-link-url` repeats the entry URL); each `http(s)` URL renders as an
   anchor (`href` exactly the URL, `target="_blank"`,
   `rel="noopener noreferrer"`, `SessionLinksSection.tsx:190`) with the label
   as its visible text. No row carries a button — this card uses `https:`
   URLs, whose rows are bare anchors (`button: null` on every row); the
   in-app open-beside button exists only for in-scope `file://` entries
   (`:204`). An empty session would show the compact
   "No saved links" empty state instead (`:159`).

3. **[browser] The Notes panel carries no links.** From the same session
   page, open **Notes** from the session chrome button/menu or `/notes`
   desktop pane and read the Shared notes section:
   ```javascript
   (() => {
     const section = document.querySelector('[data-testid="shared-notes-section"]');
     return {
       section: !!section,
       linkList: !!section?.querySelector('[data-testid="shared-notes-urls"]'),
       linkRows: section
         ? [...section.querySelectorAll("[data-testid^='shared-notes-url-']")].length
         : -1,
     };
   })()
   ```
   **Expected:** `linkList` false and `linkRows` 0 — the notes body renders
   note text only; session links live in the Overview sidebar's Links tab
   (step 2), never here.

4. **[browser-free] Remove one link over the wire.** On the `/rpc` socket,
   call:
   ```json
   {"id":4,"method":"urls/remove","params":{"ref":"local:<SID>",
    "clientMutationId":"<uuid>","expectedInstanceId":"<SID>","id":"<U_MOCK>"}}
   ```
   Param shape is `appwire.UrlsRemoveParams` (`appwire/types_thread.go#UrlsRemoveParams`).
   Then re-issue the step-1 `thread/read` and diff the `sessionUrls` list.
   **Expected:** no error; the response is `{}` (empty by design — it carries
   no state, so assert nothing about the list from the response itself); the
   fresh read carries only `U_SPEC`.

5. **[browser] Confirm the removal converged without reload.** Still on the
   session page from step 2 (no refresh), re-read the step-2 query.
   **Expected:** the `U_MOCK` row is gone from the Links tab; the `U_SPEC`
   row remains; no toast appears in `section[aria-label="Notifications"]`.
   The client commits no local edit — the row disappears when
   `evener/urls/updated` lands, so a row that vanishes *before* the push
   would mean an optimistic edit someone added against the wire design.

6. **[browser-free] Remove the second link and prove the refusal.** Call
   `urls/remove` again with `id":"<U_SPEC>"`; a fresh `thread/read` now shows
   `[]` (both links gone). A third `urls/remove` re-sending the `U_MOCK` id
   fails `InvalidParams` (unknown id maps to InvalidParams,
   `agent/session_notes_mutations.go` RemoveSessionURL via
   `TestRemoveSessionURLDaemonPath`) — that refusal is the store telling you
   the first removal stuck, not a new failure.

7. **[browser-free, history check] Assert the URL context shape.** Read the
   agent context block the session actually consumed:
   ```bash
   go run ./cmd/evener doctor transcript "$SID" --format outline --range last:80
   ```
   (or find the `NOTES_CONTEXT` turns in the transcript JSONL and read one
   `user` message body). **Expected:** while both links were set, the block
   rendered each as a `Link:` line — `Link: spec
   (https://example.com/notes-spec)` (`agent/session_notes_context.go` formatNotesLinkLine,
   pinned by `TestNotesContextBlockContainsNotesAndURLs`) — and after the
   removals the next block (if any turn runs after them) no longer carries
   the removed URL. Human URL removals reach the agent even though the wire
   pushes travel hub-ward, because the block is re-rendered from the store
   before the next round (`maybeAppendNotesContext` runs beside the goal
   continuation-prompt rendering at turn start and on resume, and pre-round
   on note events). Falsify: a post-removal block still listing `U_MOCK`
   (the context re-render read a stale snapshot). Growth bound (Task 5 M3
   carried watch item, shared with the companion card): at most one
   `NOTES_CONTEXT` turn per model round while notes/URLs are set, none while
   the store is empty.

## Expected

- **Step 1 (store, exact)**: two URL entries with distinct ids, exact
  urls + labels, `sharedNotes:true`. Falsify: entries missing while the
  agent claims success (the tool emission never reached the store), or
  `sharedNotes:false` on a live evener session (the capability bit
  regressed).
- **Step 2 (render, exact-in-browser)**: two `about-link-<id>` rows in the
  Overview Links tab, anchors with exact hrefs, no button on any row.
  Falsify: rows keyed by anything but the wire id; `http(s)` links rendered
  as inert text (the `isWebHref` branch regressed).
- **Step 3 (notes panel, exact-in-browser)**: the Shared notes section
  renders note text with no link list and no link rows.
- **Step 4 (wire remove, exact)**: `{}` response, fresh read shows only
  `U_SPEC`. Falsify: the response carrying a list (the wire type changed —
  `UrlsRemoveResponse` must stay empty), or the read still listing the id
  (the store never removed it).
- **Step 5 (converge, qualitative)**: no-reload convergence in the Links
  tab, no toast. Falsify: row gone with no push on the socket (an optimistic
  edit against the design); row persisting after the push (the reducer/store
  fold dropped it).
- **Step 6 (refusal, exact)**: fresh read `[]`, re-remove refused as
  `InvalidParams`.
- **Step 7 (context, exact)**: `Link: <label> (<url>)` lines while set,
  absent after removal; growth within the per-round bound.

## Cleanup

```bash
scripts/e2e/e2e-webui-turn-controls.sh --stop "$run"   # kills fakellm + hub, removes the run dir
```

When the stack was hand-started instead of scripted: `thread/shutdown` the
session over `/rpc` (the old `/s/$SID/shutdown` form-POST shim is gone — it
404s silently and leaves the daemon up), then kill the hub by the PID you
captured and `rm -rf` your own run dir plus `$WORK`. Remove `$run` and
`$tmpdir` by name, never a `/tmp/evener-e2e-*` glob — a wildcard cleanup
deletes every other concurrent scenario's workdir too. Leave Jesse's real
`~/.evener` and `~/.local/state/evener` untouched; the isolated `$HOME`
above is what keeps this run's sessions out of them.

## Sharp edges

- **The list has a 50-entry cap, canonical dedup, and scheme/scope
  validation.** A 51st add is refused; re-adding a URL (modulo fragment,
  case, default port) keeps one entry and only updates the label
  (`TestAddSessionURLDedupsCanonically`); `gopher://` is refused and bare
  paths are scoped to the session cwd (`file:///tmp/proj/…` —
  `TestCanonicalSessionURLFileScope`, so `../escape.md` and
  `file:///etc/passwd` are refused). Prompts that add "a few links" can
  silently collide into one entry — this card uses two URLs that differ past
  the host so the count assertion means something.
- **Only the agent adds; removal is wire-only on the web.** `urls_add`
  is an agent tool (not in the AppWire catalog:
  `appwire/protocol_notes_test.go:58` asserts the agent verbs stay out), and
  `urls/remove` is a hub RPC (no agent tool removes links). The web Links
  tab is read-only by design and renders no Remove control; a card that
  looks for one finds none. Do not read either refusal as a notes bug.
- **The remove response is `{}` — there is nothing to assert in it.**
  `UrlsRemoveResponse` carries no state by wire design; the push is the
  authority. A test that reads the removed id out of the response is testing
  a field that must not exist.
- **`urls/remove` takes a bare id with no picker.** The id is discoverable
  over `/rpc` (`thread/read` lists the ids) and in the DOM (the row's
  `about-link-<id>` testid); the rendered label text alone does not show it.
- **Entries keep their first id across re-adds.** A re-add updates the label
  but keeps `id`/`addedBy`/`addedAt` (`TestAddSessionURLDedupKeepsIdentity`),
  so an id recorded in step 1 stays valid through any label churn. Asserting
  a *new* id after a re-add measures the dedup, not the path.
- **Frames carry no `jsonrpc` field.** `Message.UnmarshalJSON` rejects the
  frame outright (`appwire/jsonrpc.go:164-166`) and the socket drops — which
  reads as a network or auth problem, not a malformed request. Send bare
  `{id, method, params}` after `initialize`.
- **`expectedInstanceId` is the session id, not the ref.** It is the `local:`
  prefix stripped off (`e2e_control_without_turn_ids_test.go:19-25`
  `localInstanceIDForTestRef`); sending the full ref trips the fencing check
  and reads as a gate regression.
- **The `active` / `idle` vocabulary is canonical.** A running turn is
  `active`, never `processing` — `processing` is not a wire value at all, and
  `test/scenarios/scenario_docs_test.go` fails the build on any card that
  writes it. Poll `thread/read`'s `result.thread.status.type`.
- **Over-length URLs/labels are refused, not clamped** (unlike notes, which
  clamp to 1000 runes). A >2048-char URL or >280-char label fails the add
  (`TestAddSessionURLRejectsOverLength`) — the step-1 prompt uses short
  values so a refusal there means something else broke.
- **The transcript is virtualized** (`VirtualList`, `panes/session/Session.tsx`),
  so a global row count measures the viewport. Scope every query to the
  Links tab's list (`li[data-link-url]` inside the activity sidebar) or the
  Shared notes section, and use the doctor — not DOM counts — for the
  step-7 history shape.
- **The pure add/remove logic is already unit-tested** —
  `TestUrlsAddRemoveTool` (`agent/session_tools_notes_test.go:98`) pins the
  add→event→remove→empty loop including the id round-trip. If those pass and
  this card fails, the break is in the wiring (projection, push fold, or
  render), not the store.
