# web-notes-url-add-remove: agent-added links render, human removal sticks, bare-id removal is discoverable

**What this covers**: the session URL list end to end on the web surface.
The agent's `urls_add` tool (`agent/internal/tool/definitions.go:DefUrlsAdd`,
agent tool name `urls_add` — tool names cannot contain slashes, so the wire
RPC `urls/add` and the tool differ in spelling) appends entries the Details
panel renders as links; the human's `urls/remove`
(`appwire/types.go:826-837`, daemon `agent/session_notes_rpc.go:RemoveSessionURL`)
removes one by id; the `evener/urls/updated` push keeps every client
converged; and the removed id stays discoverable from the panel (the Task 8
minor-5 watch item — the TUI drawer renders URLs without ids, so a bare-id
command is usable but undiscoverable there; step 5 pins the web panel's
answer).

The Go layer covers the store and the gate with unit tests
(`agent/session_notes_test.go:TestAddSessionURLDedupsCanonically`,
`TestAddSessionURLRejectsOverCapAndBadScheme`,
`agent/session_tools_notes_test.go:98` `TestUrlsAddRemoveTool`,
`cmd/evener-hub/app_rpc_test.go:11390`
`TestHubRPCUrlsRemoveGatedByCapability`); this is the live-stack counterpart
that proves the add→render→remove→converge loop actually works. It exercises
four things in one run:

- **Agent-side add reaches the browser.** `urls_add` stores
  `{id, url, label, addedBy, addedAt}` (identity pinned by
  `TestAddSessionURLDedupKeepsIdentity` — a re-add keeps the first id) and
  emits `evener/urls/updated`; the Details panel's links row renders one
  `<li data-testid="shared-notes-url-<id>">` per entry
  (`DetailsPanel.tsx:254-283`).
- **Human-side remove is id-addressed and push-authoritative.**
  `urls/remove` takes `{ref, clientMutationId, expectedInstanceId, id}` and
  its response carries no state at all (`UrlsRemoveResponse` is empty by wire
  design — the `evener/urls/updated` push is the authority,
  `threads.ts:2655-2658`). The web client commits no local list edit; the
  row disappears when the push lands.
- **Canonical dedup keeps the list honest.** Re-adding the same URL (modulo
  fragment, case, default port — `TestCanonicalSessionURLNormalizesHTTP`)
  keeps one entry and updates its label; `docs/x.md` and `./docs/x.md`
  collide (`TestAddSessionURLDedupsCanonically`,
  `agent/session_notes_test.go:39`).
- **Removal converges everywhere.** The push folds through the reducer and
  the store (`protocol/reducer.ts` urls case, `threads.ts` push routing with
  the per-verb `urlsUpdateGenerations` fence), so a second client watching
  the same session sees the same list without reloading.

**Surface**: see `docs/developing-evener/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. There is
no `window` handle to add or remove URLs through (`threadsStore` is a module
import with nothing on `window`), so adding goes through the agent's real
tool and removal goes through the real UI (the row's Remove button) plus
`/rpc` for the exact assertions.

## Pre-state

- A freshly built hub on an isolated `$HOME` and a kernel-assigned port — see
  the Setup checklist in `docs/developing-evener/agentic-testing.md`. Token at
  `$HOME/.evener/auth-token` (that isolated one).
- **No provider credential needed.** This card drives the session against
  `test/e2e/fakellm` (a scripted OpenAI-compatible provider local to the
  repo): `scripts/e2e/e2e-webui-turn-controls.sh` builds fakellm + evener +
  the SPA into a throwaway run dir, starts both on kernel-assigned ports,
  and points the hub's `providers.toml` at fakellm. The fake answers every
  round with a tool call of its own choosing — but the agent's `urls_add`
  tool is invoked by *prompting the session to call it*, and a scripted
  provider still routes tool calls through the real session loop, so prompt
  the session to call `urls_add` (step 1) rather than trying to inject the
  tool call at the provider boundary.
- A real SPA bundle (`make build-web` — a checkout that has never run it serves a
  one-line `frontend/dist/PLACEHOLDER` and no app). The script above builds it
  unless passed `--skip-web`.
- A hermetic `$WORK` as the session's `working_dir` (the script's
  `$workspace` serves; `mktemp -d` otherwise).

## Steps

Spawn through the AppWire-backed UI: visit `$HUB/auth/$TOKEN`, navigate to
`/new`, use working directory `$WORK`, model `fake/fake-test-model`, and a
prompt that makes the agent record two links, e.g. `Call urls_add twice:
once with url "https://example.com/notes-spec" and label "spec", once with
url "https://example.com/notes-mock" and label "mock". Then stop.` The turn
ends when the fake's `--rounds` budget runs out at the latest, so even if the
model dawdles the session settles. Read the session ref off the resulting
`/s/local:<SID>` path, and wait for `idle` (poll `thread/read`'s
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

2. **[browser] Read the links row.** Authenticate and open the session:
   ```text
   navigate $HUB/auth/<TOKEN>?next=/s/local:<SID>
   await_element [data-testid="composer-input-card"]
   ```
   (Use the literal token, not the path. Note the ref form — a bare `/s/<SID>`
   renders "Page not found" by design.) Open the Details panel and read the
   Shared notes section:
   ```javascript
   (() => {
     const section = document.querySelector('[data-testid="shared-notes-section"]');
     const rows = section ? [...section.querySelectorAll('[data-testid="shared-notes-urls"] li')] : [];
     return {
       port: location.port,                        // page-identity check, always
       path: decodeURIComponent(location.pathname), // /s/local:<SID>; the literal colon breaks naive === compare
       section: !!section,
       rows: rows.map((li) => ({
         testid: li.getAttribute("data-testid"),
         text: li.textContent,
         href: li.querySelector("a")?.getAttribute("href") ?? null,
         remove: li.querySelector("button")?.getAttribute("data-testid") ?? null,
         removeLabel: li.querySelector("button")?.getAttribute("aria-label") ?? null,
       })),
     };
   })()
   ```
   **Expected:** two rows; each row's `testid` is `shared-notes-url-<id>`
   with the `<id>` half byte-identical to the wire id from step 1 (this
   pairing is the discoverability proof — see step 5); each `http(s)` URL
   renders as an anchor (`href` exactly the URL, `target _blank`,
   `rel="noopener noreferrer"`, `DetailsPanel.tsx:259-262`); each row carries
   a Remove button whose `data-testid` is `shared-notes-url-remove-<id>`
   with the *same* `<id>` (`:268-275`). File/path URLs would render as inert
   text with no anchor (the OpenButton affordance is explicitly descoped for
   this section, `DetailsPanel.tsx:129-136`) — this card uses `https:` URLs
   precisely so the anchor half is exercised.

3. **[browser] Remove one link through the real UI.** Click the `U_MOCK`
   row's Remove button (`[data-testid="shared-notes-url-remove-<U_MOCK>"]`)
   and re-read the step-2 query. **Expected:** the `U_MOCK` row is gone; the
   `U_SPEC` row remains; no toast appears in
   `section[aria-label="Notifications"]`. The client commits no local edit —
   the row disappears when `evener/urls/updated` lands (`threads.ts:2655`),
   so a row that vanishes *before* the push would mean an optimistic edit
   someone added against the wire design. Falsify: a `Couldn't remove link`
   toast (the panel's call failed — cross-check step 4's wire call to tell a
   panel bug from a store refusal).

4. **[browser-free] Remove the second link over the wire — the exact
   push-authority proof.** On the `/rpc` socket, call:
   ```json
   {"id":4,"method":"urls/remove","params":{"ref":"local:<SID>",
    "clientMutationId":"<uuid>","expectedInstanceId":"<SID>","id":"<U_SPEC>"}}
   ```
   Param shape is `appwire.UrlsRemoveParams` (`appwire/types.go:828-833`).
   Then re-issue the step-1 `thread/read` and diff the `sessionUrls` list.
   **Expected:** no error; the response is `{}` (empty by design — it carries
   no state, so assert nothing about the list from the response itself); the
   fresh read carries only `[]` (both links gone). A second `urls/remove`
   with the same id now fails `InvalidParams` (unknown id maps to
   InvalidParams, `agent/session_notes_rpc.go:72-76` via
   `TestRemoveSessionURLDaemonPath`) — that refusal is the store telling you
   the first removal stuck, not a new failure.

5. **[browser] Confirm the removal converged without reload, and note the
   discoverability answer.** Still on the session page from step 3 (no
   refresh), re-read the step-2 query. **Expected:** the links row is gone
   (or the section shows only the human/agent rows, or — if the session held
   nothing else — the not-live inert `No shared notes` text); still no
   toast. Then answer the Task 8 minor-5 question explicitly: read one
   remaining-or-remembered row's `remove` testid and strip the
   `shared-notes-url-remove-` prefix — the remainder is the bare id the
   `/url-remove` TUI command (or a raw `urls/remove` RPC) needs. Record in
   the run notes whether that pairing held: on the web panel the id IS
   discoverable (row testid + button testid both embed it, and the button's
   `aria-label` names the entry — `Remove spec` — so a human can pair row to
   id without devtools only if they can see testids, which they cannot; the
   honest statement is "discoverable over `/rpc` + DOM, not from the rendered
   text alone"). The TUI drawer renders URLs without ids at all
   (`cmd/evener-tui/details_drawer.go:110-116`), so there the command stays
   usable-but-undiscoverable — carried forward as the documented sharp edge,
   not silently fixed by this card.

6. **[browser-free, history check] Assert the URL context shape.** Read the
   agent context block the session actually consumed:
   ```bash
   go run ./cmd/evener doctor transcript "$SID" --format outline --range last:80
   ```
   (or find the `NOTES_CONTEXT` turns in the transcript JSONL and read one
   `user` message body). **Expected:** while both links were set, the block
   rendered each as a `Link:` line — `Link: spec
   (https://example.com/notes-spec)` (`agent/session_notes_rpc.go:134-140`,
   pinned by `TestNotesContextBlockContainsNotesAndURLs`) — and after step
   4's removal the next block (if any turn runs after it) no longer carries
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
- **Step 2 (render, exact-in-browser)**: two `shared-notes-url-<id>` rows,
  anchors with exact hrefs, per-row `shared-notes-url-remove-<id>` buttons.
  Falsify: rows keyed by anything but the wire id (the step-5 pairing
  breaks); `http(s)` links rendered as inert text (the `isWebURL` branch
  regressed, `DetailsPanel.tsx:134-136`); Remove buttons missing while live
  (the ordered display rule regressed — rule 2 strips them only when *not*
  live, `:268`).
- **Step 3 (UI remove, qualitative)**: one row gone on push, no toast.
  Falsify: row gone with no push on the socket (an optimistic edit against
  the design); row persisting after the push (the reducer/store fold dropped
  it); toast naming a failure the wire call does not reproduce.
- **Step 4 (wire remove, exact)**: `{}` response, fresh read shows the id
  gone, re-remove refused as `InvalidParams`. Falsify: the response carrying
  a list (the wire type changed — `UrlsRemoveResponse` must stay empty), or
  the read still listing the id (the store never removed it).
- **Step 5 (converge + discoverability, qualitative)**: no-reload
  convergence plus the recorded discoverability verdict (web: id embedded in
  row/button testids; rendered text alone does not show it; TUI: not shown
  at all — the sharp edge below).
- **Step 6 (context, exact)**: `Link: <label> (<url>)` lines while set,
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
- **Only the agent adds; only the human removes — by design.** `urls/add`
  is an agent tool (not in the AppWire catalog:
  `appwire/protocol_notes_test.go:58` asserts the agent verbs stay out), and
  `urls/remove` is a hub RPC (no agent tool removes links). A card that tries
  `urls/add` over `/rpc` gets a catalog refusal; a card that has the model
  call `urls_remove` finds no such tool. Do not read either refusal as a
  notes bug.
- **The remove response is `{}` — there is nothing to assert in it.**
  `UrlsRemoveResponse` carries no state by wire design; the push is the
  authority. A test that reads the removed id out of the response is testing
  a field that must not exist.
- **`/url-remove` takes a bare id with no picker** (Task 8 minor 5 carried
  watch item). On the web the id is embedded in the row and button testids
  (step 5's pairing); in the TUI drawer it is not rendered at all, so the id
  is undiscoverable there without `/rpc`. Usable but undiscoverable — noted
  in scenario prose per the brief, not fixed by this card.
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
  Shared notes section, and use the doctor — not DOM counts — for the step-6
  history shape.
- **The pure add/remove logic is already unit-tested** —
  `TestUrlsAddRemoveTool` (`agent/session_tools_notes_test.go:98`) pins the
  add→event→remove→empty loop including the id round-trip. If those pass and
  this card fails, the break is in the wiring (projection, push fold, or
  panel), not the store.
