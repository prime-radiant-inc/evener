# Spec 2: Cheap filtered transcript reads for Managers

The Manager hub's transcript reads become turn-addressed, item-filtered
and budget-measured — extending the content selection the wire already
carries — so a sweep reads what it asked for and pays for what it reads.

## Problem

Tonight, three separate costs, all grounded below:

1. **The latest-turn sweep read is not cheap.** The sweep read ("the newest
   turn, please") returns the window's items in full: every raw tool output —
   including `job_status` echoes that embed a delegate's whole task brief
   twice — evidence-comment bodies and usage blocks. The hub does trim large
   reads — `packItemCandidates` drops the oldest items until the encoded
   turns fit a 1MB soft limit (`cmd/evener-hub/app_item_page_fit.go:179-213`,
   documented at `docs/product/history-content.md:32-37`) — but that soft
   limit sits far ABOVE the Manager's 256KB whole-read cap (grounding
   below), so a Manager read dies whole at the cap before the packer ever
   helps it: the packer exists at the wrong scale for this path. Over
   eight hours this dominated the sweep's context budget, for
   information that was one agent message.
2. **Older turns are not addressable.** Finding one specific earlier turn (a
   verdict relayed two turns back) required paging backward through opaque
   cursors, because `itemLimit` pages items — default 40, max 40
   (`appwire/paging.go:8-22`) — regrouped into turn fragments, with no way to
   name a target turn. Each page is a full round trip that cannot say what it
   is looking for.
3. **The Manager tool cannot say what it wants.** The read returns
   reasoning, raw tool outputs, steering frames and system messages alike.
   The wire's content selection (grounded below) already lets a caller
   narrow categories — but the Manager tool's schema refuses to pass it,
   and the wire's categories are coarser than item types, so every
   Manager read pays for everything.

The aggravation that makes this urgent: the whole Manager read dies if it
exceeds the 256KB cap (`session_reads.go:241-253`, "no data returned"), so
over-reading is not merely expensive, it is lossy — and the docs teach
follow-up paging (`docs/automation.md:189-197`) whose suggested calls
would silently drop any new shape this spec adds (grounded below).

## Current behavior (grounded at origin/main 2be411cd8f)

- **Read path.** `read_transcript`'s hub branch
  (`agent/session_tools_hub.go:705-730`): session refs →
  `ThreadTurnsListParams{ref, cursor, itemLimit}`; a cursorless request is
  a full `thread/read`; a cursor pages older items
  (`cmd/evener-hub/session_reads.go:56-92`,
  `cmd/evener-hub/app_rpc.go:1376-1429`). The cursor is a hub-minted,
  identity-fenced base64 `{thread_ref, incarnation, projection_version,
  before-position}` (`internal/appitempaging/cursor.go:25-54`); the wrong
  identity yields `TranscriptItemCursorStale`. Items carry positions
  `{entry, item, sub}` (`internal/appitempaging/page.go:196-199`) and
  regroup into turn fragments (`RegroupTurnFragments` :124-153). The
  cursor's bound is an exclusive backward bound: `CandidatesBefore`
  returns items strictly older than the boundary (:204-213) — and the
  encoding carries ONLY a `before` bound, so a FORWARD (after-bound)
  continuation and a version-bearing single-item fence are owned new
  forms extending it (scope below).
- **The packer exists; its soft limit is above the Manager cap.**
  `packItemCandidates` trims the oldest items until the encoded turns
  fit `transcriptRPCResultSoftLimit = 1 << 20` (1MB)
  (`cmd/evener-hub/app_item_page_fit.go:14,122,179-213`), and it runs
  on every Manager read shape — the cursorless read
  (`cmd/evener-hub/session_tools.go:609`), cursor pages
  (`cmd/evener-hub/app_rpc.go:445`) and saved pages
  (`cmd/evener-hub/app_threadread.go:230,287`). The defect is scale,
  not absence: the Manager's whole-read cap is 256KB
  (`session_reads.go:241-253`), so an over-cap Manager read fails whole
  before the 1MB packer engages. This spec EXTENDS the existing packer
  — window and budget semantics land in `app_item_page_fit.go` — and
  builds no second packer.
- **Two gates refuse anything but today's shape, and a third trims the
  wire's own vocabulary.** The agent-side schema gate rejects extra hub
  params before dispatch (a windowed call dies with a generic type
  refusal, `agent/session_tool_repair.go:163-213` behind the closed hub
  schema at `agent/session_tools_hub.go:70-89`); behind it, the hub's own
  `SessionOperationRequest.Validate` rejects Manager history reads
  carrying any nonzero `ThreadContentParams` — "content filters are
  unsupported" (`appwire/session_tools.go:126-127`). Both gates are
  landing zones.
- **The wire already filters — coarsely.** `ThreadContentParams`
  (`appwire/history_content.go:8-22`) defines the include/exclude flags,
  and `Selection.Item` (:65-97) projects items per category:
  `agentMessage`, `userMessage` and user steering share the conversation
  category; reasoning has its own; command executions have full and
  intent-only forms; everything unlisted shares the system category.
  `Selection.Item` cannot express "agentMessage only" or "messages
  without user steering", and `Items` (:100-110) DROPS excluded items
  outright — a filter that cannot report what it withheld. Both
  `ThreadReadParams` (`appwire/types_turn.go:323`) and
  `ThreadTurnsListParams` (:539-540) embed the params, and
  `docs/product/history-content.md:3-37` documents the categories.
  Views are selection-fenced: a cursor carries its `Selection` plus a
  selection-scoped snapshot incarnation
  (`internal/apphistory/paging.go:24-45,295-305`), and changing any
          selection flag mints a fresh latest read — reusing another view's
  cursor fails `transcriptItemCursorStale`
  (`docs/product/history-content.md:39-45`). That key encodes only the
  coarse category bitmask: `Key()` renders the selection as a hex
  string composed into the incarnation prefix
  (`appwire/history_content.go:47-51`;
  `internal/apphistory/paging.go:21,41`) — so the window/types/bounds
  vocabulary is an owned EXTENSION of the same key scheme, not a
  value the key carries today, and the windowed view identity is
  owned work extending the same incarnation.
- **Items mutate in place — and in-flight items have no version to
  fence.** The index updates an existing item's
  contributor and `Version` (`internal/transcriptindex/build.go:974-997`),
  and later call results merge into an earlier item, replacing nonempty
  content (`internal/apptranscript/logical_turn.go:243-305`). `Version`
  is an entry ordinal and `0` marks an overlay item — no history
  version — (`appwire/types_turn.go:253-256`), and a running call's
  overlay output mutates in place with no version advance
  (`internal/appoverlay/overlay.go:489-506`). A position
  fence alone therefore does not make a fetch revision-stable: the item
  at a stable position can change without any stale-cursor error, and
  a version-0 item has no fenceable version at all.
- **Reasoning has no servable summary form.** The projection serves each
  thinking part's `Text`, falling back to its `Summary` only when the
  text is empty (`internal/apptranscript/apptranscript.go:1011-1040`);
  client summaries derive from that projected text. There is no
  alternate summary shape on the wire to select.
- **Turn ids are addresses, not ordinals, and turn resolution barely
  exists.** The id families are `turn_m<N>`, `turn_system` and `t_<id>`
  (`agent/schema/turn.go:516-519`); order comes from the transcript's entry
  ordinal, which the projection resolves — ids do not sort by form. No
  content read names a turn today. The only turn-id resolver is the
  saved-index's unexported `findTurn`
  (`internal/transcriptindex/build.go:520-552`), a backward scan that
  SKIPS `turnKindLegacy` turns; `AnswerContinuation`
  (`internal/transcriptindex/answer_continuation.go:39-55`) accepts
  execution ids only. `ThreadTurnItemsListParams` is `ScopeUnimplemented`
  (`appwire/protocol.go:178`). Turn-addressed windows are new machinery
  this spec owns (below).
- **Saved-index windows are item-shaped.** For daemonless sessions the hub
  serves the transcriptindex sidecar; its windows select items, not turns:
  `Latest` takes an item count (`internal/transcriptindex/window.go:38-57`),
  `LatestSince` takes a snapshot identity (:176-216), `Before` takes an
  item position (:232-250), and the index can be caught up to the
  transcript file's end (`app_threadread.go:373-393`). Turn windows wrap
  these primitives; they do not replace them.
- **The daemon decodes strictly, capabilities ride the wire, and remote
  sources rebuild them by hand.** `ThreadReadParams.UnmarshalJSON` rejects
  unknown params (`appwire/types_turn.go:516-533`;
  `ThreadTurnsListParams.UnmarshalJSON` :574-582), so new params cannot
  be sent to an older daemon at all. `ThreadCapabilities`
  (`types_thread.go:618-624`) is the existing additive-capability
  mechanism — but `RemoteHubSource` reconstructs capabilities field by
  field (`cmd/evener-hub/internal/appsource/remote_hub_refs.go:134-139,185-203`),
  so a new bit is false on every remote read until that rebuild names
  it.
- **Follow-up suggestions are fixed-shape.** The read path's follow-up
  emission produces only `{ref, cursor, itemLimit}`
  (`cmd/evener-hub/session_tools.go:253-260`), and
  `SessionStatusTarget` (`appwire/session_tools.go:393-406`) has no
  window, filter or bound fields. Any new read shape that does not
  teach its own continuation would degrade to an unfiltered read the
  moment a caller follows a suggestion.
- **The typed clients validate strictly.** The generated item allowlist
  knows no fetch or elision fields
  (`appwire-client/typescript/types.gen.ts:5752-5757`),
  `validWireItem` rejects unknown keys
  (`transcriptValidators.ts:295-300`), and `ThreadTurnsListResponse`
  allows only six fields with deferred-page validation rejecting others
  (:652-670). New wire fields land with regenerated types and validator
  updates or the clients reject the responses.
- **Overlaps.** Spec 3 owns the serving facts on the same path; 5659 and
  5641 touch the adjacent schema block in `session_tools_hub.go`.

## Scope

Additive parameters on the Manager hub transcript read for session refs.
Existing call shapes keep their meaning byte-for-byte. The new surface:

1. **`turnWindow`** — one of:
   - `{latest: N}`: the newest N turns, served in transcript
     (chronological) order — today's page behavior
     (`internal/appitempaging/page.go:93-113`). `latest: 1` is the
     sweep read.
   - `{turn: turnId}`: the exact named turn, inclusive — the
     locate-a-turn read the evidence base demanded (a verdict two turns
     back), and the address spec 4's ask-turn fetch uses.
   - `{since: turnId, limit}`: the turns recorded strictly after that turn
     id, in transcript order, oldest-unseen first — the catch-up read.
     The boundary is exclusive: `since` names the newest turn the caller
     has already processed, and an id equal to the newest turn returns an
     empty turn list.
   - `{before: {turnId, limit}}`: the backward form, turns strictly older
     than the boundary id, also exclusive.
   Id resolution is owned new machinery on BOTH serving paths: hub-side,
   `findTurn` grows into an exported resolver that also resolves legacy
   turns (its skip is a gap, not a contract); daemon-side, the read
   handler gains turn-id→position resolution. An id a path cannot resolve
   is a typed error naming the session's current turn frontier, never an
   empty result in disguise.
2. **`itemTypes`** — a list of the projection's exact emitted item-type
   strings (`agentMessage`, `userMessage`, `reasoning`, `systemMessage`,
   `commandExecution`, and the rest of the projection's own vocabulary;
   nested content like `input_image` is not a standalone type and is
   governed by the byte bounds), plus the convenience alias `messages`,
   which is source-aware: it expands to `agentMessage`, `userMessage`,
   and the user-authored `steering` items (`Source == "user"` — the
   projection's own conversation category, `appwire/history_content.go:65-73`)
   — all visible conversation text, which is what the alias promises.
   Steering frames not authored by the user are NOT in the alias: a
   caller who wants every steering frame names `steering` explicitly in
   `itemTypes`. ONE filter, one
   mechanism: in windowed mode the exact-type predicate is the only
   filter, applied by the windowed handler; `Selection.Items` is
   bypassed for filtering (its projection forms — the intent-only
   command view — still apply to what is served), so elision is
   computed once from the FULL unfiltered turn item list. A request
   that names coarse content flags is TRANSLATED into the exact-type
   predicate at the tool edge (sugar for `messages`-shaped intents) —
   mixed requests have one behavior because there is one mechanism,
   and nothing is dropped silently before the elision pass. The
   windowed view identity extends the existing selection-scoped
   snapshot incarnation (grounding above): window kind, type list and
   byte bounds join the view key, and changing any of them mints a
   fresh view — reusing another view's cursor fails stale, exactly as
   selection flags do today. The new filtering surface rides ONLY
   with a window: `itemTypes`, the byte bounds and the budget are
   refused typed at the tool edge when the request carries no
   `turnWindow` (today's read shape stays byte-for-byte; there is no
   filter-only mode to define). The tool edge translates the coarse
   flags that have an exact-type equivalent (the `messages` shape)
   and REFUSES the rest typed, naming the exact-type form to use —
   no combination is undefined.
3. **Per-item byte bounds.** New additive wire params bounding what
   travels: tool output by `previewBytes` (default 2048), message text
   (`agentMessage`, `userMessage`) by `messageBytes` (default 8192).
   Truncation is always labeled on the item, never silent — the
   LABEL is unconditional — and the label's wire carrier is named,
   because the item type is closed generated types that carry no
   such fields today: the label is owned additive wire work ON
   `ThreadItem` (`appwire/types_turn.go:181-267`) — `truncated`
   (bool) and `truncatedBytes` (the item's untruncated body
   length) — beside the `fetchCursor` field the truncated and
   elided shapes share, with the generated types and the
   appwire-client validators regenerated per rule 5. The
   truncated item's `fetchCursor` is a separate, K-bounded line
   (scope 5), so a truncated item beyond that bound is labeled but
   cursorless.
4. **`fetchCursor`, version-fenced.** Every elided or truncated SETTLED
   item carries a `fetchCursor`: a hub-minted, identity-fenced cursor
   naming that single item at its current `Version` — an owned
   extension of
   the existing cursor encoding, which carries no item `Version` field
   today (grounding above). Because items mutate in place
   (grounding above), the fence carries the version: a fetch against a
   changed version fails typed (`item-changed`, naming the new version)
   instead of silently serving different bytes. An in-flight item has
   no version to fence (`Version == 0`, grounding above), so it is
   never fetchable: a truncated or budget-elided in-flight item is
   labeled with its byte count and `inFlight: true` and carries NO
   `fetchCursor` — it is reconstructable by re-reading the window after
   the turn settles (reads are pure; the re-read mints the cursor at
   the settled version), and the elision line never pretends a fence
   exists where the bytes have no stable version. The follow-up read
   returns the item bounded by the 256KB whole-read cap, with a
   byte-range continuation when the full body is larger; there is
   deliberately no unconditional one-call full-body guarantee.
   Follow-ups are idempotent per (position, version). The byte-range
   contract is fully specified:
   - **The ranged byte domain is the item's CANONICAL WIRE ENCODING**
     — byte-identical to the item's encoding in an unfiltered read of
     the same view (the `ThreadItem` serialized exactly as the packer
     emits it, grounding above). Fetching every range and concatenating
     reproduces the unfiltered read's bytes for that item, asserted
     byte-for-byte (journey 7); that equality DEFINES "the item's
     body", so a heterogeneous item — `Text` plus `ArgumentsJSON` plus
     `Output` plus `Raw` together — has ONE byte string, not per-field
     streams, and images ride it as the wire's references (ids and
     routes), never as binaries, so an item's length is its JSON
     length.
   - **Offsets are byte offsets into that fixed encoding**, minted at
     fetch time against the version-fenced encoding, so a range can
     never split anything that matters. WHO forms the encoding and
     slices the range is per-arrangement, each answer named: on the
     `serving-daemon` arrangement the SERVING DAEMON forms the
     canonical encoding and slices the addressed range — the daemon
     read handler that builds the response
     (`server/appwire_read_handlers.go:98`, `handleAppThreadRead`)
     is the assigning site, reached through the hub's client call
     (`cmd/evener-hub/internal/appsource/local_daemon.go:648-659`,
     the transport hop that receives it); the `hub-compat`,
     `saved-index` and `remote-source` arrangements construct the
     encoding hub-side (the origin routing below). The byte domain
     is the same canonical wire encoding in every arrangement, so
     journey 7's equality holds per arrangement against the
     unfiltered read the Manager would issue. Range data travels
     base64-encoded, so an
     arbitrary offset (mid-UTF-8, mid-JSON-token) is a valid boundary
     and the client needs no boundary rules — decode, concatenate in
     offset order, parse the complete encoding when the last range
     arrives.
   - **The partial response carries** the identity fields (position,
     version), `totalLength` (the canonical encoding's length), the
     range's `offset`, `data` (the base64 range bytes), and a
     continuation cursor carrying (position, version, next offset).
     Metadata (type, tool name, timestamps) never repeats per
     fragment: it lives inside the canonical bytes exactly once, by
     construction.
   - **The final fragment is short**; completeness is
     `offset + length = totalLength`. The version fence covers every
     range: an item that changed between ranges fails typed at the next
     range, never silently mixing versions.
   - **The REQUEST grammar, end to end.** The Manager tool's hub branch
     gains one additive argument, `fetchCursor` (the same
     `session_tools_hub.go` schema block; today the edge maps only the
     history/job shapes — `managerReadDefinition` :36-64 and
     `invokeHubRead` :705-730 — so the argument is a named landing
     zone). A request carrying `fetchCursor` is a FETCH, and its
     grammar is complete: `ref` is REQUIRED (dispatch resolves the
     session ref before routing, :705-730; the ref names the session,
     the cursor names the item within it). The operation member is
     named: `SessionOperationRequest`
     (`appwire/session_tools.go:82-99`, closed today with no fetch
     member) gains an additive `Fetch` member, and the new tool
     argument decodes into it — today the branch's dispatch builds
     only `request.Turns` or `request.JobOutput`
     (`agent/session_tools_hub.go:720-730`), so the branch gains
     the third arm; a fetch-carrying request is a FETCH at the
     operation level, the same grammar this scope states — and
     the exclusivity is the real mechanism, named: `Validate`
     enforces exactly-one operation by counting the member slice
     (`appwire/session_tools.go:105-113` — that count IS the
     exclusivity rule), and the `Fetch` member JOINS the count,
     so a `Fetch` joined with `Turns` or `JobOutput` reaches
     count two and is refused typed. The content-filter branch
     (`:126-128`) is a `Turns` field-level rule, not an
     exclusivity mechanism; without joining the count a
     `Fetch`+`Turns` request passes at count one and the
     dispatch silently builds the `Turns` arm, dropping the
     cursor. And the
     fetch carries
     NOTHING else — `turnWindow`, `itemTypes`, the byte bounds,
     `windowBudgetBytes` and `itemLimit` from this spec's vocabulary,
     and the legacy paging `cursor` plus the job-branch fields
     `jobId`, `maxBytes` and `beforeBytes` (the schema's history and
     job branches are mutually exclusive,
     `agent/session_tools_hub.go:73-89`) are each refused typed at the
     tool edge naming the conflict (a fetch is item-addressed; its
     cursor already carries the view identity). T12's hole closes
     with them: a fetch carrying the existing paging `cursor` is
     refused, not undefined.
   - **The RESPONSE carrier, end to end.** The fetched bytes need
     a wire home on the way back, named with the request's own
     completeness: `SessionOperationResponse`
     (`appwire/session_tools.go:202-218`, closed today — `Read` at
     :205, `Turns` at :206, no fetch member) gains an additive
     `Fetch` member: a named fetch response type carrying the
     partial-response payload above — the identity fields
     (position, version), `totalLength`, the range's `offset`,
     `data` and the continuation cursor — owned wire work with
     the generated types and validators (rule 5). The hub
     dispatcher gains the FETCH arm beside its `Read` and `Turns`
     arms (`cmd/evener-hub/session_tools.go:237-398`: decode the
     cursor, resolve by origin, assign the response member), and
     the 256KB Manager read-cap gate gains the FETCH arm
     (`cmd/evener-hub/session_tools.go:399-403`, the whole-response
     `managerReadWithinCap` at `cmd/evener-hub/session_reads.go:18`):
     the fetch response is bounded by the same whole-read cap
     every other Manager operation response obeys, the promise
     this scope already makes — and the cap is never a DEAD
     END for a valid item, because the response's size is
     bounded before it is built: the hub mints each fetch as a
     byte RANGE of a chosen length (a named constant; the
     range's offset rides the hub-owned cursor, so the serving
     side returns exactly the addressed range), sized so the
     ENCODED response — the identity fields, `totalLength`,
     the served `offset`, the base64 body at its 4/3
     expansion, and the origin-bearing continuation cursor —
     fits the cap by construction, the margin a measurement
     obligation on a near-cap fixture (longest actor ids,
     longest cursor encoding at the new form); the
     continuation advances past each range, so an item of any
     size streams in range-sized pieces, and the cap gate
     stays as the backstop a VALID item never reaches as a
     refusal. Without the response member the
     request grammar is half a contract — the hub could resolve
     the cursor as designed and still have no declared route to
     return the bytes.
   - **The cursor is HUB-OWNED and the fetch is HUB-RESOLVED.** The
     cursor is hub-minted by construction — hub-owned identity in the
     encoding (`internal/appitempaging/cursor.go:19-65`) — and it
     carries its ORIGIN: the arrangement that minted it, scope 7's
     `appliedBy` values (`serving-daemon` | `hub-compat` |
     `saved-index`) plus ONE fetch-only value, `remote-source`, for
     a cursor minted over a remote source's window — whose READ is
     marked `appliedBy: serving-daemon` because the remote daemon
     did the window's work, while its FETCH resolves hub-side, a
     different fact about a different operation. The hub decodes
     its own cursor and routes
     by origin, so every arrangement's elision
     lines name recovery instructions its own contract can fulfill:
     - `serving-daemon`: the item fetches through an additive
       `fetchCursor` field on `ThreadReadParams`, sent only to a
       bit-advertising daemon (whose strict `UnmarshalJSON` decodes
       it, grounding above); the fence is the cursor's item
       version. The return leg names its own carrier, or the
       bytes stop one hop short: the daemon's read response is
       the closed `ThreadReadResponse`
       (`appwire/types_turn.go:367-402`: `Thread` mandatory,
       `BootGeneration`, no fetch member today) — it gains an
       additive fetch member REUSING the named fetch-response
       payload type (identity, `totalLength`, the served
       `offset`, `data`, the continuation), assigned by the
       daemon read handler that builds the response
       (`server/appwire_read_handlers.go:98`,
       `handleAppThreadRead`), received by the hub's client call
       (`cmd/evener-hub/internal/appsource/local_daemon.go:648-659`,
       the transport hop, typed `ThreadReadResponse` today), read
       by the hub's
       FETCH arm and assigned to the outer
       `SessionOperationResponse.Fetch` member. The whole round trip
       is named: cursor in on `ThreadReadParams` → the daemon
       read handler, which forms the canonical encoding and
       slices the addressed range → `ThreadReadResponse`'s fetch
       member → the hub
       FETCH arm → the outer member → the Manager. The
       hub-compat and saved-index fetches construct their
       result hub-side and never touch the daemon member.
     - `remote-source`: the fetch NEVER crosses the inter-hub
       wire, because neither direction of a forwarded cursor
       can work — cursor validation is an EXACT match on thread
       ref, incarnation and projection version
       (`internal/appitempaging/cursor.go:59-64`), so the outer
       hub's cursor fails the inner hub's validation, and the
       inner hub's cursor names `local:` with no outer identity
       to route by. The byte domain differs across the hop too:
       the ordinary remote read TRANSLATES actors, image routes
       and authorship into the outer namespace
       (`cmd/evener-hub/internal/appsource/remote_hub_refs.go:407-455`,
       the typed response case at `:687`), so the inner hub's
       raw encoding is not the encoding the Manager reads, and
       byte fragments cannot receive those transformations after
       slicing. The fetch follows the pattern the remote ITEM
       PAGING already proves
       (`cmd/evener-hub/internal/appsource/remote_hub_source.go:743-753`:
       the remote hub's opaque cursor never reaches the
       controller; each page mints a controller-owned identity
       and retains the inner state behind it): the fetch cursor
       is a CONTROLLER-OWNED identity carrying the remote
       source, the item's position and version, and the range
       offset; each fetch RESOLVES HUB-SIDE — the hub re-reads
       the item through the ordinary translated source read, so
       TRANSLATION PRECEDES ENCODING AND SLICING, and the
       canonical encoding for a remote-origin item is the
       CONTROLLER's unfiltered encoding, the exact bytes
       journey 7 asserts against the Manager's own read.
       Reads are pure, so a per-range re-read is correct, and
       the version fence fails typed on drift. The leg's REACH
       is what the inter-hub read carries: an item the ordinary
       remote read cannot deliver fails a TYPED refusal
       naming the degradation (`fetch-unavailable-remote`, the
       item beyond the inter-hub read's reach) — never a silent
       path; the recovery is the upgrade path, a future
       remote-side range serve, out of scope here.
     - `hub-compat`: the item resolves HUB-SIDE, re-derived through
       the same legacy page read that built the compat window — with
       the recipe's bounds stated, because purity does not freeze a
       latest-page result and the legacy item page is bounded (40
       items, `appwire/paging.go:8-22`): the hub re-issues the legacy
       read and, when newer items have scrolled the target out of
       the newest page, pages BACKWARD with the existing exclusive
       `Before` boundary (`ThreadTurnsListParams`,
       `appwire/types_turn.go:540-551`) — but ONLY on peers that
       advertise the EXISTING `PageBefore` capability bit
       (`appwire/types_thread.go:618-624`; the remote rebuild
       deliberately treats the bit as off, and
       `remoteCursorlessBeforeRefusal`
       (`cmd/evener-hub/internal/appsource/remote_hub_source.go:1632-1645`,
       issue #3283) is the named evidence that the pre-`Before`
       peer population is real), so the traversal is a gated
       recovery path, never an availability dependency. The bit
       the FETCH reads at fetch time is the same hub-owned one the
       serving arrangement stamps — `PageBefore` is overwritten
       from the serving arrangement for local and saved threads
       (`cmd/evener-hub/app_threadread.go:892-934`), NOT the
       probe-cached daemon bit windowed reads gate on — so a
       fetch-time skew is a property of the arrangement, not a
       cache race: a peer whose arrangement lacks the bit is
       pre-`Before` and takes the newest-page re-derivation below
       with the same typed failure, the uniform contract. The
       remote rebuild masks the bit unconditionally
       (`cmd/evener-hub/internal/appsource/remote_hub_refs.go:134-158`),
       so every remote peer is pre-`Before` and journey 10's
       page-back success leg runs on a LOCAL compat peer, stated. Its
       stopping rule is executable by construction: the cursor's
       OWN target position is the endpoint — positions are ordinals
       (`internal/appitempaging/page.go:196-199`), each `Before`
       page serves strictly older items (:201-214), and paging
       backward ENDS when the target position is reached or passed
       — backed by a server-side maximum page count, a NAMED
       constant bounding COST, not termination (the transcript is
       finite; exhaustion at the cap fails typed with the recovery
       below): its default value is CHOSEN — a round-trip cost
       budget against the peer — and journey 10's cap-exceeding
       leg derives its own fixture from the chosen value, cap+1
       pages of arrivals, asserting the typed failure and the
       recovery at exactly the chosen bound — displacement after
       minting is caused by unbounded NEW
       arrivals, never by the original window's span (`{latest:
       N}` has no maximum), so the cap is never a quantity
       derived from the window; no held-page cache, no new hub
       state. The precedence is REACHABILITY-FIRST, because the
       hub must reach the item to read its version at all: reached
       → the version fence decides (`item-changed` on a changed
       version, naming the new version); not reached within the
       page-count cap → the fetch
       fails the typed `item-unreachable-on-compat` naming the
       recovery (re-read the window: a fresh view re-derives current
       state and mints fresh cursors); an item that both changed and
       scrolled out reports unreachable — the recovery is the same.
       On a pre-`Before` compat peer the fetch attempts only the
       newest-page re-derivation and, the target absent from it,
       fails the SAME typed `item-unreachable-on-compat` with the
       same recovery — the typed contract is uniform across the
       whole compat population. The old daemon strict-decodes no
       `fetchCursor`
       field, and none is ever sent to it — a fetch never reaches a
       daemon that cannot serve it.
     - `saved-index`: the item resolves from the hub's own index at
       the cursor's position and version; no daemon exists to
       consult.
     A byte-range continuation submits its cursor the same way —
     hub-decoded, routed by origin. The validation errors are named:
     the ORIGIN is required and validated at fetch decode — a cursor
     with no valid origin (a legacy paging cursor presented as
     `fetchCursor`, or a pre-upgrade cursor minted before the field
     existed) fails the cursor decode typed, never routed by
     guesswork; a malformed cursor fails the cursor decode typed; a
     cursor whose
     thread identity no longer matches the fetch's `ref` fails
     `TranscriptItemCursorStale` (the existing identity fence); a
     changed version fails `item-changed` naming the new version; and
     the combination refusals above.
5. **Measured budget with a floor.** The server enforces a
   windowed-response byte budget (`windowBudgetBytes`, requested default
   192KB) by extending the EXISTING packer (grounding above), not
   arithmetic: it encodes items into the response until the serialized
   window content reaches the effective budget, then stops
   and reports the consequence. Two bounds, both stated: the budget
   bounds the window CONTENT; the 256KB cap bounds the OUTER response
   — envelope, serving facts and follow-up suggestions included — and
   keeps today's refusal. The effective budget is clamp(requested,
   floor, cap − envelope-and-follow-ups): `cap-margin` is the room the
   outer envelope needs inside the cap, asserted as a proven bound
   (packed content plus envelope never exceeds the cap). The
   FLOOR's funding is a head-count stated by construction, with
   both bounds named: a turn fragment carries at most
   itemLimit-max (40) per-item lines — bounded bodies,
   labeled-truncated lines and per-item elision lines; the
   per-turn item cap is the LINE bound — of which at most K
   (10, the per-turn item cap default) bear cursors (a fetch
   cursor on a truncated item and a per-item elision line SHARE
   the K bound: K bounds the cursor-bearing SUBSET, never the
   line count), plus its turn's ONE aggregate record — and that
   construction, 40 lines at most plus one record, is what makes
   the floor sufficient below, so the floor's value is measured,
   never asserted; the envelope's own content stays the
   window-level trimming report and NOTHING ELSE (below), so its
   own size is measured on that declared content, never asserted.
   The truncation LABEL sits deliberately
   OUTSIDE the K bound: every served-but-truncated item ALWAYS
   carries its
   own truncated flag and byte count (scope 3's never-silent
   promise), so a truncated item beyond the Kth cursor-bearing line
   is LABELED-CURSORLESS — served, labeled, counted in its turn's
   ONE aggregate elision record (scope 6, shape c) — and an elided
   item beyond the bound rolls into the record the same way; the
   record's split populations carry a
   truncated-beyond-K count and an elided-beyond-K count beside the
   type-excluded and cap-withheld ones, each with its recovery
   named (scope 6). The caller always knows WHICH items are
   truncated, because every one is labeled. The max-case fixture is
   therefore a K-cursor burst, not an unbounded one — TWO legs, both
   at the NEW version-and-origin-bearing cursor encoding this spec
   mints: the round-9 measurement of 40 minimal lines at 10,381
   bytes (`internal/appitempaging/cursor.go:25-53`) was the OLD
   identity+position form's basis — cited as the measurement that
   falsified this spec's earlier 4KiB and 10KiB ceilings — and each
   leg's floor is re-derived from its own fixture measurement,
   never carried over from the old form: a window
   whose every selected item is budget-elided with K elision lines,
   and the per-turn-cap-maximum leg — itemLimit at its max, all 40
   served items truncated and labeled, K truncation cursors at the
   version-and-origin-bearing cursor encoding and the
   labeled-cursorless remainder in the aggregate record — each plus the
   longest serving-fact ids and the widest follow-up target, one
   aggregate record and one continuation cursor. The journey MEASURES
   each leg's complete encoded size and asserts the floor covers
   them — floor ≥ measured worst case of either leg — with the
   floor's own value
   (16KiB) held to the measurement as an obligation, not a claim: if
   the measured worst case breaches the floor, K or the floor adjusts
   before landing, never the assertion. Every floor and envelope
   number in this scope is that obligation's starting value,
   re-measured on the fixture.
   Per-item cursor-bearing lines — elision lines, truncation labels
   and their cursors (scope 6, shapes a and b) — AND each turn's ONE
   aggregate elision record (shape c) are FRAGMENT CONTENT, counted
   toward the
   measured budget as part of their turn fragment: the packer measures
   the complete encoded fragment — bounded bodies, their elision
   lines and their turn's aggregate record together, ONE accounting —
   and a fragment that does not fit the remaining
   budget is withheld with its mode's continuation, never refused. At
   least one turn fragment is always served: the floor funds the
   bounded K-line burst, one aggregate record and one continuation
   cursor (all held to the max-case measurement above), so the
   degenerate edge below still tells the caller exactly what to
   lower. The floor
   (16KiB) always covers the
   response envelope, one turn fragment with its own aggregate
   record, and one continuation cursor,
   so a caller-set budget below the floor is served at the floor (stated
   in the response), never refused and never lying. The envelope's own
   bounded content is the window-level trimming report, NOTHING ELSE:
   served count, withheld count, the boundary turn ids and ONE
   continuation cursor — not an exhaustive per-turn list; exhaustive
   naming is capped and then aggregates. Aggregate elision records
   (shape c) are deliberately OUTSIDE that envelope set: each is
   fragment content, measured inside its turn's budget like a bounded
   body (the paragraph above), so the response's per-turn record
   population is itself budgeted and the cap guarantee holds by
   construction under the ONE accounting. Because the budget measures
   the ENCODED BYTES of the packed content, every content field —
   arguments, raw data, images, tool errors — and its JSON escaping
   overhead count by construction; the envelope's own bytes are
   accounted once, at the bounds — inside the floor's coverage
   (above: the floor funds one turn fragment, the envelope, and
   one continuation cursor) and inside the cap's outer bound — so
   the envelope's content rule and its byte rule are one
   accounting, never two.
   Parameters are desires; the packed response states the effective
   budget it enforced and what it actually served:
   - `latest` trims from the OLDEST end (the withheld turns are older
     than the served ones); the response says which end was trimmed,
     and the continuation resumes at the oldest withheld turn, in the
     same chronological order (the existing backward-bound encoding
     serves it).
   - `since` trims from the NEWEST end (the withheld turns are newer,
     and catch-up is oldest-first); the response says which end was
     trimmed, and the continuation resumes at the first withheld turn
     through an owned FORWARD (after-bound) cursor form (grounding
     above), across calls until exhausted.
   - No parameter combination is refused for size. A single item whose
     bounded body alone exceeds the remaining budget is elided with its
     `fetchCursor`. The degenerate edge — no item fits at all — yields a
     response of labeled elision records with fetch cursors, which tells
     the caller exactly what to lower.
6. **Intra-turn item cap and the one elision rule.** In windowed mode,
   `itemLimit` becomes the per-turn item cap (windowed default 10, max
   unchanged at 40), selecting items in transcript order within each
   turn — and it applies to EVERY window form, `{turn: id}` included:
   a located turn serves like any windowed read, bounded with its own
   continuation (the locate case is one call that names the target,
   not a wholeness guarantee). Elision has exactly three shapes, one
   rule: (a) served-but-truncated, settled → an on-item truncation
   LABEL in scope 3's declared wire fields (`truncated` and
   `truncatedBytes`, ALWAYS present, never silent — scope 3's
   promise) plus the item's `fetchCursor`, bounded
   at K per response (scope 5's shared cursor-line bound): a
   truncated item beyond the Kth cursor-bearing line is
   LABELED-CURSORLESS — served with its label but no cursor,
   counted in the aggregate record's truncated-beyond-K count,
   reconstructable by the NAMED RE-READ of the same window at a
   wider byte bound (a fresh view: the bodies were served, the
   labels are on the items, and a wider-bound re-read
   serves them untruncated or re-mints their cursors); (b)
   selected-but-budget-elided, settled
   (an item whose bounded body exceeds the remaining budget) → a
   per-item elision line — the item rides as a `ThreadItem`
   carrying its identity and scope 3's declared label fields
   (`truncated`, `truncatedBytes` naming the unelided length)
   with no body — plus its `fetchCursor`, bounded at the same K
   per response (scope 5's shared bound) — elided items beyond K
   appear only in the aggregate record's elided-beyond-K count,
   reconstructable by its same-view
   continuation; an IN-FLIGHT item (Version == 0, grounding above)
   takes neither cursor in either shape: its line is labeled
   `inFlight` with its byte count and no `fetchCursor` (scope 4's
   rule — the version-0 hole has no fence until the turn settles;
   contract 5 carries the same carve-out); (c) type-excluded,
   withheld by the
   per-turn cap, or counted beyond the Kth cursor-bearing line of
   shapes (a)
   and (b) (scope 5's shared bound) → ONE aggregate elision record
   per turn that SPLITS
   its populations truthfully: per-type excluded counts (e.g.
   `commandExecution: 3, reasoning: 1`), the cap-withheld
   remainder, a truncated-beyond-K count (the LABELED-CURSORLESS
   served items of shape a — their labels are on the items; the
   record carries the count) and an elided-beyond-K
   count (the fully-elided items of shape b) — the cap-withheld and elided-beyond-K
   populations with a continuation cursor positioned at the first
   withheld position, version-fenced, continuing INSIDE the same
   filtered view. Type-excluded items are reconstructable by a
   NAMED RE-READ — the same turn via a wider type list, a fresh
   view by the view-identity rule (deliberate, stated) — never by
   same-filter continuation (which re-excludes them) and never by
   cursor reuse across views (the selection fence forbids it);
   truncated-beyond-K items by the SAME named re-read at a wider
   byte bound (their bodies were served; a fresh wider-bound view
   re-mints their cursors). The
   recovery guarantee reads: every withheld item is reconstructable —
   by continuation within the same view, or by the named re-read;
   the record always says which. The
   aggregate record is deliberately exempt from the per-item rule —
   per-item elision lines for a 50-item turn are the noise this spec
   exists to remove. Elision records do not count against the item
   cap, and the response's fragments cover whole turns only.
7. **Capability gating, remote forwarding, and visible compat.** A new
   `ThreadCapabilities` bit (naming follows the `PageBefore` idiom)
   advertises windowed reads. The gate's source is the probe-cached
   thread-row `Capabilities` per lane (`localDaemonEntriesFromRoster`
   carries them) — a cache of the last probe, with the staleness rule
   stated in the docs. The skew window is bounded both ways, and the
   capability bit is the ONLY skew signal: on a windowed request
   failure against a lane whose cached bit said supported, the hub
   RE-PROBES the daemon's current capabilities and retries
   hub-compat only when the fresh probe lacks the bit (ONE retry,
   visibly marked `appliedBy: hub-compat`); a failure against a
   fresh-probed bit is a genuine parameter error and surfaces
   unchanged — a generic `InvalidParams` decode error is never a
   skew signal (no machine-readable unknown-field identity exists).
   The reverse skew (cache
   lacks the bit the daemon has) degrades to hub-compat, harmless.
   The hub never sends windowed, filter or
   bound params to a daemon that does not advertise the bit, and
   `RemoteHubSource`'s field-by-field capability rebuild FORWARDS the
   bit (a named landing zone, grounding above) so remote daemons are
   judged by their real capability, with the raw-status and masking
   policy updated and exhaustively tested. Against a daemon without the
   bit the hub degrades visibly and narrowly: it performs today's
   unfiltered read (a single legacy item page) and runs the same packer
   hub-side, marking `appliedBy: hub-compat` (three values across the
   serving arrangements: `serving-daemon`, `hub-compat`, and
   `saved-index` for the hub's own daemonless index applying the
   window).
   Hub-compat serves `{latest: N}` only, within that one page; a window
   the page cannot cover is labeled partial with its continuation;
   `since`, `before` and `turn` against such a daemon are a typed
   `window-unsupported-on-daemon` error naming the capability fix — no
   multi-page traversal promises, no invented windows. The legacy
   read's own failure modes surface unchanged, including the over-cap
   failure. The PR names the bit and the removal condition (every
   supported daemon advertises it).
8. **Follow-ups preserve the shape.** The follow-up suggestion emission
   and `SessionStatusTarget` gain the window, filter and bound fields,
   and a fetch-mode suggestion carries its `fetchCursor`
   (named landing zones, grounding above; scope 4's request grammar):
   following a suggestion from
   a windowed read re-issues the same shape, never a silent return to
   the unfiltered legacy read, and following one from a fetch re-issues
   the fetch.
9. **Freshness facts, by reference and conditionally.** Windowed
   responses carry spec 3's serving facts when that spec has landed;
   this spec defines no second vocabulary and its journey asserts those
   fields only in a build where both have landed (README rule 6).

## Non-goals

- No change to transcript storage, journaling, or the projection's write
  path.
- No change to the non-hub session-scoped `read_transcript` or doctor
  reads; their range grammar and caps are untouched.
- No full-text search; `output_match` on job refs is unrelated and stays.
- No streaming; these are bounded reads.
- No client BEHAVIOR change — but no silent schema rot either: the new
  wire fields land with regenerated AppWire types and updated
  appwire-client validators in the same change (the repo's
  generated-output gates enforce this).
- No change to the subscribe/incremental read machinery.
- No change to the 256KB cap itself; the packer makes windowed reads fit
  under it, measured.
- No reasoning-summary wire form: `summarizeReasoning` was removed in
  round 3 — the projection has no servable summary shape (grounding
  above), and callers bound reasoning by bytes or exclude it by type.
- `ThreadTurnItemsListParams` stays `ScopeUnimplemented`; this spec does
  not rebuild that API.

## Behavior contract

1. **The sweep read.** `turnWindow: {latest: 1}, itemTypes: ["messages"]`
   on a mid-turn lane returns the newest turn's agent messages and
   visible user text, each bounded by `messageBytes`; unselected items
   appear in the turn's ONE aggregate elision record (scope 6), a
   truncated item carries its marker and `fetchCursor`, and no raw
   tool output travels. The journey measures the saving,
   asserted, not eyeballed.
2. **The catch-up read.** `since: <lastProcessed>` returns exactly the
   turns the caller has not seen, in transcript order, oldest first;
   `since: <newest>` returns an empty turn list; an unresolvable id
   errors naming the frontier.
3. **The locate read.** `turn: <id>` returns exactly that turn —
   bounded like every windowed read (per-turn cap, byte bounds,
   aggregate elision, its own continuation): the two-rounds-back
   verdict in one call. The cap applies here as everywhere (scope 6).
4. **Bounded by measurement.** A windowed response's window content
   never exceeds the effective budget, and its complete encoded OUTER
   response (envelope, serving facts, follow-ups included) never
   exceeds the 256KB cap — both asserted on the encoded response; the
   response states the effective budget and which end was trimmed;
   when the budget trims, the response names served and withheld counts
   with boundary ids and carries a continuation that resumes exactly
   where the window closed, per the mode's direction (scope 5). No
   parameter combination is refused for size.
5. **Elision never lies, and fetches never drift.** Every truncated or
   budget-elided SETTLED item appears as a labeled line with its byte
   count — always, no exception; the `fetchCursor` rides that line
   only up to the shared K-line bound (scope 5). A truncated item
   beyond the bound is LABELED-CURSORLESS (served, labeled,
   recoverable by the named wider-bound re-read); an elided item
   beyond it rolls into the record's elided-beyond-K count;
   beyond that bound its class's overflow count appears in the turn's
   ONE aggregate record (scope 6); an in-flight item's line is labeled
   `inFlight` with no cursor (scope 4); type-excluded and
   cap-withheld items appear in the
   turn's ONE aggregate record with its split populations and the
   named re-read (scope 6); a follow-up fetches the item's body at
   the fenced
   version, bounded by the read cap, with a byte-range continuation
   when the body is larger; a fetch against a changed version fails
   typed, naming the new version. What was withheld is always
   reconstructable — by fetch, by in-window continuation, by the named
   re-read, or (in-flight items) by re-reading the window after the
   turn settles.
6. **Intra-turn honesty.** A turn with more items than the per-turn cap
   serves the cap's items in transcript order plus one elision record
   naming the withheld count, with a cursor that continues inside the
   turn.
7. **Older-daemon tolerance, visible and narrow.** Against a daemon
   predating the capability bit (local or remote, once the remote
   rebuild forwards the bit), `latest` windows answer through the
   hub-compat projection with `appliedBy: hub-compat`, partial when the
   page cannot cover the window, and `since`/`before`/`turn` fail the
   typed `window-unsupported-on-daemon` error; a legacy read (no window
   params) is unchanged. Never a silent fallback: when spec 3 has
   landed, its serving facts mark the read, and `appliedBy`
   distinguishes who did the work — `serving-daemon`, `hub-compat`,
   or `saved-index` for the hub's own daemonless index.
8. **Follow-ups keep the shape.** A suggestion followed from a
   messages-only bounded window re-issues a messages-only bounded
   read, asserted.
9. **Orthogonality.** Windowing, filtering, bounds and budget compose
   freely; every combination has defined behavior and mutates nothing.

## Recovery and preservation

Reads are pure. The `fetchCursor` follow-up is idempotent per (position,
version), so a caller that crashes between a windowed read and a
follow-up loses nothing by re-reading; a byte-range continuation is
idempotent per range at the same version. An item that changed between
reads fails typed rather than serving mixed versions — the caller re-reads
the window, which is always safe because reads are pure. Rotation racing
an iteration is governed by spec 3's serving facts; a stale cursor
already fails typed (`TranscriptItemCursorStale`), and the follow-up path
surfaces that rather than masking it.

## Journey test plan

Seed a session with a completed multi-item turn containing one large tool
output (over 100KB), one reasoning item, one steering frame and one system
message; a running turn with in-flight tool output; a 50-item turn; and six
earlier completed turns with known ids T1..T6. (Harness: the existing
`startHubStack`/`fakellm` stack with `RespondToolCall` seeding,
`e2e_turn_control_test.go:599-634`; Manager path `startManagerThread`,
`e2e_manager_session_test.go:684-713`; saved-index fixtures
`app_threadread_index_test.go:25-116`; agent-side schema contract via
`agent/session_tools_hub_read_contract_test.go`'s capture transport;
the operation-validation gate via `appwire/session_tools.go:126-127`.)

1. **Red-first, at both real gates.** Today the windowed call dies
   pre-dispatch at the agent schema gate (spike-observed refusal); with
   that gate extended, the hub's operation validation still rejects the
   selection ("content filters are unsupported"). The red asserts both
   refusals today; post-change the windowed call is accepted through
   both.
2. **Sweep cost:** `latest: 1` with `messages` travels no raw tool
   output; its encoded payload is asserted absolutely bounded by its
   parameters (turns × item cap × per-item bounds, then the measured
   budget) and at least an order of magnitude below the unfiltered
   read of the same turn.
3. **Catch-up:** `since: T4` returns exactly T5..newest in transcript
   order, oldest first (the boundary is exclusive); `since: <newest>`
   returns an empty list; an unknown id errors naming the frontier.
4. **Locate:** `turn: T5` returns exactly T5 in one call, at the
   default per-turn cap; a T5 larger than the cap serves the cap's
   items plus the aggregate record, and its continuation finishes the
   turn.
5. **Budget trim:** a `since` window whose content exceeds the budget
   serves the OLDEST turns that fit, names served and withheld counts
   with boundary ids, and its continuation serves the remainder forward;
   a caller-set budget below the floor is served at the floor with the
   effective budget stated; the response never exceeds the budget,
   asserted on encoded bytes.
6. **Intra-turn cap:** the 50-item turn at the default cap serves 10
   items plus one aggregate elision record; its continuation cursor
   continues inside the turn (the next 10, and so on to exhaustion).
7. **Follow-up fidelity:** a truncated message's and an elided tool
   output's `fetchCursor` each fetch the item's full body, equal to the
   unfiltered read's bytes when under the cap; a body over the cap
   serves a byte range with a continuation; an item that completed and
   grew between window and fetch fails the typed `item-changed` error
   naming the new version. The version-0 rule's own assertions: a
   truncated IN-FLIGHT item carries no `fetchCursor` (labeled
   `inFlight`); once the turn settles, a re-read of the same window
   mints the cursor at the settled version and the fetch succeeds —
   the growth case above is exactly this fence. The range contract's
   own assertions: a
   MULTI-FIELD oversized item (large `Output` AND `ArgumentsJSON` AND
   `Raw` together) fetches through ranges whose concatenation is
   byte-identical to the unfiltered read's encoding of that item
   (asserted exactly, not by length); a range boundary forced mid-UTF-8
   and mid-JSON-token decodes and reassembles cleanly (base64 carries
     arbitrary bytes); the reassembled encoding carries the metadata
     exactly once; the REMOTE-origin case — an item whose actors and
     image routes the ordinary read translates (scope 4's
     `remote-source` origin) fetches hub-side through ranges whose
     concatenation is byte-identical to the CONTROLLER's unfiltered
     encoding of that item, translated actor names and image routes
     included, with offsets and `totalLength` measured against that
     same encoding, and an item beyond the inter-hub read's reach
     fails the typed `fetch-unavailable-remote` refusal; and the
     MAX-CASE envelope fixture — TWO legs, both
     at the real version-and-origin-bearing cursor encoding: a
     window whose
     every selected item is budget-elided with K elision lines, and
     the per-turn-cap-maximum leg (itemLimit
     at its max, all 40 served items truncated and labeled, K
     truncation cursors,
     the labeled-cursorless remainder in the aggregate record), each plus longest
     serving-fact ids, widest follow-up
     target, one aggregate record and one continuation cursor — MEASURES
     each leg's complete encoded size, asserts the floor covers them
     (floor ≥
     either leg's measured worst case, scope 5's obligation), and
     that the clamp's
     margin holds on them.
8. **Composition:** `since: T4` with `itemTypes: ["messages"]` returns
   each turn with only message items, labeled elision lines present
   for the rest — and the aggregate record's per-type counts name
   them, with a wider-type-list re-read of the same turn retrieving
   the excluded items (a fresh view, deliberately), asserted.
9. **Follow-up shape:** following the suggestion emitted by a
   messages-only bounded window re-issues the same shape (asserted:
   the follow-up call carries the window, types and bounds).
10. **Legacy tolerance and probe staleness:** against a fixture daemon
    predating the capability bit, `latest: 2` answers through hub-compat
    with `appliedBy: hub-compat` (partial-labeled when the page cannot
    cover it), `since`/`turn` fail the typed unsupported error, and a
    legacy read (no window params) is unchanged. Settled old-daemon
    truncation, end to end: a hub-compat window that elides a large
    SETTLED item mints its `fetchCursor` with origin `hub-compat`;
    the follow-up fetch resolves HUB-SIDE — no fetch params are ever
    sent to the old daemon — and the fetched bytes equal the
    unfiltered legacy read's encoding of that item, the fetch fence
    (contract 5) holding on the compat arrangement. Scroll-out, both
    directions of the compat fetch's bound: the same window, then
    exactly 40 newer
    items arrive before the fetch — `Before` is exclusive and a
    backward page serves the newest strictly-older items
    (`internal/appitempaging/page.go:201-214`), so ONE page-back
    reaches the former newest page and the fetch SUCCEEDS, the
    fence holding (asserted) — on a LOCAL compat peer, stated:
    the remote rebuild masks `PageBefore` unconditionally
    (`cmd/evener-hub/internal/appsource/remote_hub_refs.go:134-158`),
    so a remote peer is always pre-`Before` and takes the
    re-derivation path. Then the cap-exceeding fixture — arrivals
    at the traversal cap constant's own cap+1 pages, the leg
    derived from the chosen bound — the target position
    unreached within it — fails the typed
    `item-unreachable-on-compat` naming the recovery, and a re-read of
    the window recovers, serving fresh state with fresh cursors; on a
    pre-`Before` compat peer the scroll-out fetch (newest-page
    re-derivation only) fails the same typed error with the same
    recovery.
    Probe staleness, both
    directions: a probe cache that names the bit while the daemon lacks
    it answers via the strict-decode rejection and the ONE automatic
    hub-compat retry (marked); a cache that lacks the bit the daemon
    has serves hub-compat, harmless.
11. **Freshness (conditional):** in a build where spec 3 has landed,
    every windowed response carries spec 3's serving facts, and pages
    of the same view agree with its frontier.

## Dependencies and interfaces

- Consumes spec 3's serving facts by reference, asserted conditionally
  (README rule 6); shares the read path; either lands first.
- Owns the exact-type predicate, the `fetchCursor` mechanism
  (single-item, identity- and version-fenced, plus byte-range
  continuation), the turn-window machinery on both serving paths, and
  the capability bit; specs 1 and 3 do not depend on any of it.
- Extends the `session_tools_hub.go` schema block adjacent to 5659's and
  5641's; rebases under both.
- Landing zones, all named in grounding: the EXISTING packer to extend
  (`cmd/evener-hub/app_item_page_fit.go`, `packItemCandidates`); the
  agent-side schema gate
  (`session_tools_hub.go:70-89`, hub object at :705-729); the hub's
  operation validation (`appwire/session_tools.go:126-127`); the
  `ThreadTurnsListParams`→`ThreadReadParams` conversion preserving the
  new selectors (`cmd/evener-hub/session_reads.go:56-62`); the live
  handlers (`server/appwire_read_handlers.go`); the saved path
  (`cmd/evener-hub/app_threadread.go` windows); the continuations
  `listItemTurns` (`cmd/evener-hub/app_rpc.go:412`) and
  `pastThreadTurnsList` (`app_threadread.go:216`); the follow-up
  emission (`cmd/evener-hub/session_tools.go:253-260`) and
  `SessionStatusTarget` (`appwire/session_tools.go:393-406`); the owned
  cursor extensions — the after-bound (forward) form, the item
  `Version` fence and the serving-arrangement ORIGIN
  (`internal/appitempaging/cursor.go:25-54`) — and the
  windowed view identity extending the selection-scoped incarnation
  (`internal/apphistory/paging.go:24-45`); the `RemoteHubSource`
  capability rebuild
  (`cmd/evener-hub/internal/appsource/remote_hub_refs.go:134-139,185-203`);
  the new `ThreadCapabilities` bit (`appwire/types_thread.go:618-624`);
  and the regenerated AppWire types plus appwire-client validators for
  every new wire field.

## Acceptance criteria

- The journey passes red-first against today's two gates; the measured
  sweep-read payload on the seeded session is asserted at an order of
  magnitude below the unfiltered read; no windowed response ever
  exceeds its effective budget, asserted on encoded bytes.
- `docs/automation.md`'s read contract gains the windowing, filtering,
  bounds, budget, fetch, follow-up and compatibility rules (extending
  the existing paging lines); `docs/product/history-content.md` — the
  owning guide for content selection and paging (grounding above) —
  gains the windowed-read selection, elision, fetch and continuation
  contract in the same change, per the product docs' same-change rule;
  the Manager prompt template
  (`agent/testdata/messagingprompt/manager-on.md`) teaches the cheap
  sweep shape.
- The PR description names the new capability bit, the older-daemon
  direction (narrow visible hub-compat; typed unsupported for
  non-latest windows) and the removal condition (every supported
  daemon advertises the bit).

## Alternatives considered

- **Store-side per-turn digests.** Rejected for v1: new durable state for a
  read-shape problem; preview bounds plus the measured budget cover the
  byte cost.
- **Arithmetic budget accounting over selected fields.** Falsified by the
  round-2 spike: 40 items × 8,192 default bytes = 327,680, over both the
  192KB budget and the 256KB cap, before metadata, images and JSON
  escaping. Field arithmetic cannot pay this guarantee; the existing
  packer, extended to measure at Manager scale, can, and does. Round 3
  added the other
  edge: a caller-set budget below one elision record cannot satisfy
  "never exceed" and "never refuse" — the floor resolves it.
- **Filtering through the wire's category selection.** Rejected after
  round 3: the categories are coarser than types (agentMessage,
  userMessage and user steering share one bucket), and `Selection.Item`
  drops what it excludes, so it cannot report elisions. The exact-type
  predicate is owned machinery applied by the windowed handler with
  elision computed from the full list.
- **A servable reasoning summary.** Rejected in round 3: the projection
  has no summary wire form (summaries are a text-empty fallback);
  callers exclude reasoning or bound it.
- **Changing defaults to elide.** Rejected: silently reinterpreting
  existing call shapes breaks every current Manager.
- **A separate windowed-read tool.** Rejected: one door;
  `read_transcript` owns transcript reads in the Manager surface.
- **Raising the 256KB cap.** Rejected: making over-reads cheaper is the
  wrong direction; the packer makes the cap unreachable for windowed
  reads instead.
- **Refusal at the tool edge for oversized windows.** Rejected: refusal
  reintroduces "no data returned" for exactly the turn-rich lanes this
  spec exists for; the packer trims the window and says what it
  withheld, which is always recoverable by continuation.
- **Multi-page hub-compat traversal for `since`/`before`.** Rejected in
  round 3: each legacy page drags full unfiltered payloads — the cost
  this spec exists to avoid — and the resolution bound would be
  arbitrary. The narrow compat (latest-only, typed unsupported
  otherwise) is honest and bounded.
- **Reusing `ThreadTurnItemsListParams`.** Rejected: it is
  `ScopeUnimplemented` (`appwire/protocol.go:178`); building the Manager
  surface on an unimplemented scope rebuilds that API under pressure.
  The Manager read extends the implemented `ThreadTurnsList`/`ThreadRead`
  path instead.
