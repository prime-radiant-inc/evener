Review record: preserve the initial independent assessment and its adjudication appendix. The coordinator synthesis in ../2026-09-08-iphone-ux-panel.md governs accepted priorities.

# iPhone UX review panel: navigation, IA, and everyday workflows

Scope: read-only Assessment A of the iPhone Operate/Read surface at worktree
HEAD `d683abe21`; the native runtime evidence identifies `f75411ead`. I reviewed
the current source, `mobile-native/DESIGN.md`, the mobile philosophy, style guide,
workflow studies, and the dated 2026-09-08 screenshots. This is a design review,
not an end-to-end acceptance claim.

## Heuristic scores (0 poor, 4 excellent)

| # | Heuristic | Score | Evidence / cost |
|---|---|---:|---|
| 1 | Visibility of System Status | 3 | Connected, working, question, failure and uncertain-delivery words exist; deeper queue/approval state is only visible after opening composer controls. |
| 2 | Match System / Real World | 3 | “Sessions”, “Search”, “Reconnect”, “Load older messages” and native sheets fit iPhone expectations; “project page tier” is not exposed to the person. |
| 3 | User Control and Freedom | 3 | Back, refresh, latest, restore/dismiss uncertain delivery, queue cancellation and stop are present; recovery still depends on finding the top-of-reader controls. |
| 4 | Consistency and Standards | 3 | Native navigation and ActionSheet-style message actions are coherent; the project browser and flat search use different row information and hierarchy. |
| 5 | Error Prevention | 3 | Stale reads and unconfirmed delivery disable risky actions; “Connection” and generic retry affordances do not explain the next safe step at the point of failure. |
| 6 | Recognition Rather Than Recall | 2 | Project/session titles are recognizable, but current/recent are merged and the active hub is only a small line; users must remember which hub and tier they were browsing. |
| 7 | Flexibility and Efficiency | 3 | Search, automatic paging, latest jump, retained position, drafts and queue operations support frequent use; search cannot continue beyond the 50-result cap. |
| 8 | Aesthetic and Minimalist Design | 3 | The flat, quiet canvas and generous transcript typography work; the running composer can accumulate many stacked actions and become a control dashboard. |
| 9 | Error Recovery | 3 | Reconnect, retry, refresh, delivery checking and draft retention are thoughtfully implemented; recovery is split between header, inline notices and a scroll-to-header button. |
| 10 | Help and Documentation | 2 | Labels are mostly plain language, but critical consequences such as current/recent grouping, queue semantics, and uncertain delivery are learned only from contextual copy. |
| **Total** |  | **28/40** | **Good foundation; journey-level discoverability and action hierarchy remain the main gap.** |

## Specificity and overall impression

The result feels authored for Evener in its project-first browser, hub-aware
connection line, retained reading position, and explicit uncertain-delivery
card. The visual language itself is intentionally restrained and could be
mistaken for another native developer tool; the product character comes from
the workflow semantics rather than decoration. That is appropriate for an
Operate/Read surface.

The dated screenshots show a convincing happy path: Projects are expanded in
place, search results identify the project, transcript content is the visual
center, and a message action uses a native-looking sheet. They do not show
running work, approvals, questions, queue inspection, stale reads, large text,
screen-reader order, or a physical iPhone. The JSON receipts establish useful
simulator-scoped behavior, but their native artifact/source revisions differ
from this worktree review; treat them as scoped evidence, not proof that every
current source path is accepted.

## What's working

1. The project browser gives a stable starting orientation. `ProjectSessionsList`
keeps projects and nested sessions in one scrolling surface, caps depth, and
puts a separate details affordance beside the project row
(`mobile-native/src/ProjectSessionsList.tsx:59-121`, `:313-371`). This directly
supports “find the right work” without forcing a project detail detour.
2. Reader continuity is treated as a real product requirement. The transcript
restores an anchor, loads older history when required, exposes a Latest control,
and keeps the composer draft independent of the reader
(`mobile-native/src/screens.tsx:2010-2060`, `:2163-2203`). The before/after
screenshots preserve the same transcript and separate draft while returning.
3. Uncertain sends are explained with the original text and safe next actions.
The card explicitly says to check the transcript, disables duplicate recovery
when needed, and offers Restore to draft or Dismiss
(`mobile-native/src/screens.tsx:2071-2108`). This is unusually good consequence
handling for a mobile agent client, and the simulator receipt records no
automatic resend.

## Priority findings

### [P1] The same destination changes shape between browsing and search

**Why it matters:** Search hides the project browser (`display: searchActive ?
"none" : "flex"`) and switches to a flat roster. The search screenshot shows
project and hub metadata, while the browser shows nested current/recent rows;
returning requires reconstructing the prior project context. Search is capped
at 50 results and the source explicitly tells users to narrow the query
(`mobile-native/src/screens.tsx:631-658`, `:678-710`). A person looking for a
duplicate title across hubs or beyond the first 50 cannot confidently know they
found the right work.

**Severity/confidence:** P1 / high confidence from source and screenshots.

**Next design action:** Make search an overlay/filter over the same project-aware
navigation model, or include an explicit hub, project, and recency tier in every
result plus a clear “more results unavailable” explanation. Preserve the prior
expanded project and scroll position as a visible breadcrumb when leaving search.

### [P1] Current and recent sessions are merged without a user-facing distinction

**Why it matters:** `projectBrowser.ts` concatenates `current` and `recent`,
deduplicates by `ref`, and `ProjectSessionsList.tsx` renders one sequence. There
is no “Current” or “Recent” separator in the row model or screenshot
(`mobile-native/src/projectBrowser.ts:58-83`, `mobile-native/src/ProjectSessionsList.tsx:95-121`).
The style guide says the tiers are meaningful, so users cannot predict where an
older session went or understand why a project count and visible rows differ.

**Severity/confidence:** P1 / high confidence.

**Next design action:** Keep the single scroll surface, but add lightweight
section labels or a recency marker and explain truncated/omitted descendants
next to the affected group. Keep the dedicated Project route's tabs separate as
the design document requires.

### [P1] Running work can turn the composer into a stack of competing actions

**Why it matters:** The composer can simultaneously show question count,
approval count, queue count, goal, attachment, draft, model/reasoning/vision,
command actions, send/steer, settings error, draft retry, connection/error,
stop, and queue (`mobile-native/src/screens.tsx:2263-2478`). The static reader
screen is calm, but the workflow study's running state will produce several
similarly weighted rows. On a phone this increases decision time exactly when a
person must distinguish steer, queue, stop, answer, and approve.

**Severity/confidence:** P1 / high confidence from source; running-state visual
evidence is unverified.

**Next design action:** Establish one primary state-dependent action row. Put
pending decisions in a single attention tray with counts and destination-aware
labels; move queue inspection and session management into sheets. Show Stop as a
clearly separated destructive control while work is active, and test the full
combination with large text.

### [P2] Recovery is accurate but split across locations and the “Connection” label is vague

**Why it matters:** Conversation errors appear at the list header, while the
bottom controls offer a `Connection` button that scrolls the reader to offset 0
(`mobile-native/src/screens.tsx:2441-2464`). A user who is deep in a long answer
gets no local explanation or direct reconnect target; “Connection” does not say
whether it will reconnect, show status, or inspect delivery. This is a recall
cost during a high-stakes uncertain-send or disconnect.

**Severity/confidence:** P2 / high confidence.

**Next design action:** Use a compact sticky recovery banner with the exact state
and action (“Hub disconnected · Reconnect”, “Delivery uncertain · Check
transcript”). Keep the top status for orientation, but make the local action
operate in place and preserve the reader anchor.

### [P2] Attention signals compete with project identity in the browser

**Why it matters:** The project screenshot is visually dominated by large
project names and counts. Session attention words are generated only when a
session is active, waiting, warning, or failed (`ProjectSessionsList.tsx:457-469`),
so a quiet project header gives no indication that a nested session needs a
decision until the person expands it. This undermines the workflow study's
requirement to recognize what needs attention across many projects.

**Severity/confidence:** P2 / medium-high confidence; the supplied fixture
screenshots do not include an attention-bearing row.

**Next design action:** Add a compact, text-based attention summary to the
project header when descendants need the person, such as “1 question” or “1
failed”, while preserving the quiet idle rule. Do not rely on color alone.

## Persona red flags

**Alex, power user:** Efficient paging, search, Latest, retained drafts and queue
operations are strong. The cost is mode switching: search changes the entire
information architecture, and active work can require scanning many stacked
controls before choosing queue, steer, or stop.

**Jordan, first-timer:** “Hubs” and the small connection line establish some
orientation, but a duplicate session title, opaque project names, and merged
current/recent rows do not answer “which copy should I open?” The “Connection”
button and generic “Review error” wording require interpretation during stress.

**Morgan, interrupted mobile worker:** Reader and draft persistence are strong,
including uncertain delivery. Recovery still asks Morgan to scroll to the top,
remember which message may have reached the hub, and then reconcile before
continuing; a local recovery banner would lower that burden.

## Minor observations

- The project screenshot’s right-side ellipsis is visually adjacent to the count
  but its purpose is not apparent until tapped; include the item name in the
  visible or accessible action title consistently.
- The empty-session screenshot uses `session Rmtlg1`, which is a poor human
  destination label if such names can reach production. The empty state should
  explain the next meaningful action beyond the placeholder composer.
- Search has both a keyboard Search action and a visible Search button. This is
  defensible for discoverability, but at large text sizes it should be checked
  that wrapping does not push results below the fold.
- “No current or recent sessions in this project.” is clear, while “Projects
  have changed.” needs the same explicit consequence and action in every list
  tier.

## Whole-journey conclusion and limits

The designed journey is coherent through find → open → read → return, and the
scoped simulator evidence supports project paging, reader return, draft
preservation, message actions, reconnect, and uncertain-delivery handling. It is
not yet a complete iPhone UX design for find/resume/start/send/queue/decide/
recover because the highest-cognitive-load states (simultaneous attention items,
running controls, large text, screen reader, and physical-device behavior) are
not represented by the supplied screenshots and remain unverified here. Before
calling the whole UX designed, resolve the shared search model, expose
current/recent semantics, and prototype the full running composer with pending
questions, approvals, queue, disconnect, and a newer draft in the same frame.

Questions skipped: independent review panel task; no user preference question was required for this read-only report.

## Adjudication appendix

This appendix records the follow-up source check without rewriting the
independent assessment above.

- The earlier P1 concern that search loses project context should be reduced.
  Search results include the project basename at `mobile-native/src/screens.tsx:789-798`,
  and the shared `ConnectionStatus` header keeps the selected hub visible.
  Search also hides rather than destroys the project browser, and the scoped
  project-browser receipt verifies that Clear returns the expanded project and
  all 25 loaded rows. The remaining concrete issue is narrower: two sessions
  with the same title in one hub can still be difficult to distinguish because
  the result exposes only a basename and title. Treat that as P2, with a fix of
  adding a concise differentiator such as status, branch, or human-readable
  timestamp where available. Do not redesign search solely to mirror the tree.
- The earlier P1 concern about merged current/recent tiers was overstated. These
  are backend runtime cohorts and need not be user-facing labels. The user-cost
  claim should be treated as unverified unless testing shows people need that
  distinction. The safe design action is to preserve the single hierarchy and
  expose a cohort only when it resolves a real ambiguity, such as an explicit
  “older sessions” affordance.
- Project-level attention counts are available in the protocol summary as
  `rollup_attn?: number` (`cmd/evener-hub/frontend/src/protocol/types.gen.ts:1084-1099`),
  but this review did not verify how the native decoder materializes or presents
  that field. Do not prescribe eager per-project loads. Reframe the earlier P2
  finding as an evidence gap: verify whether the existing rollup is sufficient
  to signal attention in a collapsed header, then design against that contract
  without adding list latency.
- The running-action and recovery findings remain. Their costs are grounded in
  current source (`mobile-native/src/screens.tsx:2263-2478` and `:2441-2464`),
  while their worst-case combinations remain unverified in the supplied static
  screenshots. They should be prototyped and tested in the scoped iPhone
  runtime before severity is raised.
