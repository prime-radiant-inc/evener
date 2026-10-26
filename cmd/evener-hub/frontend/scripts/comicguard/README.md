# Comic Chat browser guard

From the repository root:

```sh
COMICGUARD_EVIDENCE_DIR="$EVENER_SCRATCH_DIR/task10-browser-fallback" \
go test -tags browserguard ./cmd/evener-hub -run '^TestComicChatBrowser$' -count=1 -v
```

Rebuild the production frontend with `npm run build` from
`cmd/evener-hub/frontend` after inspecting `scripts/clean-dist.mjs`. The guard
starts isolated real daemons and an authenticated production hub. Only the external
LLM provider is scripted. Delegation, shell calls, transcript production, AppWire,
shared stores, readers, widgets and Chrome use real code. No store/model injection
or invented child refs are used. The helper and browser are test-owned and cleaned
up; the running preview and main hub are untouched. Skips and timeouts are failures.
The fixture runs the production roster watcher as well as its initial refresh.
A daemonless child response is not proof that its live subscription attached.
The complete browser journey retains the original two-minute wrapper deadline.

The root journey covers literal source, bounded history reads, available and failed
font states, responsive layout, plain/comic switching, durable provider failure,
reconnect/reload, native no-scroll click, and away/end behavior during streaming.
The provider failure produces a real user item and system error item. Itemless turn
errors remain component-test evidence, not this runtime scenario.

The additive child journey asks production `delegate` tools to create an actual
child and grandchild. Available reasoning is emitted by the scripted provider,
not inferred from an answer. The child performs real shell rounds to create older
history. The driver waits for canonical child activity before searching the
virtual flow, expands the two levels, measures qualified source coordinates at
1360, 760, 600, 390 and 320 CSS pixels, reloads, and uses the explicit full-page
route and Back. It releases held child stream deltas while reading a grandchild,
checks native Enter expansion and collapse focus, and switches plain/comic views.
It loads at least three child history pages, relocates the existing expanded strip
when its causal event arrives, and checks a nonzero later-page source offset through
disconnect, reconnect, reload and Back. Browser RPC capture rejects `turn/start`
and `thread/resume` from reads.
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

For authorized personal font verification add `COMICGUARD_PRIVATE_FONTS=1` and use
a separate evidence directory. Both ignored local font files must already exist.
The guard checks actual rendered regular and bold faces through Chrome's font
inspection, not CSS names alone. It never downloads fonts. Do not publish its
font-bearing build. The default run blocks the private font URLs.

Unavailable remote children and native touch inertia remain shared-adapter or
component evidence. Human questions and sandbox approvals remain interactive-root-only.
The invalid delegated `ask_user` producer is absent; no child pending state is
injected. This guard proves actual child status and navigation, reader demand
retirement and cancellation ordering. Root question/approval journeys belong to
their interaction tasks, not inline subchat delivery.
