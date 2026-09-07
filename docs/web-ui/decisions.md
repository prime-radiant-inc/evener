# Web UI: the design decisions, and whether they are still true

Status: **current**. This is the blessed design's provenance — what we chose,
out of what alternatives, and whether the code does it today.

## Directory selection (2026-09-05)

Jesse approved the session-start directory dialog and its use for every web
directory picker. The authoritative [directory-selection contract](design-system.md#directory-selection-one-shared-interaction)
requires shared widgets, explicit confirmation, cancellation that preserves the
committed value, and inline child-directory creation on desktop and mobile.
Older immediate-commit directory popovers are retired. File selection retains its
file-specific behavior.

It exists because the record was scattered. Between 2026-06 and 2026-07 a
visual-brainstorming pass produced 23 mockups, each presenting four labelled
alternatives, and we picked winners and shipped them. The winners were
recorded in commit subjects and nowhere else; eight of the 23 mockups carry
an in-file `Recommendation` block, and exactly one entry in `TARGETS.md` was
ever annotated with what shipped.

Then the whole app was rewritten. The `renderer.js` / `style.css` hub those
decisions shipped into was deleted and rebuilt as the React SPA under
`cmd/evener-hub/frontend/src`. **The rewrite did not port the decisions.** It
authored a fresh visual direction from
`docs/superpowers/plans/2026-07-20-webui-rewrite-wave2-design-system.md`
§Direction, and nobody diffed the two. `design-system.md` says so plainly: the
wave-2 library is "a from-scratch visual system, not a reskin of the old one."

So this document does two jobs. It records what we decided, so
`history/mockups/` can be deleted without losing the decisions. And it records
what survived, so the gap between the two is a work list instead of a feeling.

## How to read a verdict

- **LIVE** — the rule holds in the React app today, with a citation.
- **CHANGED** — something related shipped, but the rule differs. The note says
  how, and whether the difference was reasoned.
- **ABSENT** — no trace in the current code.

A CHANGED or ABSENT verdict is not automatically a defect. Several are
documented, reasoned departures where the implementer hit something the
mockup could not have known — those are marked **by design**, and they are
closed questions, not work. The ones with no such reasoning are the work list.

## Where the primary sources are

- **The mockups themselves**: `history/mockups/*.html`, 23 files, four
  alternatives each. Open in a browser; they are self-contained.
- **The golden reference**: `history/examples/01-golden-live-session.html` —
  the assembled screen the mockups were built against, plus
  `02-hard-cases.html` for error / long-output / heavy-fan-out states.
- **The three explored directions**:
  `history/examples/direction-{a,b,c}-*.html`.
- **The brief every direction had to render**:
  `history/examples/_mockup-brief.md`. Its eight non-negotiable principles are
  reproduced below, because they are the actual design law and outlived the
  mockups that tested them.
- **Which alternative shipped**: the commit subjects cited in the tables
  below. `git log --oneline --all -- docs/web-ui` finds the rest.

## The eight principles

Reproduced from `_mockup-brief.md`. Every direction and every mockup had to
honour these; they are upstream of any individual A/B/C/D choice, and none of
them has been retired.

1. **Conversation-first.** User and assistant prose are the loudest, most
   readable thing. Tool calls are visually subordinate.
2. **Tool calls collapse once scrolled past.** A run of finished calls folds
   to one summary line.
3. **Subagents are first-class, aggregated.** One inline panel per turn shows
   each child's lifecycle and links into its own transcript.
4. **Status lives on a left rail / glyph**, colour-coded and scannable down
   the column.
5. **Steering — a human message mid-run — is LOUD.** The highest-signal
   interruption, unmissable and clearly human-authored.
6. **Liveness is visible.** Elapsed time at the streaming tail, "waiting on N
   subagents" when blocked, and a jump-to-latest pill when scrolled up.
7. **Mono only for machine text** — paths, commands, model IDs, counts, code.
   Sans for everything human, including every chrome label.
8. **The navigator declutters by recency and kind**, de-weighting subagents
   and bucketing disposable test sessions.

Two of these are not in the state the list implies, and a reader judging whether
a behaviour is intentional needs to know that:

- **Principle 5 was overridden by our own later decision.** `TARGETS.md` item 3
  directs a "neutral (not amber) steer tick", and all four alternatives in
  mockup 03 render a steer at the dim hairline tone — never louder than an
  ordinary prompt. That shipped. So "steering is LOUD, unmissable" is not the
  live rule; the mockup replaced it, and no document said so until this one.
  (Steering rendering is being reworked separately; this note is about the
  record, not that work.)
- **Half of principle 6 has no implementation.** `transcript/flow/liveness.ts:describeLiveness`
  produces only "Quiet ~Ns" or "May be stalled — no updates for Ns", and nothing
  under `panes/session/` mentions waiting on subagents at all. The elapsed clock
  and the jump-to-latest pill are live; **"waiting on N subagents" when the main
  agent is blocked is absent** — which is precisely the state a reader most needs
  explained, since from outside it is indistinguishable from a stall.

## Which direction won

The written record says there was no winner:
`history/ux-and-implementation-plan.md` says three directions were explored
and "converged on a **restrained synthesis** (the golden example) after
feedback."

The artifact points at **direction A (refined terminal)** with the excess
stripped out. The evidence is uneven, so take it in order of strength:

- **Colour is decisive.** `#7aa2f7` (accent) and `#f7768e` (error) are direction
  A's verbatim; the amber is one step off (`#e2b06a` vs `#e0af68`); the darkest
  surface `#0a0c14` matches exactly. Directions B and C share **zero** hex
  values with the golden.
- **Type is suggestive, not conclusive.** The golden's sans is Hanken Grotesk at
  the same four weights direction A requests — but direction B requests that
  identical string too, layering a serif over it, so the sans face alone
  distinguishes nothing. The mono is JetBrains Mono, which A and C both use,
  and the golden asks for one fewer weight than A does. Reported here corrected:
  an earlier draft of this document called the fonts byte-identical to A's and
  unshared with B and C. Neither is true.

What the golden *dropped* from direction A is exactly the plan's own bullet
list: A's subagent-purple, green and cyan are gone, collapsing the palette to
four meanings. Recorded here as inference from the artifacts, not as a decision
anyone wrote down.

## Decision inventory

Verdicts were established by reading the mockup for the rule it claims, then
finding the code that enacts or contradicts it — not by matching vocabulary.

One caution for anyone extending this document. Several mockups declare rules
**held constant across all four alternatives** — a "Held constant" block, or a
rule stated once in the shared CSS above the A/B/C/D overrides. Those are not
any one alternative's contribution, and an earlier draft of this inventory
credited alternative D for three of them (topics 09, 10 and 14). Corrected
below. When reading a mockup, check the shared block before attributing a rule
to a letter — and note that a held-constant rule is the *strongest* kind of
decision, since every alternative agreed on it.

<!-- decision-tables:begin -->

### Foundation

**01 · Colour & status system** — chose A (strict four-colour remap) + C
(contrast-fixed ramp) + B's glyph contract. Shipped `36989fbdc`.

| Part | Verdict | Where it stands |
| --- | --- | --- |
| A — four meanings, done recedes, colour is scarce | LIVE | `styles/token-contract.test.ts:SEMANTIC_USE_ALLOWLIST` enforces it in CI; `widgets/statusdot:.neutral` gives idle/ended no hue at all. **By design:** the mockup's single blue covered both "live" and "interactive"; shipped tokens split it into `--alive` (agent working) and `--accent` (focus/selection/links only). One meaning per hue — a refinement, not a break. |
| C — dimmest tone is hairline-and-chrome, never body text | LIVE | `transcript/toolcallitem.module.css:.demoted` carries the measured numbers: `--ink-low` is 2.97:1 dark / 3.64:1 light, under the 4.5:1 floor; `--ink-mid` clears it at 6.86/6.56. The same reasoning is repeated at `usermessageitem:.tag` and `chrome/taskspanel:.staleHint`. The rule is unchanged, but those ratios are pre-2026-09-06: `--ink-low` was raised to clear AA that day (see the 2026-09-06 entry below) and the stylesheet comments quoted here still carry the old numbers. |
| B — every state glyph-paired, colourblind-safe | **CHANGED, unexplained** | Half-shipped, and the half that shipped is the transcript's. `widgets/failureglyph` is a real distinct-shape marker rendered beside any single failed tool call (`ToolRow.tsx`) and any turn-failure notice (`SystemNoticeItem:FailureLine`), as well as beside the status row's failure count — so *failure* is genuinely glyph-paired wherever it appears in the transcript. The gap is everywhere else: `widgets/cadence` draws one dot shape for every state, varying only hue, with an `aria-label` as the sole non-colour signal, and no `FailureGlyph` appears anywhere under `shell/rail/`. So the navigator — the app's primary triage surface — is colour-only for idle / working / needs-you / ended. |

**02 · Chrome & labels** — chose A (sans, sentence-case, hairline) plus D's
`Details ⋯` overflow. Shipped `36989fbdc`, `974abbc07`. The mockup's own words
for all-caps mono labels: *"the amateur tell."*

| Part | Verdict | Where it stands |
| --- | --- | --- |
| A — sans, **sentence-case**, hairline | CHANGED, **superseded** | Labels are sans, never mono (`Rail.module.css:.sectionTitle`), and session names are sentence-case. Group and tier headers kept `text-transform: uppercase` with tracking — which reads as the mockup's Alt C, but is **explicitly sanctioned by a later clarification**: `design-system.md` §Type (polish pass, 2026-07-24, commit `3755e95c2`) rules that a short structural eyebrow at caption size, medium weight, `--ink-low`, uppercase, `0.04em` tracking "is typographic hierarchy, not shouting", and names the rail's "Projects" as its example. It is legitimate only for grouping eyebrows of ~2 words or fewer — never buttons, titles or sentences. So the fourteen surviving `text-transform: uppercase` rules are not automatically defects; each is one only if it labels something longer than an eyebrow. Nothing here is open work. |
| D — top bar is identity-only, detail behind an overflow | CHANGED | **By design.** `widgets/panescaffold`'s header is genuinely identity-only, but `chrome/StatusRow` keeps model, cost, context and work time visible in a footer strip. Its own comment: everything shown "could make you act in the next minute." The mockup's one hide-it idea became three tiers — identity header, glanceable footer, exact-figures Details sheet. |

### Transcript grammar

**03 · User message & steering** — chose A (quiet left `You` tag, no bubble).
Shipped `43069bfa1`, `7098d3fae`, `36989fbdc`.

Verdict **CHANGED, by design, on colour — LIVE on geometry (kata 8v4n).** The
bubble is gone and nothing is right-aligned, as decided. The tag/message
contrast is deliberately inverted from the mockup — `.tag` is `--ink-hi` and
`.text` is `--ink-mid`, because a dim tag on a dim message gave zero
separation, with the ratios recorded in `usermessageitem.module.css`.

That is only the colour half of Alt A, and it is all this entry originally
checked. The 40px inline gutter column is now superseded by the stacked `You`
eyebrow described in `docs/web-ui/specs/2026-07-27-transcript-tiered-density-design.md`
§Decision revisions requiring Jesse's ratification, item 3. The earlier verdict's
"by design" covered the contrast inversion, not this.

`usermessageitem.module.css:.message` still carries the exchange-boundary
geometry measured at 32px in the live app; the change is that the speaker cue
has moved from the inline gutter to the stacked eyebrow. Steering rendering is
being worked separately and is out of scope here, but `steeringitem.module.css`'s
divider still stacks its own summary above its body the same pre-fix way — it
will need the identical eyebrow when that work lands.

**Superseded again, 2026-07-29 (slack-lean speaker treatments, spec
`docs/web-ui/specs/2026-07-29-transcript-slack-lean-messages.md`).** The stacked
caption eyebrow lasted two days: speaker identity is now a one-line header — a
24px avatar tile beside `You` at body size plus the message's clock time — and
the contrast demotion itself is reversed: `.text` is `--ink-hi` (spec decision
5; the header does the boundary-scannability work the muting was doing). The
steering divider did not get the eyebrow in the end; it got body-size text
instead (Jesse: "system Steering should probably be the same font size as other
messages" — `steeringitem.module.css` `.summary`/`.body`).

**04 · Assistant hero & reading hierarchy** — chose A (size + space) + D
(contrast), explicitly **not** C (first-sentence lede). Shipped in the React
rewrite by `cd4663a99` and `bf1c7f318`.

| Part | Verdict | Where it stands |
| --- | --- | --- |
| D — agent prose at full contrast, user demoted | LIVE | `widgets/markdown:.root` is `--ink-hi`; `usermessageitem:.text` is `--ink-mid`. |
| C — first-sentence lede | ABSENT | Correctly rejected. Agent text is one plain `<Markdown>` block. |
| A — agent prose wins on **size and space** | CHANGED, **SUPERSEDED** | `agentmessageitem.module.css:.message` no longer sets `--prose-font-size: var(--font-size-pane-title)`; agent prose is body-size now. The surviving hierarchy is the contrast pair from `docs/web-ui/specs/2026-07-27-transcript-tiered-density-design.md` §Decision revisions requiring Jesse's ratification, item 1: `--ink-hi` agent prose against `--ink-mid` user text (decisions.md:193). The size signal was applied per-fragment and cancelled itself. |
| B — no visible agent label | **SUPERSEDED** | The SR-only `Agent` label is superseded by the visible exchange-boundary eyebrow `Agent · {model}` described in `docs/web-ui/specs/2026-07-27-transcript-tiered-density-design.md` §Decision revisions requiring Jesse's ratification, item 2. The eyebrow is shown only at exchange boundaries, matching the measured 32px boundary mechanic (decisions.md:119-130). |
| (all four alternatives agreed) inline code is a quiet underline, never a filled chip | LIVE | `markdown.module.css:.inlineCode` uses relative mono sizing and an `--edge` bottom rule with no background, padding, or radius. |

Rows B and D superseded again, 2026-07-29 (slack-lean speaker treatments, spec
`docs/web-ui/specs/2026-07-29-transcript-slack-lean-messages.md`): the
exchange-boundary eyebrow is now the speaker header — avatar tile + `Agent` +
`model · time`, firing only at exchange openers (spec decisions 1-2) — and the
user-text demotion in row D is reversed: user text is `--ink-hi` (spec decision
5). Agent prose keeps its full-contrast body-size treatment unchanged.

**05 · Thinking block** — chose A (reserved-slot collapse) + D
(duration-weighted prominence + gist). Originally simplified by `44d04271f`.

Verdict **SUPERSEDED BY kgp2 — LIVE.** The always-open live state remains the
correct append-only `StreamingText` path. Once settled, the native one-level
disclosure says `Thought for <duration>` when a valid item timestamp pair is
available, preferring wire timestamps and then reducer-observed frame timing;
it never measures an in-progress item or invents a missing duration. Its
collapsed line also carries a bounded plain-text rendering of the final
meaningful nonblank thought line, while expansion renders the complete
Markdown body. The newer kgp2 record explicitly supersedes the earlier
no-preview choice; session-keyed disclosure state remains unchanged. The preview
is closed-only; the open state shows just the short label, per `docs/web-ui/specs/2026-07-27-transcript-tiered-density-design.md` §Decision revisions requiring Jesse's ratification, item 4.

Restyled 2026-07-31 (the **draft restyle** — mockup #4 of the `/dev/thoughts`
candidates, Jesse's pick): the collapsed label grammar is dot-joined
(`Thought · 12s · preview`, superseding kgp2's `Thought for <duration>`
wording) and trails a rotate-on-open chevron (ToolRow's data-open idiom); the
live stream renders as italic draft text — settling to roman — with the
eyebrow at body size (quiet through ink, never size), capped at six body
lines, pinned to its own tail (a ResizeObserver re-pins on delta-less
reflows) — **the cap is superseded, see the next paragraph**. The mockup's
soft fade marking the cut was implemented — corrected from its surface-0 to
the pane's actual surface-1 backdrop — then **removed entirely on Jesse's
call after seeing it live** (2026-07-31): the cap's cut is a hard clip at the
box edge.
Because the cap made the wire's missing reasoning `item/completed`
load-bearing, the renderer now settles a thought the moment anything later
starts in its turn (tail position stands in for the completion the wire
withholds; no duration is invented before the turn's own settle stamps one) —
restoring kgp2's "collapses once the assistant starts answering" behavior
that the always-open-while-inProgress reading had quietly repealed. The
no-caret law, the closed-only preview, and session-keyed disclosure state
all stand.

The six-line cap is **SUPERSEDED by kata bh8h, 2026-07-31** — Jesse: "webui
showing thinking as it's running should not have a bounded height. you should
be able to see the full thinking block." A running thought now renders at
whatever height its own text needs; the capped box, the tail pin and its
ResizeObserver are gone, and the transcript's own scroller is the only
viewport, the same one every other growing item shares. The fade question
dies with the cap — nothing is cut off, so there is nothing to mark. What the
cap took with it and what it did not: the italic-to-roman draft signal, the
dot-joined collapsed grammar, the chevron and the body-size eyebrow are all
untouched, and settle-on-supersede stands on its own — it restores kgp2's
"collapses once the assistant starts answering," which is a behaviour call
rather than a consequence of the cap.

**06 · Tool calls & long output** — chose A (cluster summary leads with the
mutating step) + D (peek / ride / drop). Shipped `7bbe0e91e`.

| Part | Verdict | Where it stands |
| --- | --- | --- |
| A — a run of finished calls folds to one summary line naming the consequential step | **ABSENT, unexplained**. Landed 2026-09-06: see below. | There is no cluster concept at all. `TurnBlock` renders items one at a time via `itemRendererFor`, and `toolRowGrammar.test.tsx` pins "exactly one per call." A run of read/grep/edit/test calls is a column of individually-collapsible rows. This is also principle 2 of the brief, so its absence is a gap against the design law, not only against one mockup. Landed 2026-09-06 as `transcript/toolRuns.ts` + `ToolRunGroup.tsx`; the fold rule is written up in the 2026-09-06 typography, measure and rhythm entry below. |
| D — peek / ride / drop tri-state | CHANGED | **By design.** The anti-lying principle survives — `tools/helpers.ts:tailFold` and `widgets/codeblock` never offer an "expand" over bytes that are gone, and say so inline. The explicit three-state vocabulary is gone; the state is prose, not a labelled UI state. |

**07 · System churn & silent success** — chose A (quiet one-liner) + B
(coalesced "N system events"). Shipped `42b233353`.

Verdict **LIVE for both chosen alternatives.** `systemnoticeitem.module.css:.line`
is `--ink-low` with no rule or divider and no character count, citing the mockup
by number; `transcript/messages/systemGrouping.ts:shouldGroup` coalesces at three
or more, pinned by its own test. One nuance differs: the group headline names the
chronologically first event rather than the most consequential one. The round
timings joined this quiet one-liner rule too; the scaffold box no longer applies
to them, per `docs/web-ui/specs/2026-07-27-transcript-tiered-density-design.md`
§Decision revisions requiring Jesse's ratification, item 5.

The topic's third fix — Alt C, "✓-only silent success" for a call that returns
nothing — was not among the chosen alternatives and was not audited. It appears
moot by construction: `tools/editTools.tsx`'s `write_file` renderer always
produces a `Wrote <path>` summary, so there is no silent case to fix. Recorded
as unverified rather than passed.

### Subagents

**08 · Subagent module states** — chose A (honest-clock demotion + `?` unknown)
+ B (columnar overflow sorted by severity). Shipped `9f16d9d35`.

Verdict **ABSENT, replaced.** `classifyJobStatus` degrades an unknown status to
"running" — deliberately the opposite default from the mockup — and
`sortedRows` orders strictly by spawn index, never severity. What shipped
instead is a different anti-churn mechanism: `DONE_VISIBLE_CAP` folds only done
rows behind "+N more", and `watchedChild.tsx` watches each child's own thread
status rather than inferring from the parent's liveness. A real solution to the
same problem, arrived at independently.

**09 · Subagent navigation & nesting** — chose A (parent breadcrumb banner).
The worst-state rollup was **held constant across all four alternatives**, not
D's contribution (D's own idea was a tabbed Map view of the whole session
forest). Shipped `789c3927b`.

Navigation half: **CHANGED, by design.** Kata `0pzz` (`033acf5bf`, `60397b261`)
chose a drill-down child pane carrying one hop of parent identity plus a return
action, rather than a breadcrumb chain.

Rollup half: **ABSENT — built, shipped, then lost in the rewrite.** The commit
above is titled "Subagent parent breadcrumb + Esc-to-parent + **worst-state
rollup** (mockup #9)" and its body describes the feature landing in the Go hub's
`renderer.js`/`style.css`. That hub was deleted in `660376f78`, kata `0pzz`
restored only the return navigation, and the current frontend has no
rollup or worst-descendant logic anywhere. Same shape as topic 12: not
"never built", but "built, then dropped by the rewrite."

### Navigator & wayfinding

These three are the sidebar. **Two of them were revisited and reversed on
2026-07-23** — those are supersessions, not regressions, and they are closed.

**10 · Sidebar IA** — chose A (delete the LIVE rail) + C (cluster repeated
titles). The magnitude-rollup badge belongs to **Alt A's own description**, and
the "you are here" selected-row treatment sits in the mockup's shared CSS block
and applies to all four alternatives — neither is D's, and D's one original idea
(the cross-project "Needs you" tier) is credited under topic 11 where it
belongs. Shipped `c16f8178f`.

Verdict **superseded** for Alt A: `docs/superpowers/plans/2026-07-23-webui-ux-round2.md`
records the decision to keep Live, Pinned, Projects, Archived and Test runs, and
that "residual duplication is accepted." Alts C and D are **ABSENT**, and in a
telling way — `TreeNode.cluster_count` and `TreeProject.rollup_live` are both
defined on the wire and never read by anything. A half-finished backend-first
cut, not a rejection. No "you are here" row exists.

**11 · Cross-session attention triage** — chose A (a top "Needs you (N)" tier)
+ B's badge and `n` cycle. Shipped `c16f8178f`, `a67115aac`.

Verdict **superseded.** The tier was built and then deliberately deleted
(`88920043d`, kata `vbh8`); the round-2 spec names the dedicated tier itself as
the defect and replaces it with inline signals plus sort-to-top — which is
structurally the mockup's own Alt C. The `n` cycle key was never bound.

**12 · Test-runs bucket & finding old work** — chose B (date sub-grouping).
Shipped `d8eb90055`.

Verdict **ABSENT — lost in the rewrite**, scoped strictly to the date
sub-grouping. To be clear against topic 10 above: the **Test runs section itself
survives** (`Rail.tsx`, `title="Test runs"`, rendered through the same
`projectNodes()` as any project). What is gone is Alt B's mechanic — the
Today / Yesterday / Older buckets inside it, of which there is no trace in
`railNodes.ts` or `Rail.tsx`.

It shipped faithfully into the Go-templated sidebar, which was deleted wholesale
in `660376f78` ("webui m10: delete legacy assets"). Nothing reimplemented it and
the round-2 spec does not mention it — an unintentional casualty, distinct from
the two supersessions above.

### Liveness, motion, scroll

**13 · Liveness & motion economy** — chose A (one liveness source; kill the
cursor blink) + B (quantized quiet bucket) + calm/concern banding. Shipped
`3b5b9b2fc`.

Verdict **INVERTED, by design.** The quantized bucket is LIVE
(`transcript/flow/liveness.ts:formatQuietBucket` → "~30s" / "~1m" / "~2m"). The
motion decision was consciously reversed: the wave-2 design system names
"streaming caret blink" as one of three sanctioned motions and forbids idle
pulses — the exact inverse of the mockup, which wanted the caret killed and one
idle dot breathing. Calm/concern escalation was likewise declined on purpose:
`livenessline.module.css` reserves the attention tone for the new-content pill
alone, to avoid two competing attention signals.

**14 · New-content pill & error findability** — chose B (split calm/urgent
colour, which already carries its own jump-to-the-worst-anchor behaviour). C
(scrollbar minimap) was recommended but not built. D's distinguishing idea was a
*cycling queue* through several anchors, which is not what shipped — the app
picks a single target, so this reads as B rather than D. Shipped `3b5b9b2fc`.

Verdict **LIVE**, with one loss. `NewContentPill` picks danger → attention →
neutral count in that precedence, and `useTranscriptScroll:errorAnchorIndex`
really does scan turns in document order for the first unseen failure. The
minimap is confirmed absent. **Lost:** the pill's arrow is a hardcoded `↓` with
no flip to `↑` when the new content is above — a rule
`parity/contracts-transcript-scroll-liveness.md` explicitly named as behaviour
to re-cover, and it was not.

### Edge & error states

**15 · Connection & main-agent errors** — chose A (chrome reconnect banner +
queued send). Shipped `79cdb7b30`.

Verdict **CHANGED.** The banner survives but is deliberately off the colour
allowlist — `shell/ConnectionBanner.module.css` says "same understated treatment
either state, not a loud color", declining the mockup's amber-while-reconnecting
rule. **Queued send does not exist:** `protocol/client.ts:request` rejects
immediately whenever the connection is not ready, so a send while disconnected
simply fails. The comment there points at a server-side auto-resume layer as the
replacement resilience strategy. `TurnFailureEndCap` (mockup 15's Alt C idea) is
live and matches well.

**16 · Blocking needs-you** — chose A (amber container + blue button) + C
(docked bar above the composer) + D (quick-reply chips). B (all-amber) was
explicitly rejected. Shipped `9160a98e3`; annotated in `TARGETS.md`.

Verdict **CHANGED — the most consequential gap in this section.** C's placement
survives: `AskDock` mounts above the queue strip inside the composer. Almost
nothing else did. The ask card and its Send button use only neutral tokens —
`--surface-1/2`, `--edge`, `--ink-hi/mid` — referencing neither `--attention`
nor `--accent`. Options render as plain radio rows, not chips. So the app's
clearest "a human is needed right now" moment is drawn in the same neutral ink
as everything else. B stays correctly rejected. The blue primary button survives
only on sandbox escalation's Allow, a different feature.

**17 · Context pressure & compaction** — chose A (quiet gauge, coloured only
near the edge). Shipped `e5fb74608`.

Verdict **LIVE.** `chrome/statusFormat.ts:contextTone` is one shared function
consumed identically by the status row and the details panel, and a compaction
event renders as a collapsed, never-coloured one-liner. A 95% danger tier was
added on top of the mockup's two zones, called out in code as a deliberate
addition.

**18 · Plan / todo** — chose C (inline plan block in the transcript). Shipped
`2757f0840`.

Verdict **CHANGED, and it diverged before the rewrite.** The inline card
(`tools/taskCard.tsx`) has no border, no glyphs, no colour, and shows only the
rows one `task_list` call touched — never the standing list. The real
glyph-and-tone checklist (`○ ● ✓ ✕`) lives in `chrome/TasksPanel.tsx` behind a
Sheet, which is structurally the mockup's own **Alt B**, not the Alt C we chose.
`docs/superpowers/specs/2026-07-15-inline-task-update-card-design.md` records
walking the living-plan card back to per-call rows in the old app already; the
rewrite carried an already-diverged decision forward faithfully.

**19 · Diff / patch** — chose A (collapsed `+N −N` expanding to a desaturated
unified diff). Shipped `ec0e04f43`.

Verdict **CHANGED — Jesse re-ratified the palette in kata 9jew (2026-07-26).**
The collapse-and-expand shape is live. The mockup's own capitals remain the
authoritative visual intent: diff add/remove uses a dedicated, quiet,
non-semantic pair because those colors are syntax/domain notation, not status.
`widgets/diffblock:.add/.del` now use `--diff-add-bg`/`--diff-del-bg`, with
`+`/`−` markers as the independent meaning channel; the old semantic-token
allowlist entry is removed. The implementation ruling is recorded in
`docs/web-ui/design-system.md` §4 so mockup 19 cannot reopen the contradiction.

**20 · Multi-image** — chose B (contact-sheet grid + set-navigating lightbox).
D (provenance-grouped) was dropped for want of a backend signal. Shipped
`f6273464d`.

Verdict **CHANGED, with real information loss.** The shared lightbox is live and
wraps correctly. The grid is a flex strip of fixed 96px thumbnails, not a
contact sheet. More importantly: **captions are gone.** The wire's `OutputImage`
carries `source`, `name` and `path`, and `protocol/reducer.ts:imagesToStrings`
collapses each to a single fallback string before the UI ever sees it — so the
frontend has nothing left to label or group by. A caption was constant across
all four alternatives, not a feature of B alone.

**21 · Cold start** — chose A (optimistic echo) + B (skeleton turn) + C
(onboarding affordances). Shipped `b5374494e`.

| Part | Verdict | Where it stands |
| --- | --- | --- |
| A — optimistic echo | CHANGED | **By design**, dated. `pending/PendingChips.tsx` renders the pending message as a chip beside the composer rather than an echoed transcript row, and says so: "a conscious presentation choice in the wave close sweep." |
| B — skeleton turn | **ABSENT** | No skeleton is wired to any transcript or cold-start loading path; `widgets/skeleton`'s real consumers are settings, the doc pane and the model catalog. |
| C — onboarding affordances | **ABSENT** | `panes/welcome/Welcome.tsx` is an `EmptyState` title and two buttons — no orientation copy, no example prompts. Nothing in the later wave or round docs discusses onboarding, so this reads as unfinished rather than declined. |

**22 · Mobile spawn** — Treatment A (tuned single screen + auto-expanding
textarea), formally **approved** in
`docs/superpowers/specs/2026-07-12-mobile-spawn-form-design.md`. This is the
only alternative in the whole set carrying a written sign-off.

Verdict **CHANGED, and the approval was never re-targeted.** The auto-expanding
textarea is live (`widgets/textarea:autoGrow`). The rest is not: `panes/spawn/`
uses one unified flex layout with no mobile branch at all, and two fields were
demoted into a collapsed Advanced section instead. The approved spec's own
implementation steps name `templates/partials/spawn.html`, `assets/style.css`
and `assets/spawn.js` — all deleted. No comment anywhere acknowledges that the
spec is still open.

**23 · Subagent sidebar** — a single prototype, no alternatives.

It prototyped a recursive navigator where each parent partitions its children
into an always-visible current set and a collapsed "Inactive subagents (N)"
disclosure, recursively, with lineage preserved. Verdict **shipped**: recursive
nesting and the inactive-children disclosure are both real in
`shell/rail/railNodes.ts` (`splitChildren` / `CURRENT_SUBAGENT_STATES`).
Settled 2026-08-05 (Jesse): "idle" folds as inactive too — since sessions
stopped closing on provider failure (ff859dbbe), a finished child rests open
at idle indefinitely, so idle children are settled work, not current.

## The rule that explains most of the drift

Most CHANGED verdicts above are not neglect. They trace to one rule the mockups
could not have known about: a **machine-enforced colour allowlist**
(`styles/token-contract.test.ts:SEMANTIC_USE_ALLOWLIST`, documented in
`design-system.md` §4) restricting `--attention`, `--alive` and `--danger` to a
short reviewed list of widgets. That is why the ask card and the inline task
card stay flatly neutral even in textbook needs-you moments — a real design law,
just never reconciled against mockups that assumed per-feature colour.

The same rule cuts the other way for diffs, where it overrode the mockup's
explicit CRITICAL CONSTRAINT. Whichever way it is settled, it should be settled
once, in `design-system.md`, rather than per feature.

## Known, not scheduled

Real gaps that are deliberately not being worked right now because other
worktrees own those files:

- The navigator's `TreeNode.cluster_count` and `TreeProject.rollup_live` are
  defined on the wire and read by nothing (mockup 10, alts C and D).
- Test-runs date sub-grouping (mockup 12, Alt B) shipped faithfully into the
  Go-templated sidebar and was lost when `660376f78` deleted it. Nothing
  reimplemented it and no later doc mentions it.
- Worst-state rollup for nested subagents (mockup 09, held constant across all
  four alternatives) shipped in `789c3927b` and was lost when the Go hub was
  deleted. A parent row's colour reflects only its own status today.

## 2026-07-31 re-theme: Fjord + Ledger

The wave-2 palette (neutral dark ink + steel-blue accent, cool light grey)
was replaced wholesale — the first deliberate palette change since the
rewrite. Five complete candidate palettes were built as `[data-theme="cand-*"]`
overrides in `tokens.css` and evaluated with the real widget library (one
session pane plus the core control set rendered per candidate). Every
candidate was held to the same bars the canonical themes are: ink-hi ≥ 7:1,
ink-mid ≥ 4.5:1, ink-low ≥ 3:1 on both pane surfaces, accent ≥ 4.5:1 for
link text, and the DiffBlock quiet-contrast rules from
`token-contract.test.ts`.

**Chosen:** dark = **Fjord** (cool blue-grey surfaces, frost-blue accent,
teal-leaning alive); light = **Ledger** (warm paper surfaces, warm
near-black ink, brass attention, deep indigo accent). Rejected: Carbon
(dark neutral, indigo accent), Hearth (dark warm sepia), Studio (light
crisp, product-blue). Type scale, space grid, radius, and motion are
unchanged — the re-theme touched color tokens only, which is exactly the
seam the token contract was built to provide.

## 2026-08-13 re-theme: full adoption of Beautiful UI

Superseded the 2026-07-31 Fjord/Ledger decision above, five weeks after it
shipped. Jesse's call: "move toward their aesthetic" — full adoption
(palette, fonts, motion, chrome) of [Beautiful UI](https://www.beautifului.dev)'s
design language, rather than another from-scratch candidate palette. Spec:
`docs/superpowers/specs/2026-08-13-webui-beautiful-ui-retheme-design.md`.
Shipped in two phases on this branch: tokens+fonts (`672cfffcd`) and widget
chrome (`3da2dcf8e`).

Unlike the 07-31 re-theme, this one is not color-tokens-only — it touches
palette, type, shape, elevation, and motion together, because that is what
"adopt a design language" means as opposed to "pick a new palette." What was
kept, deliberately: the token-contract enforcement machinery (§4 of
`design-system.md` — no literals outside `tokens.css`, the attention-family
allowlist, the z-ladder, focus-ring rules, dark/light parity); the
attention-semantics thesis (one meaning per hue, Beautiful UI's own
orange/green/red/blue map 1:1 onto `--attention`/`--alive`/`--danger`/
`--accent`); the 4px spacing grid and the type ramp's size steps; Cadence's
honest liveness (no idle motion, ever — unchanged); the widget inventory,
APIs, and one-dir-per-widget convention; and the AA guarantee that any hue
used as text clears 4.5:1 on the surfaces it sits on, in both themes — now
carried by the new `-ink` companions (`--accent-ink`, `--alive-ink`,
`--attention-ink`, `--danger-ink`), because Beautiful UI's own bare light
hues measure only 2.8–3.9:1 on white.

Two changes are worth flagging as real departures rather than re-tuning:

- **The motion law widened** from "default none" to "no idle motion."
  Idle animation is still banned outright, but a new 150ms
  `--motion-duration-hover` budget now covers color/background/border/shadow
  transitions on hover/focus/press for interactive chrome — Beautiful UI's
  chrome leans on hover response in a way the old two-state (rest/active)
  system didn't need. Transitions still name their properties; `transition:
  all` is not used anywhere.
- **Light theme inverts its surface order**: `--surface-1` (card/pane
  background, white) now sits *lighter* than `--surface-0` (page
  background), the reverse of Ledger's ordering where panes were darker
  paper than the page. This is how Beautiful UI's cards "pop" instead of
  blending, and it was flagged as the re-theme's main readability risk in
  the spec (any pane assuming the old darker-than-page ordering needed a
  second look during the phase-1/phase-2 gallery review).

Rejected implicitly by not being on the table: another from-scratch
candidate-palette bake-off in the 07-31 style. The decision was to stop
generating original palettes and adopt an external, complete design
language wholesale — attributed per its MIT license
(`cmd/evener-hub/frontend/LICENSES/beautiful-ui.txt`, Copyright (c) 2026 Shane
Levine) rather than reinterpreted as an in-house original.

## 2026-08-14 title-count notification default flips to ON

Reverses one channel of the wave-7 "all-OFF" floor (`stores/prefs.ts`'s
`loadNotifications`, pre-adjudicated code-wins resolution of a legacy
copy/code discrepancy — see that function's own comment history). All four
notification toggles (title, favicon, OS notification, sound) shipped OFF by
default; the attention system built on top of them was consequently
invisible to anyone who never opened Settings and turned it on by hand.

Title bar count is the one channel that flips to ON. It's the quietest,
most reversible of the four: no OS permission prompt (unlike OS
notification), no audio (unlike sound), no favicon repaint to notice or
distrust — just a `(N)` prefix on the tab title, gone the instant the count
drops to zero. Favicon dot, OS notification, and sound stay OFF; this is a
one-channel exception to the floor, not a re-litigation of it.

Only the default for an *absent* key changed. A browser that already stored
an explicit title=off keeps reading false — `readBool`'s stored-value-wins
contract (`prefs.test.ts`'s pinned-key-contract block) already guaranteed
this for every other pref here; the migration test in `prefs.test.ts`
("a stored title of '0' beats the new on-by-default") pins it for title
specifically.

## 2026-08-14 welcome pane teaches the chords

The welcome/empty state (`panes/welcome/Welcome.tsx`) gained a quiet hint
row below the existing task suggestions, listing the three chords a new
person has no other way to discover from this cold pane: Mod+K (command
palette), Mod+I (focus composer), Mod+J (next session needing you) — the
same three from `CommandPalette.tsx`'s own `HELP_ROWS`, rendered through the
shared `KeyHint` widget rather than hand-rolled text — plus a line noting
that `?` inside the palette shows the full shortcut legend. Caption-size,
ink-low/mid, placed below the example-prompt buttons so it reads as an
aside rather than another call to action.

<!-- decision-tables:end -->

## 2026-08-14 launch-error presentation: pass-through by default

`friendlyLaunchErrorMessage` initially mapped the whole hubLaunch family to
"no daemon — run evener" guidance. A live repro (credentialed daemon,
uncredentialed default provider) showed that masks the family's config half
with actively wrong advice, and review showed it also destroyed the daemon's
own propagated stderr — a diagnosis the hub has a dedicated Go test to
preserve. The rule is now inverted: hubLaunch messages pass through with
their own instructions; only the no-diagnosis subset (launch-check
canceled/timed out, fork/exec — the daemon never produced output) gets the
guidance copy. A new hub message therefore defaults to being shown, never
swallowed.

## 2026-08-14 GoalControl rides the inline status row

Production only ever mounts SessionChrome's composer placement, and the goal
chip only rendered in the footer placement — unreachable dead code, caught by
the live E2E pass (the long-standing "popover clipping" suspicion was this,
misdiagnosed). The chip now renders in the inline status row, compressing
away below 560px of container width per the pre-existing status-row rule.
Known limitation: phones therefore have no goal affordance; a compact
trigger or SessionMenu entry is the follow-up if goals matter on phones.

## 2026-08-14 session commands move to the composer; the palette hands off

Session commands (`/goal`, `/model`, `/steer`, `/compact`, and the rest of
`shell/palette/commands.ts`'s session-scoped registry, built-in and plugin
alike) were offered in two places at once: the command palette (`Mod+K`)
could list and run any of them against the focused session, and the
composer's own inline `/` menu separately completed the plugin catalog for
sending as message text. Two entry points for the same action is a UX tax —
a real user has to learn (and re-learn) which surface a given command lives
in — and the split had drifted further than that: the palette could run a
mutation on a session it wasn't showing, the composer's menu never offered
the built-ins at all, and a plugin command picked from the palette inserted
a qualified invocation the composer's own menu had no part in resolving.

The fix picks ONE place per command, keyed on what the command is *for*:
"the palette is where you go; the composer is where you act on this
session" (design-system.md §9 has the full mechanics). App-global commands
(new session, settings, theme, search, …) need no session and stay
palette-native. Every session-scoped command now runs from that session's
own composer, Slack-model — the composer becomes the session's real command
line, not just a text box beside it.

**The interception trade-off was accepted deliberately.** Making the
composer intercept a submitted message that PARSES as a known `/command`
means a literal chat message starting with, say, `/goal` no longer sends as
written — it runs `/goal` instead. This is exactly Slack/Discord's own
behavior, and it's the behavior most users bring with them already; the
escape hatch (anything that doesn't parse as a known built-in, including
every plugin catalog command, sends as plain text unchanged) covers the
"I actually meant to say that" case without requiring an explicit "run as
command" gesture. The alternative — a confirmation step, or requiring some
other prefix to distinguish "run" from "send" — was rejected as friction
the muscle-memory case doesn't need and the rare literal-message case can
route around by rephrasing.

**The handoff row is the bridge.** A user who still reaches for the palette
out of habit and types a `/`-prefixed session command name isn't met with a
missing command (which reads as a bug or a memory slip) or a command that
runs against whatever session happens to be focused (which is exactly the
"palette can mutate a session it isn't showing" problem this decision
closes) — they get ONE row, "Continue in the composer: /goal …", that
inserts their own typed text into the focused session's composer and moves
focus there. With no session focused, the row says so instead of silently
doing nothing. The palette never executes a session mutation itself again.

## 2026-08-16 light-theme --attention goes from salmon to true amber

Beautiful UI's light-theme orange `#EF720C` was adopted 1:1 in the
2026-08-13 re-theme, but on white surfaces its tints betray the design
system's own vocabulary: every doc calls this hue "amber", yet the 15%
`--attention-bg` wash (badges, chips, toasts) rendered `#FFEBE0` and the
AskDock batch's deliberate 24%/55% amber envelope rendered `#FFDFCD` —
salmon/peach (oklch hue ~51°), not amber. The color-is-attention
semantics are untouched (amber still means exactly "a human is needed");
the fix is one token. Light-theme `--attention` is now `#F59E0B` (oklch
hue ~70°, true amber), so every `color-mix(var(--attention) …)` consumer
— badges, toasts, chips, `--attention-edge`, and AskDock's local mixes —
follows automatically. Dark keeps `#F68F3C`, which already reads amber
against dark surfaces; `--attention-ink` stays `#AD5209` (still 4.75:1 AA
on the new tint). The re-theme spec's palette table is a historical
record; this entry supersedes its light `--attention` cell.

## 2026-08-27 one open-out affordance: OpenButton/OpenIcon

"Open out of this surface" had grown four presentations of the same idea —
the delegate/delegate_send rows' and notification cards' `OpenTranscriptButton`
(word + glyph), the file tool cards' `FileOpenBesideButton` (glyph only), the
activity chrome's `ActivityTranscriptAction` (glyph only), and settings'
"open in editor ↗" (a raw anchor with a text glyph). All are now one widget,
`widgets/openbutton` (`OpenButton` + the `OpenIcon` box-arrow glyph, in
design-system.md §3's inventory with a `/dev/widgets` gallery section): word
form, `iconOnly` dense-row form, and an `href` anchor form for external
targets (new tab, no opener, no referrer — the same rel policy as the app's
other new-tab links, which is why settings' `rel` assertion changed). The
widget owns `stopPropagation` because every form rides something clickable.
Two layout rulings shipped with it: a purpose-only tool row (the delegate
card) now trails its affordance on the disclosure line itself — a sibling of
the trigger button, never nested inside it — the placement notification
cards already gave "Open subagent"; and the settings link's local "↗" text
glyph is retired in favour of the standard box-arrow. **The affordance's
rendering is planned to change**; routing every site through one component
is what makes that a one-place change, and the gallery section is where the
new rendering gets reviewed.

## 2026-08-29 plugins settings: segmented workspace

Settings → Marketplaces & Plugins was redesigned from a six-mockup
exploration (decluttered sections, segmented workspace, master–detail,
catalog storefront, mobile-first sheets, power table — mockups and
desktop/mobile screenshots of all six in
`docs/web-ui/specs/assets/2026-08-29-plugins-segmented-workspace/`). The
winner is the **segmented workspace**, and its two idioms are now the
design system's collection-page language, written up in design-system.md
§10: same-weight sibling collections go behind one page-level
SegmentedControl with counts in the segment labels instead of stacking
titled sections, and per-item actions leave the list rows (which become
single tappable targets) for a detail Sheet — right side on desktop,
bottom on mobile — that owns state chips, a lazily-browsed description, a
meta table, Switch rows for binary state, and the ConfirmDialog-gated
destructive action nested safely over it.

One deliberate departure from the approved mockup, discovered against the
wire: the mockup's "Update available" chip and "Upgrade to vX.Y.Z" label
imply an update-detection field the plugin data model does not have
(`PluginEntry` carries version, enabled, autoUpgrade, broken — nothing
about a newer upstream). The shipped sheet offers a plain "Upgrade" action
(the RPC's actual "check and pull if newer" semantics) instead of faking
the field; real update detection is a backend feature, not a presentation
choice, and was not smuggled into this redesign. The implementation
screenshots beside the mockups in the assets directory record what
actually shipped, in the app's default theme.

## 2026-09-06 typography, measure and rhythm

The pass came out of a measured critique of the running app at 1440x900 and
375x812 in both themes,
`docs/web-ui/typography-spacing-critique-2026-09-06.md`; the implementation is
`docs/superpowers/plans/2026-09-06-webui-typography-spacing.md`, on branch
`claude/evener-webui-typography-spacing-3d4fd4`. Each paragraph below is one
decision, and three of them revise decisions recorded earlier in this file.

**The ramp is 12 / 13 / 15 / 18 / 22 / 28, with three line-heights.** The old
12/13/14/16/20 at 1.5 body / 1.3 title had a ratio of about 1.08 between
steps, which is one size with rounding error rather than a scale: 340 of 382
explicit `font-size` declarations picked caption or ui, so nearly everything
on screen was 12 or 13px and the eye had no landmarks to find. Body is 15
because Inter at 14 on a 1440 display is a settings-dialog size, and the
reading products that also carry code sit at 15 to 16. Body is 16 below 900px
for a second, harder reason: iOS Safari zooms into any focused field under
16px, and the body size IS the field size (the shared textarea takes it).
`--line-height-ui` (1.4) is new, for the dense chrome rows 1.6 was too loose
for. `src/styles/measure.test.ts` pins the six steps and the phone body off
disk.

**The reading measure is 44rem, with a wide preference.** `--session-measure`
was a 76rem literal on `.turn`, hand-copied into `session.module.css`. 76rem
is 1216px, which at a 1440 window is wider than the pane itself, so the column
was effectively full bleed and a plain agent paragraph measured **149
characters per line**. The comfortable range is 45 to 75, and the chat
products that must also show code settle around 90 to 100. 44rem is 704px:
about 90 characters at 15px, with room for a 100-column code block. It is now
one token declared on `<body>` that the transcript column, composer, cold
start, spawn form and settings content all read, so they can no longer drift
apart. Settings → Theme → Transcript width raises it to 64rem via
`<body data-transcript-measure="wide">` for readers who want the window back.
The new layoutguard case `transcript-measure` pins 100 characters per line and
a centred column at 1440 and 1920; it is mutation-verified, and putting the
76rem literal back fails it at 126 and 157 characters per line respectively.

**Vertical rhythm is four named steps, which REVISES the 2026-07-30 "no dead
air" ruling recorded in topic 03.** `--rhythm-line` (4px, inside one item),
`--rhythm-item` (8px, between items in a run), `--rhythm-group` (16px, between
a run and the next speaker header, and above a turn footer),
`--rhythm-exchange` (24px, above a user message). The transcript previously
had exactly one deliberate step, 4px message padding against 8px tool-call
padding, because the 2026-07-30 ruling had removed the larger one at exchange
boundaries as dead air. That ruling was correct for the layout it was made in:
lines ran 149 characters and every row sat 4px from its neighbour, so added
margin was air with nothing to separate. With the column bounded to
`--session-measure`, 24px above a user message reads as a paragraph break, and
it is what lets the eye find the next exchange in a long session without
hunting for a 24px avatar. The full reasoning is in
`usermessageitem.module.css`'s header, which is where the old ruling lived;
`src/styles/rhythm.test.ts` pins each step to the site that names it.

**Speaker headers are semibold.** At body size, a medium weight beside regular
prose is a landmark only for a reader already looking for one, and the speaker
header is the transcript's one structural landmark per exchange. Its meta line
moved at the same time, from caption-size `--ink-low` to ui-size `--ink-mid`,
since a model name and a clock time are things a reader actually reads.

**Pane titles are headings, SUPERSEDING the 2026-08-13 micro-label port for
panes.** The Beautiful UI adoption brought the micro-label across (11.5 to
12px, uppercase, 0.08em tracking, `--ink-mid`) and made it the PaneScaffold
title. On a dashboard card that pattern labels a container sitting inside a
page that has a real heading; here the pane IS the page, so the page's title
became its smallest text ("START AN AGENT", "GENERAL", "THEME"), and once a
session had a title, that title was the user's whole prompt rendered as an
uppercase sentence, which design-system.md §6 already forbade in as many
words. A PaneScaffold title is now sentence-case `--font-size-pane-title`,
semibold, `--ink-hi`; a session title is never transformed at all. The
micro-label keeps its real job under a single name, next.

**One eyebrow recipe, and the rename it forced.** The app had 21
`text-transform: uppercase` rules spread across four tracking values. There is
now one token, `--tracking-eyebrow` (0.06em), and one recipe: caption size,
medium or semibold weight, `--ink-mid` or darker, uppercase, at most two
words, only ever titling a container inside a page (InspectorCard's header
band, RecommendationCard's kicker, Table headers, the rail's section titles,
the settings cluster headers). The two-word cap is a real constraint rather
than a guideline, and it forced a rename: the settings cluster "Agents &
models" is now **"Agent setup"**.

**Tool-run folding lands principle 2 and topic 06's Alt A**, the row this
document has carried as "ABSENT, unexplained". In a settled turn, three or
more consecutive completed, non-failed tool calls whose renderer has opted
in collapse into one `<details>` row labelled
`N steps · <last consequential summary>`. Folding is opt-in per descriptor:
`fold: "quiet"` (the reads, searches, web fetches and transcript reads)
folds and only counts; `fold: "consequential"` (the edit tools, shell,
worktree) folds and marks the mutations a label may name, since that is
what a run amounted to; `fold: "never"` (delegate, ask_user, task_list,
use_skill, the `job_*` tools) and any descriptor with no policy at all,
which is every unregistered or MCP tool, never fold away, because a tool the
UI does not know may have had a side effect the reader must see. A live turn never folds at
all, because while the agent is working each call appearing IS the progress
signal, and a failure, a call still in flight, an auto-expanding card or any
non-tool entry breaks the run rather than being spanned by it, so a folded row
can never gather calls the reader saw separated by an answer. Three is the
threshold because two folded rows save one row and cost a click.
`transcript/toolRuns.ts` decides what a run is; `ToolRunGroup.tsx` renders it,
with its disclosure state in the shared store.

**`--ink-low` was raised until it clears AA, and then mostly stopped being
used for reading.** The token is documented for placeholders, disabled
controls and timestamps, and it was nonetheless the colour of 96 text rules,
most of them at 12px too, so the two demotions compounded. Dark went to
`#8A8E95` (4.72:1 on `--surface-1`, 5.40:1 on `--surface-0`) and light to
`#6F737A` (4.76:1 on white, 4.57:1 on `--surface-0`), and
`token-contract.test.ts` now pins both. The most-read of those sites (speaker
meta, thought summaries, the liveness line, rail section titles) moved up to
`--ink-mid` anyway, which is what topic 01's "dimmest tone is
hairline-and-chrome, never body text" rule said all along.

**Mono discipline, principle 7, applied to the chrome people look at most.**
The model chip, the status row's percent/clock/queue figures, the rail's
relative ages and the turn footer are sans with
`font-variant-numeric: tabular-nums`, which buys the column alignment that was
the reason to reach for mono in the first place. Mono stays for code, paths,
shell summaries, diffs and the identifiers in Details.
`src/styles/faces.test.ts` pins those four rules off disk.

**The rail's three text glyphs became SVG icons.** `global.css` subsets Inter
to Latin, so the gear, magnifier and sidebar toggle set as characters were
rendering from whatever system fallback happened to carry them, at whatever
stroke weight it had, beside the app's own SVG chevrons and open-box icon.
`shell/rail/railIcons.tsx` draws all three now, the same reasoning that made
`SteeringGlyph` an SVG.

**The viewport meta no longer locks zoom.** `index.html` carried
`maximum-scale=1, user-scalable=no` to stop iOS Safari zooming into the 13px
composer field, which disabled pinch-zoom for the entire app (WCAG 1.4.4
resize text). The 16px phone body removed the reason for the lock, so the lock
is gone, and `src/styles/viewport-pin.test.ts` fails if it comes back.

**Agent prose is the document; the user's words keep the bubble.** The
2026-07-30 chat-bubbles decision put every agent fragment in a neutral ink
wash hugging its content. With the column bounded to `--session-measure` that
wash stopped doing any work and read as a slab behind every paragraph, so
`agentmessageitem.module.css`'s `.bubble` is now only the prose's layout box:
full column width, one `--rhythm-line` step of padding, no fill, no radius.
Continuation fragments take one `--rhythm-item` step above them instead of a
uniform radius. The user's own message keeps its `--accent-bg` wash
(`usermessageitem.module.css`), which a short line benefits from. This
reverses the bubble decision for the agent side only, and it is the one
2026-09-06 change that is taste rather than measurement; it is the shape
both major chat assistants converged on for long technical answers.
