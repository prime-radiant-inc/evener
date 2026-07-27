# Web UI: the design decisions, and whether they are still true

Status: **current**. This is the blessed design's provenance — what we chose,
out of what alternatives, and whether the code does it today.

It exists because the record was scattered. Between 2026-06 and 2026-07 a
visual-brainstorming pass produced 23 mockups, each presenting four labelled
alternatives, and we picked winners and shipped them. The winners were
recorded in commit subjects and nowhere else; eight of the 23 mockups carry
an in-file `Recommendation` block, and exactly one entry in `TARGETS.md` was
ever annotated with what shipped.

Then the whole app was rewritten. The `renderer.js` / `style.css` hub those
decisions shipped into was deleted and rebuilt as the React SPA under
`cmd/serf-hub/frontend/src`. **The rewrite did not port the decisions.** It
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
| C — dimmest tone is hairline-and-chrome, never body text | LIVE | `transcript/toolcallitem.module.css:.demoted` carries the measured numbers: `--ink-low` is 2.97:1 dark / 3.64:1 light, under the 4.5:1 floor; `--ink-mid` clears it at 6.86/6.56. The same reasoning is repeated at `usermessageitem:.tag` and `chrome/taskspanel:.staleHint`. |
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
checked. It never audited Alt A's GEOMETRY: `.a-you{display:flex;gap:var(--s3);
align-items:baseline}` with `.tag{flex:none;width:40px}` (mockup 03's own CSS)
puts the tag in a fixed-width column baseline-aligned with the first line of
the message beside it — matching the golden reference's identical `.you`/`.tag`
row. What shipped instead stacked the tag in a `.header` row above the text,
a shape none of mockup 03's four alternatives actually draws (B and C read as
demotion-by-position with no stacked label at all; D's own `.d-you` is the
same flex row as A). Unaudited, not merely unmentioned — the earlier verdict's
"by design" covered the contrast inversion, not this.

`usermessageitem.module.css:.message` is now the same flex row as the
mockup's `.a-you` (kata 8v4n): `.tag` carries `flex: none; width:
var(--space-7)` (the app's space scale standing in for the mockup's
hand-measured 34/40px), and `.body` holds the gallery/text/fork-action column
beside it. Steering rendering is being worked separately and is out of scope
here, but `steeringitem.module.css`'s divider still stacks its own summary
above its body the same pre-fix way — it will need the identical column when
that work lands.

**04 · Assistant hero & reading hierarchy** — chose A (size + space) + D
(contrast), explicitly **not** C (first-sentence lede). Shipped `36989fbdc`.

| Part | Verdict | Where it stands |
| --- | --- | --- |
| D — agent prose at full contrast, user demoted | LIVE | `widgets/markdown:.root` is `--ink-hi`; `usermessageitem:.text` is `--ink-mid`. |
| C — first-sentence lede | ABSENT | Correctly rejected. Agent text is one plain `<Markdown>` block. |
| A — agent prose wins on **size and space** | **ABSENT, unexplained** | `agentmessageitem.module.css` sets no body font-size at all; `markdown:.root` and `usermessageitem:.text` both resolve to `--font-size-body`, and both carry identical padding and gap. Nothing distinguishes the two by size. This is the single decision most responsible for the transcript reading flat. |
| (all four alternatives agreed) inline code is a quiet underline, never a filled chip | **ABSENT, unexplained** | `markdown.module.css:.inlineCode` is a filled chip: `background: var(--surface-2)`, padding, radius. Every alternative in the mockup shared the opposite rule. |

**05 · Thinking block** — chose A (reserved-slot collapse) + D
(duration-weighted prominence + gist). Shipped `bd8366c30`.

Verdict **CHANGED / ABSENT, by design — closed.** Both halves were rejected
with reasoning in the code. Reserved-slot collapse lost to an always-open live
state because of `StreamingText`'s append-only contract; the gist was never
built because `<summary>` holds plain text only and a preview drawn from the
same text the disclosure reveals "is guaranteed to repeat itself the instant a
reader opens it." Deliberate simplification, recorded at the site.

**06 · Tool calls & long output** — chose A (cluster summary leads with the
mutating step) + D (peek / ride / drop). Shipped `7bbe0e91e`.

| Part | Verdict | Where it stands |
| --- | --- | --- |
| A — a run of finished calls folds to one summary line naming the consequential step | **ABSENT, unexplained** | There is no cluster concept at all. `TurnBlock` renders items one at a time via `itemRendererFor`, and `toolRowGrammar.test.tsx` pins "exactly one per call." A run of read/grep/edit/test calls is a column of individually-collapsible rows. This is also principle 2 of the brief, so its absence is a gap against the design law, not only against one mockup. |
| D — peek / ride / drop tri-state | CHANGED | **By design.** The anti-lying principle survives — `tools/helpers.ts:tailFold` and `widgets/codeblock` never offer an "expand" over bytes that are gone, and say so inline. The explicit three-state vocabulary is gone; the state is prose, not a labelled UI state. |

**07 · System churn & silent success** — chose A (quiet one-liner) + B
(coalesced "N system events"). Shipped `42b233353`.

Verdict **LIVE for both chosen alternatives.** `systemnoticeitem.module.css:.line`
is `--ink-low` with no rule or divider and no character count, citing the mockup
by number; `transcript/messages/systemGrouping.ts:shouldGroup` coalesces at three
or more, pinned by its own test. One nuance differs: the group headline names the
chronologically first event rather than the most consequential one.

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
disclosure, recursively, with lineage preserved. Verdict **partially shipped**:
recursive nesting is real in `shell/rail/railNodes.ts`, but the prototype's
centrepiece — the inactive-children disclosure — has no trace. Its production-fix
plan existed but targeted the deleted vanilla-JS files and was never re-aimed at
React.

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
- The inactive-subagents disclosure from mockup 23.

<!-- decision-tables:end -->
