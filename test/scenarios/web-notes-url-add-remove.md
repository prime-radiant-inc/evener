# web-notes-url-add-remove: agent-added links render in Notes and human removal survives rejoin

**What this covers**: agent `urls_add` → real session metadata/event → Notes
panel render → human `urls/remove` → push convergence and fresh reads.
Agent notes and URLs retain their existing metadata persistence; the human
whiteboard's atomic mutation snapshot does not replace their storage.

`urls_add` is an agent tool, not an AppWire add RPC. Human removal is an
id-addressed `urls/remove` RPC with `{ref, clientMutationId, expectedInstanceId,
id}`. Its response is empty (`{}`); `evener/urls/updated` and authoritative
`thread/read` carry the list. Preserve capability checks, URL validation,
identity-preserving deduplication and model-context delivery.

**Surface**: use the Notes panel, not Details. The implementation is
`cmd/evener-hub/frontend/src/panes/session/chrome/NotesPanel.tsx`.
The setup/selector guide is `docs/developing-evener/agentic-testing.md`.
There is no `window` store handle: use real UI actions and `/rpc`.
This card is manual guidance, not a recorded execution.

## Pre-state

- Build the hub and real SPA, using an isolated HOME, workspace and
  kernel-assigned port per the setup guide. Record the exact run directory,
  token, port, workspace and process identities. Never use personal state.
- **This variant requires an explicitly chosen live provider/model and its
  credential**, since it asks the model to choose `urls_add`. Opt into the
  live/manual run deliberately; a credential alone must not trigger it.
  First prove a known-good ordinary tool round on this provider. A provider
  setup failure is not evidence of URL behavior.
- The standalone `test/e2e/fakellm/cmd` used by the companion interrupt card
  emits fixed `read_file` calls. It will not call `urls_add` because a prompt
  asks it to. A deterministic variant needs a scripted provider response
  containing real `urls_add` calls at the LLM boundary, not mocked Evener
  internals; do not claim that variant ran without such a script.

## Steps

1. **[browser + wire] Add through the actual agent tool.** Authenticate via
   `$HUB/auth/<TOKEN>`, spawn through `/new` with the isolated workspace and
   selected model, and ask it to call `urls_add` twice: URL
   `https://example.com/notes-spec`, label `spec`; URL
   `https://example.com/notes-mock`, label `mock`. Ask it to finish afterward.
   Read the ref from `/s/local:<SID>`. Await actual successful tool results
   and `idle`; model claims alone do not establish a write.

   Dial `ws://127.0.0.1:$PORT/rpc` with `Authorization: Bearer $TOKEN` and
   initialize using protocolVersion `evener-appwire-v2` (no `jsonrpc` field).
   Call:
   ```json
   {"id":3,"method":"thread/read","params":{"ref":"local:<SID>","includeTurns":true}}
   ```
   Require `result.thread.evener.capabilities.sharedNotes == true` and two
   `sessionUrls` entries with exact URLs/labels, distinct non-empty IDs, and
   populated `addedBy`/`addedAt`. Record IDs as `U_SPEC` and `U_MOCK`. Preserve
   successful `urls_add` tool records and provider call evidence. Capability
   absence or tool errors are explicit failures, not successful empty lists.

2. **[browser] Inspect the Notes links.** Open the session, await
   `[data-testid="composer-input-card"]`, and open **Notes** using the session
   chrome button/menu or `/notes` desktop pane. Query:
   ```javascript
   (() => {
     const section = document.querySelector('[data-testid="shared-notes-section"]');
     return {
       port: location.port,
       path: decodeURIComponent(location.pathname),
       section: !!section,
       rows: [...(section?.querySelectorAll('[data-testid="shared-notes-urls"] li') ?? [])].map(li => ({
         testid: li.getAttribute('data-testid'),
         text: li.textContent,
         href: li.querySelector('a')?.getAttribute('href'),
         target: li.querySelector('a')?.getAttribute('target'),
         rel: li.querySelector('a')?.getAttribute('rel'),
         remove: li.querySelector('button')?.getAttribute('data-testid'),
         removeLabel: li.querySelector('button')?.getAttribute('aria-label'),
       })),
     };
   })()
   ```
   Require two `shared-notes-url-<id>` rows whose IDs exactly match step 1.
   Each HTTPS entry has its exact destination as an anchor with
   `target="_blank"`, `rel="noopener noreferrer"`, and the URL visible beside
   its label (a trusted-looking label must not conceal the destination).
   Each live row has `shared-notes-url-remove-<id>` and the corresponding
   `Remove <label>` accessible name. Supported in-scope file URLs instead
   retain visible URL text and use the in-app open-beside affordance; do not
   describe them as universally inert. Unsupported/out-of-scope destinations
   must not acquire that affordance.

3. **[browser + wire] Remove one through UI.** Click
   `[data-testid="shared-notes-url-remove-<U_MOCK>"]`. Await the authoritative
   `evener/urls/updated` list and re-read the DOM. Require `U_MOCK` gone,
   `U_SPEC` unchanged, and no removal error toast in
   `section[aria-label="Notifications"]`. The client must not invent an
   optimistic list edit: inspect the request/push ordering, not appearance
   alone. A row persisting after its authoritative removal indicates a
   projection/reducer failure.

4. **[browser-free] Remove the other via AppWire.** On the initialized socket:
   ```json
   {"id":4,"method":"urls/remove","params":{"ref":"local:<SID>",
    "clientMutationId":"<uuid>","expectedInstanceId":"<SID>","id":"<U_SPEC>"}}
   ```
   Require success with `{}` and a subsequent authoritative empty list.
   Initialize a fresh socket and repeat `thread/read`: its `sessionUrls`
   must also be empty, independent of any push cached by the first client.
   Re-removing the now-unknown ID with a **new** mutation ID must fail
   `InvalidParams`; do not confuse that with retrying the accepted original
   request under its original ID and payload.

5. **[browser] Confirm convergence and ID discoverability.** Without reload,
   the original Notes panel must show no URL rows and, for this live session,
   `[data-testid="shared-notes-urls-empty"]` (`No links yet`). An ended,
   entirely empty panel may use `shared-notes-empty` instead. No error toast
   is expected. Record the earlier pairing between wire ID, row testid and
   remove-button testid. These expose a bare ID through `/rpc` + DOM; rendered
   label/URL text alone does not expose the ID. Do not claim DOM testids are
   a visible picker. This proof is web-scoped, not a claim about current TUI
   ID discoverability.

6. **[browser-free] Prove model-context updates and dedup identity.** Preserve
   a provider-round record while the URLs are present (request another round
   before removing them if necessary). After removal, request another round
   and inspect the resulting `NOTES_CONTEXT` record and provider request.
   Match the task URLs/labels, not generated `Link:` prose: present URLs must
   reach the model boundary; removed URLs must not survive in newly rendered
   notes context. Historical transcript blocks can still contain them.
   Correlate rounds: at most one `NOTES_CONTEXT` per model round while the
   notes/URL store is nonempty, none while all notes and URLs are empty.

   For the dedup variant, before step 3 ask the model to re-add the spec URL
   with host case/default HTTPS port/fragment variation and an updated label.
   Await successful `urls_add`; require one canonical spec entry with its
   original `id`, `addedBy` and `addedAt`, plus the updated label. Continue
   using that same `U_SPEC` for removal. Record whether this variant ran.

## Expected

Real agent tool records prove addition, Notes shows exact destinations and
wire-paired removal controls, UI and RPC removal converge through pushes,
and fresh rejoin snapshots agree. Model requests reflect current URL context
without erasing history. The shared-notes capability gate, metadata persistence,
validation and identity-preserving dedup behavior remain intact.

## Sharp edges

- `expectedInstanceId` is SID without the `local:` prefix; the ref includes it.
- `urls/add` is not an AppWire method; `urls_remove` is not an agent tool.
- URLs cap at 50; invalid schemes and out-of-scope paths are refused. A URL
  over 2048 characters or label over 280 characters is refused, not clamped.
  `docs/x.md` and `./docs/x.md` canonicalize together; bare filenames retain
  literal percent sequences, while file-URL syntax is decoded once before
  scope validation. Do not broaden scope to make a rejected fixture pass.
- Notes capability absence hides the body. Ended-session displays keep content
  but omit live removal controls. Check status and capability before diagnosing
  a missing control as a render bug.
- For human-note editing see the companion card: its shared live textarea saves
  after a 10,000 ms dirty blur and cancels on same-session refocus; URL removal
  is an immediate separate action, not a note-save button.
- Inspect authoritative records for history; the transcript DOM is virtualized.

## Cleanup

Shut down the fixture session through `thread/shutdown` on `/rpc`, stop only
this run's recorded processes, and reclaim its isolated directories with the
repository scratch cleanup helpers. For a deliberately scripted stack use
`scripts/e2e/e2e-webui-turn-controls.sh --stop "$run"`. Leave personal state
and other scenarios untouched. Report unrun variants and provider/setup
failures explicitly; none counts as a successful scenario execution.
