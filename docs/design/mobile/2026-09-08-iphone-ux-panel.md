Method: dual-agent (A: `/root/ux_panel_workflows` · B: `/root/ux_panel_visual`), both Luna medium, followed by coordinator source adjudication.

# iPhone UX review panel

8 September 2026. Scope: the existing iPhone Operate/Read design, current source
at `d683abe21`, and the native screenshots identified as `f75411ead`. Only design
documents changed under `mobile-native/` between those revisions. iPad and
dedicated accessibility work remain paused.

The [whole-app UX contract](iphone-ux-contract.md) now consolidates the designed
navigation, screen hierarchy, composition, decisions, recovery and administration.
The project browser and reader have concrete implemented screens. The complete
running/decision/recovery composition still needs a matched current-artifact
study and native interaction review. This panel does not close Task 15 or certify
the whole app.

## Verdict and evidence

The interface is specific to Evener's work: projects organize sessions, a
conversation is the main surface, and hub identity and interrupted delivery have
explicit treatment. The strongest work is project browsing, native message
actions, and continuity of reading and drafts. The biggest remaining opportunity
is making required attention and the next safe action obvious during busy or
interrupted work.

Both independent assessments scored the inspected foundation **28/40**. These
are provisional design judgments, not measured usability results. Their raw
reports preserve initial findings and subsequent corrections:
[workflow assessment](assets/2026-09-08-ux-panel-workflows.md) and
[visual assessment](assets/2026-09-08-ux-panel-visual.md). The priorities below,
rather than superseded diagnoses in those reports, govern the next design work.

| Heuristic | A | B | Interpretation after source checks |
| --- | ---: | ---: | --- |
| System status | 3 | 3 | Session signals exist; collapsed project attention is omitted |
| Familiar language | 3 | 3 | Mostly clear, with compact setting labels needing study |
| Control and freedom | 3 | 3 | Back, preserved drafts, queue controls and explicit recovery exist |
| Consistency | 3 | 3 | Native conventions and deliberate speaker distinction are strengths |
| Error prevention | 3 | 3 | Uncertain delivery is retained; combined states still need review |
| Recognition | 2 | 3 | Similar destinations and compact composer choices need differentiation |
| Efficiency | 3 | 3 | Automatic paging and return are supported; scale is unqualified |
| Minimalist presentation | 3 | 2 | Calm ordinary states; busy combinations remain a design risk |
| Recovery | 3 | 3 | Accurate safeguards, but recovery spans distant screen locations |
| Help and explanation | 2 | 2 | Some next actions and setting labels rely on prior knowledge |
| Total, out of 40 | 28 | 28 | Original independent scores; no artificial rescore after discussion |

The real Impeccable detector run exited 0 with zero findings (`[]`), so there are
no rule names or locations to report. Its web-pattern coverage does not qualify
React Native layout, keyboard behavior or physical-device fluency. Manual image
and source inspection supplied the design evidence. No browser overlay was
created for this native target.

## Priorities and design decisions

### 1. P1: Reveal required attention in collapsed projects

The native project header displays the project name and total session count
([ProjectSessionsList.tsx](../../../mobile-native/src/ProjectSessionsList.tsx),
project row around line 313). It omits the supplied `rollup_attn` summary, so the
person may have to expand projects to discover waiting decisions. The protocol
already supplies this optional count in `NavigationProjectSummary`; the browser
retains the project summary, and the web project row uses it.

Use a compact text signal such as **“2 need attention”** when the supplied count
is positive. Preserve the quiet zero state and single scrolling hierarchy. Do not
invent a question-versus-failure breakdown from an aggregate count or load every
project's sessions to compute it. Verify the rollup's server meaning and its
refresh behavior before implementation. Suggested work: Impeccable clarify.

### 2. P2: Design the complete running composer as one state

Questions, approvals, queue depth, goal, settings, delivery/error recovery and
running actions have separate render branches in
[screens.tsx](../../../mobile-native/src/screens.tsx), around lines 2263–2478.
Their simultaneous visual cost is not established by the calm reader screenshot.
The ordinary composer itself is about 12–13% of that frame; there is no evidence
to justify shrinking it arbitrarily.

Make a matched specimen with running work, a pending decision, queued messages,
an unsent draft and a recoverable failure. Keep full-width writing, separately
tappable model/reasoning choices and native action sizes. Establish a clear
state-dependent primary action; make decisions more prominent than queue/goal
metadata. Preserve distinct actions and their consequences. Confirm which
combinations can actually occur before composing the study. Suggested work:
Impeccable shape, then layout only for demonstrated crowding.

### 3. P2: Explain recovery near the person's current action

The bottom **Connection**, **Review error** and **Check delivery** controls scroll
to the transcript header, where the detailed state lives
([screens.tsx](../../../mobile-native/src/screens.tsx), around lines 2441–2464).
This preserves information but makes a returning person interpret a generic
label and leave their reading position to understand the next step.

Show a concise state and named recovery action near the composer. Keep checking
delivery distinct from reconnecting, and retain the original unconfirmed message
separately from the newer draft. Never add automatic resend. The detailed notice
can remain inspectable; opening and returning must preserve the reader anchor.
Suggested work: Impeccable clarify and harden.

### 4. P2: Make compact choices and duplicate destinations recognizable

The ordinary composer can show a model name, `(default)` and `Vision` as separate
compact choices. Search displays title and project basename; same-title sessions
in projects sharing a basename can remain ambiguous. These are narrow recognition
issues, not a reason to replace the navigation model.

In the specimen, associate each setting's value with an understandable role
without adding a permanent toolbar. For colliding search destinations, use an
available meaningful differentiator, such as the distinct parent path. Keep
search scoped to the selected hub and preserve its current Clear/return behavior.
Suggested work: Impeccable clarify, followed by one bounded polish confirmation.

## Recommendations rejected or narrowed

- **Do not replace flat search just to mirror the tree.** It already identifies
  the selected hub and project, preserves the project browser and explains its
  50-result cap. The review's initial claim of lost context was overstated.
- **Do not add Current/Recent labels just to expose backend cohorts.** No tested
  user need establishes that extra taxonomy as helpful in the main browser.
- **Preserve the native iOS header.** Its system geometry is not a defect because
  a screenshot reviewer would prefer smaller controls.
- **Preserve the accepted user-message distinction.** A surface behind user text
  is intentional. Long-message wrapping needs observation before visual changes.
- **Empty-state logic already distinguishes loading, disconnect and error.**
  “No messages yet” is the confirmed empty branch. A short invitation to start
  writing is an optional P3 copy refinement, not a broken-state diagnosis.

## People and situations to validate

- A frequent user must find the project needing attention without opening every
  group, then choose Steer, Queue or Stop without scanning several equal actions.
- A first-time user must distinguish similar destinations and understand model
  versus reasoning choices before sending work to the selected hub.
- An interrupted user must understand whether delivery is uncertain or the hub
  is disconnected, while keeping both their newer draft and reading context.

## Ordered follow-through

1. Verify project attention semantics and define the minimal row signal.
2. Capture the matched reading, keyboard, running, decision and recovery specimen
   described in the [UX contract](iphone-ux-contract.md), using realistic content
   and current source on iPhone in light and dark.
3. Review the action/recovery hierarchy against actual reachable combinations.
4. Implement confirmed gaps in a bounded batch, independently review behavior,
   and confirm touched native journeys once. Preserve the existing visual world.
5. Retain Task 14's separate physical performance measurements and Task 17's
   physical signed-build update gate. A static panel cannot replace either.

The design questions driving that specimen are which action deserves priority
when work is running, which recovery action the person should see immediately,
and what minimum context distinguishes two otherwise identical destinations.
These fit Jesse's existing direction and authorization; no routine approval pause
or new architecture choice is needed.
