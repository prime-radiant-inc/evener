# Comic Chat browser guard

From the repository root:

```sh
make build-web
COMICGUARD_EVIDENCE_DIR="$EVENER_SCRATCH_DIR/comic-refinements-bundled" \
go test -tags browserguard ./cmd/evener-hub -run '^TestComicChatBrowser$' -count=1 -v
EVENER_COMIC_BLOCK_FONTS=1 \
COMICGUARD_EVIDENCE_DIR="$EVENER_SCRATCH_DIR/comic-refinements-fallback" \
go test -tags browserguard ./cmd/evener-hub -run '^TestComicChatBrowser$' -count=1 -v
```

Chrome/Chromium, Node, Go and matching frontend dependencies must already be
installed. `make test-web-browser` registers this as `comicguard`, behind a
successful production build, and waits for fixture cleanup on interruption.
The driver entry point is
`node scripts/comicguard/run.mjs --fixture <absolute-json-path>` from the frontend
directory. Go writes that fixture with authenticated `baseURL`, qualified `refs`,
`evidenceDir`, `requireFonts`, independent source literals and producer
checkpoint paths. Do not publish or retain its authentication-bearing input.
`COMICGUARD_EVIDENCE_DIR` selects the output directory; the default lives under
the isolated test artifact directory. Keep only evidence linked by a report.

Rebuild the production frontend with `npm run build` from
`cmd/evener-hub/frontend` after inspecting `scripts/clean-dist.mjs`. The guard
starts isolated real daemons and an authenticated production hub. Only the external
LLM provider is scripted. Delegation, shell calls, transcript production, AppWire,
shared stores, readers, widgets and Chrome use real code. No store/model injection
or invented child refs are used. The helper and browser are test-owned and cleaned
up; the running preview and main hub are untouched. Skips and timeouts are failures.
The fixture runs the production roster watcher as well as its initial refresh.
A daemonless child response is not proof that its live subscription attached.
The complete journey awaits browser-process completion under the Go test's
deadline, reserving time to join the browser and stop its four possible daemon
helpers. Cancellation first sends SIGTERM so the driver can close Chrome, then
uses the shared bounded forced-stop fallback. Browser condition deadlines remain
independent tripwires; no elapsed-time check substitutes for completion.

For the independently runnable remote-navigation diagnostic only:

```sh
COMICGUARD_EVIDENCE_DIR="$EVENER_SCRATCH_DIR/comic-remote-focused" \
go test -tags browserguard ./cmd/evener-hub -run '^TestComicRemoteNavigationBrowser$' -count=1 -v
```

This uses the same real fixture and roster/remote journeys, with an explicit
`remote-navigation` fixture scenario. It produces 101 real roster leaves and
checks all their durable inputs and answers. Its activity pages are 50/50/1,
because it does not run the earlier child journey that gives the full guard its
102nd heading and 50/50/2 pages. It shares the test-owned lifetime and 20-second
lookup bounds. `remote-route-search.json` records the selected strip identity,
each lookup's mounted headings and scroll geometry. This is not a full gate or
evidence for restoration, recovery, root actions or font modes. The registered
`TestComicChatBrowser` remains the complete default. Use
`EVENER_COMIC_BLOCK_FONTS=1` for this focused diagnostic.

For the independently runnable recovery qualification only:

```sh
COMICGUARD_EVIDENCE_DIR="$EVENER_SCRATCH_DIR/comic-recovery-focused" \
go test -tags browserguard ./cmd/evener-hub -run '^TestComicRecoveryBrowser$' -count=1 -v
```

The explicit `recovery` scenario reuses the real fixture, native transport-loss
and same/changed-identity resume journeys. It runs authentication and an independent
literal seed, but skips unrelated reader, roster, authorship and interaction
journeys. The default still runs all of them. Both modes share the same durable
checks: one seed input, one input at each loss boundary, one same-identity input,
one changed-identity input, two real HubSpawner launches and owned-helper shutdown.
The test-owned lifetime and 20-second driver waits also apply here.
Use `EVENER_COMIC_BLOCK_FONTS=1` for this focused diagnostic.

Changed-identity recovery acknowledges an already-exited owned process, then uses
the normal Settings rail to replace the conversation pane and native Back.
`resume-fenced.json` records completed
exact-ref unsubscribe before Back and a fresh non-foldable recovery read on the
original document/socket. These wire observations alone do not prove acceptance.
`resume-held.json` records the actual client resume request and authentic response
bytes held at the native WebSocket boundary, the old editor clearing after durable
enqueue, and zero premature starts. Send drives resume asynchronously, so an
unchanged busy draft would be the wrong UI-progress expectation. The driver releases those bytes once without changing
them. `resume-changed.json` requires exactly one later start, the returned instance
fence, a matching applied receipt, the destination route and its newer draft.
`wire.jsonl` retains raw connection-scoped traffic. No product debug API or issued
generation is treated as acceptance. This is not live-process force-stop coverage
or a full restoration/font qualification.

The root journey covers literal source, bounded history reads, available and failed
font states, responsive layout, plain/comic switching, durable provider failure,
reconnect/reload, native no-scroll click, and away/end behavior during streaming.
The provider failure produces a real user item and system error item. Itemless turn
errors remain component-test evidence, not this runtime scenario.

### Formatted source and recorded creation

`comic_browser_test.go` authors the original prose → code → table → prose fixture.
The raw source remains unchanged. The guard checks semantic headings, emphasis,
ordered-list starts, quotations, safe links, complete code whitespace and table
cells. Technical inserts must fit their 40rem/available-width bound, contain no
character stage, retain their full height and expose usable horizontal scrolling.
The ordinary-panel checks retain 1360/850/760/600/390/320 widths and an actual
paired row at 850px. The formatted journey also sets actual mobile viewports at
390 and 320px, not merely narrow desktop containers.

The `comic-formatted` external-provider role pauses at an unfinished fence and a
partial table before completing them. It uses the real daemon's transcript and
subscription path. `formatted-stream.json` retains all blocks at the partial-code,
partial-table, complete-text and canonical-completed-turn checkpoints. Each checks
the entire independently authored source range, exact prose, code and cells, and
one unchanged source identity. The settled sample is taken after the canonical
item and its completed turn arrive. No product store, parser or source-binding
cache supplies the expected answer.

`inlineFixtureGlyphs` handles only the known inline syntax in the history fixture.
The formatted fixture instead locates literal text directly in DOM Text nodes and
measures native `Range` rectangles. Original UTF16 offsets come from the fixture,
including hidden emphasis delimiters and collapsed whitespace. `formatted.json`
records node text, local offsets, glyphs, line rectangles, viewport dimensions,
virtual mounting and insert geometry. Observation waits for leading and trailing
source content; a partial virtual window cannot satisfy the semantic validator.
The table glyph comes from its second body row. Long cells retain their full text
and horizontal overflow; this does not assert a multiline-cell Markdown syntax
or force intrinsic-width cells to wrap. Code retains independently measured
multiline whitespace and geometry.
`formatted-recovery.json` and `formatted-cold-history.json` check a technical source
point through resize, presentation switches, reload, reconnect and distinct real
history pages without input. `formatted-font.json` records the technical point's
font readiness or blocked-font result after reload. The existing formatted-prose,
delayed-font, child cached/partial/cold recovery and error-presence checks remain
mandatory.

`creation-chronology.json` ties a strip to real delegate creation output between
parent entries. `creation-chronology-completed.json` rechecks those exact entry IDs
and strip key after completion and the expanded-reader checks. The disclosure is
then collapsed solely to mount both original neighbors in one observation.
`late-creation-position.json` records the same independently located source glyph
before and after the final origin-revealing history page. Only that authentic
WebSocket reply is held, then released unchanged after pinning the source. No
scroll, reveal or anchor recapture intervenes before the post-release measurement.
The pending sample must see the sibling unplaced-group marker. The applied sample
must see the same strip between both source neighbors of the recorded creation
output, using its receipt to bind the child and its wire IDs to bind the neighbors.
The observer follows sibling virtual rows in the owning reader's path, excluding
nested readers, rather than looking for a group-label ancestor of the strip.
`late-creation-relocation.json` also checks that late launch placement retains the
expanded identity without unsubscribe/re-read. `mounted-collapse.json`
checks a mounted collapse's successful unsubscribe while its real sibling stays
expanded and is neither unsubscribed nor re-read.

After those checks, the real provider's root and inline-child Markdown file links
open actual fixture files on the phone host. `document-links-phone.json` records
the served source-session URLs, the already-settled root location before activation,
successful source hydration after reconnect, and the same containing pane before
activation and after native Back. Reconnect need not issue a new location read.
While each
document is focused its actual file content must be visible and the Comic reader
must be unmounted. These files share the fixture working directory; differing
root/child cwd and artifacts roots, modified clicks, unsafe links and unchanged
source glyph bindings are covered by mounted renderer tests.

Run the direct observation validators with
`node --test cmd/evener-hub/frontend/scripts/comicguard.oracle.test.mjs cmd/evener-hub/frontend/scripts/comicguard.test.mjs` from the
repository root. Their negative cases reject missing semantics, wrong source
order, whitespace loss, unsafe links, clipping, unreadable overflow, replacement
stream identities, missing prefixes, duplicate code or whole-source identities,
completion-time loss, shifted relocation glyphs, pending sibling-group strips,
wrong launch neighbors, moved launch strips and sibling
retirement. They supplement, not replace, the
complete browser journeys and `make test-web`.

Entry uses the normal authenticated `/s/<ref>` URL and trusted clicks through
Session actions → Verbosity → Presentation → Comic. Standard/Comic handoffs
start on the existing empty author daemon before opening the root history fixture.
Browser fetch authenticates against the real token endpoint without executing
its home redirect, which could open and prefetch that root under Standard mode.
The root handoff follows its original paging/failure/retry proof for the same reason.
HTTP cache is disabled for the delayed-font root navigation, not the IndexedDB
transcript cache used by cached/partial/cold child proofs. Handoffs
require the same URL, document, pane id, session ref, structured editor contents
and one open RPC socket. Desktop and mobile presentation choices are independent.
Comic's internal plain mode is a separate accessibility option, not Standard.
View, Scene and expression changes open the real Appearance popup at every
viewport size, require viewport containment, and dismiss with Escape and focus
return. Plain/comic changes in the nested child journey use the same control.
`presentation-entry.json` records the entry; source-anchor measurements cover
switching away and back with an expanded nested child. The standalone Comic URL,
picker pagination and its Conversations action were removed from the product.
Their assertions are retired, not replaced with invented picker fixtures. Shared
root-history paging, roster paging and final reader-retirement checks remain.
The later-history width changes cross the responsive pane remount and retain the
same source while the shared repository restores locally reclaimed bodies.

The additive child journey asks production `delegate` tools to create an actual
child and grandchild. Available reasoning is emitted by the scripted provider,
not inferred from an answer. The child performs real shell rounds to create older
history. The driver waits for canonical child activity before searching the
virtual flow, expands the two levels, measures qualified source coordinates at
1360, 760, 600, 390 and 320 CSS pixels, reloads, and uses the explicit full-page
route and Back. It releases held child stream deltas while reading a grandchild,
checks native Enter expansion and collapse focus, and switches plain/comic views.
Back retains the direct-page secondary pane. The guard closes that pane through
its native tab close control, records its owner retirement, and keeps the inline
grandchild source fixed before later asserting no unopened descendant reload.
It loads at least three child history pages, relocates the existing expanded strip
when its causal event arrives, and checks a nonzero later-page source offset through
disconnect, reconnect, reload and Back. Browser RPC capture rejects `turn/start`
and `thread/resume` from reads.
Cached reload must also recover after the current document's full child hydration,
before any input or cache eviction. A successful correlated `thread/read` reply
alone is insufficient: the old cached DOM can still show the source before the
fresh partial window is applied. An ensuing same-connection older-page request
witnesses application; the guard awaits successful replies for every issued child
read/page and checks the same saved identity, offset and viewport placement again.
Each geometry sample follows a frozen wire snapshot and is rejected if child
read/page traffic or connection lifetime changes during that observation. The
ordinary condition wait samples again, never attaching newer replies to old DOM.
Disallowed input is latched from document initialization, including touch events;
the bounded event-detail buffer cannot erase it.
`later-child-cached-recovery.json` records those correlated requests/replies, the
unchanged document, input observations and final geometry. This proves post-refresh
recovery, not continuous placement or private store/layout settlement invariants.
After the cached later-child reload, two additional reloads evict only that child's
real IndexedDB history: first retaining the newest authentic read fragment, then
removing its history completely. Each must recover the same literal source offset
within 2px through at least two distinct successful network page replies.
`later-child-partial-cache.json` and `later-child-cold-cache.json` record the cache
boundary and connection-correlated requests and replies. Cache eviction is an
explicit browser persistence boundary, not store injection or a substituted RPC.
The control-file producer inputs are separate from browser read traffic.
The child is named `comic-named-parent` and sends a real `delegate_send` instruction
to its grandchild. The grandchild's direct page must attribute that instruction
to the same named parent, not its task description. `named-parent.json` records
that check and the parent reader's Back restoration remains measured.

An independent fresh root runs two explicit `fork_context: true` delegations.
The nested leaf inherits human input, root and child answers/thoughts, and the
child's actual `communicate` update. Its original source authors and inheritance
labels must match in nested inline panels and on the direct page through history
paging, reload and reconnect. The driver first observes all six sources in their
original root/child readers, checks their independent qualified actor identities,
then requires inherited labels to preserve those identities with one additional
indicator. It does not assert production label wording. `authorship.json` retains
these baselines, independent source literals and DOM observations;
`authorship-wire.json` retains canonical items,
pages and read-only RPCs. The Go oracle reads the leaf's actual durable transcript
and checks each original author exactly once in `authorship-durable.json`.
It does not derive expected authors from the Comic projection or SDK output.
The collector uses the leaf's inline history control when nested and its root
history control on the direct page. `authorship-search.json` retains the latest
bounded scan, including the original-source observations at a failed transition.

An additional producer creates 101 actual leaf delegates, including two named
`comic-same-name`. The browser loads the shared owner's 50/50/2-row activity
pages, visits all 102 parent headings with bounded virtual mounts, and rejects
transcript reads for collapsed leaves. The Go check independently verifies each
leaf's exact durable input and answer. `activity-roster.json` retains the real
pages, identities, mounted bound, RPCs and per-step scan geometry. Its 160-step
scan reverses at boundaries until all 102 headings have actually been observed;
recorded commanded scroll targets and subsequent offsets expose repositioning
between frames. Wire rows never substitute for DOM coverage.
`named-direct.json` proves a named
child's full-page identity before and after refresh.
`named-route-search.json` records its bounded bidirectional heading search,
which does not assume the coverage scan finished at the bottom.

A second authenticated hub connection exposes the same daemon through the
production `comic-remote` source. `remote-identity.json` compares its qualified
heading with the local identity and checks inline/direct-child persona parity.
This exercises real AppWire remote translation and subscription routing, not SSH
attachment or a second physical host. No activity responses are substituted.
Both remote parent and child require successful connection/request-correlated
reads with matching qualified response identities. The web-wide Live owner may
also read local top-level sessions, but only after a successful same-connection
Live navigation reply listed that exact ref as a live session. Missing, late,
failed or ineligible discovery cannot authorize a local read. The remote child's
local counterpart is always forbidden, even if discovery incorrectly lists it.
`turn/start` and `thread/resume` remain forbidden throughout the measured slice,
including requests from the preceding document.

Evidence includes `measurements.json`, root screenshots, `children.json`, nested
screenshots, `child-canonical.json`, `child-keyboard.json`, `child-reads.json` and
`children-durable.json`. The Go harness independently reads actual child transcript
files and checks one exact thought/answer per child and 45 exact history messages.
`root-reads.json` retains root read/page requests before the paging assertion.
Failures in the child
journey additionally capture `child-failure.json` and a screenshot. Numeric
measurements use a 2px source-point tolerance and bound mounted source rows. Cloud
text must fit inside its balloon; the deliberately protruding decorative trail is
not text overflow. A completed run, not an artifact left by an earlier attempt,
is the proof that all its assertions ran.
`reload-history-reply.json` and `activity-continuation-replies.json` pair replies
with their request and connection after the intended reload or click boundary.
On failure, `failure-wire.json` retains the journey's frames and connection boundaries.

The default run checks the bundled historical Comic Sans font through Chrome's
actual glyph inspection, including regular and browser-synthesized bold text.
It holds and releases the real font request to exercise late-font reflow. No font
download or private install is needed. Repeat with `EVENER_COMIC_BLOCK_FONTS=1`
and a separate evidence directory to verify fallback and usable controls when
the bundled font request fails.

Unavailable remote children and native touch inertia remain shared-adapter or
component evidence. Human questions and sandbox approvals remain interactive-root-only.
The invalid delegated `ask_user` producer is absent; no child pending state is
injected. This guard proves actual child status and navigation, reader demand
retirement and cancellation ordering.

Root interactions use actual `ask_user` and sandbox tool calls. The runtime sees
the selected answers, an explicit approval and an explicit denial. A dropped
approval send enables explicit retry; a held real response across a route switch
does not mutate the second root or replay the decision. Durable checks count each
exchange and inspect the approved files and denied file's absence.

Pending questions, answered questions and approvals are exercised at 1360, 760,
600, 390 and 320 CSS pixels with reduced motion and a 34px emulated bottom inset.
Native Tab traversal and center-point hit-testing reach the note and footer, not
merely the first visible choice. This is browser viewport and safe-area emulation,
not physical-device or operating-system keyboard testing. `interactions-*-viewports.json`
and matching screenshots retain geometry and focus evidence. The default run
inspects actual question/tab, composer and approval glyphs in `interaction-fonts.json`,
`composer-action-fonts.json` and `approval-actions-fonts.json`.

`send-recovery.json` captures native transport loss before delivery and after an
actual applied receipt has reached Chrome but before the application receives it.
No receipt or model state is invented. Reload/reconnect resolves the same mutation
identity, sometimes from canonical history rather than retransmission, while a
newer draft survives Standard/Comic handoffs. Go independently counts one
recorded input per fixture in the runtime transcript. This scenario is transport
recovery, not proof of daemon identity-changing resume.
