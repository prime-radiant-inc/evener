# Web UI round 3: tool renderers, persistence, and the things that shouldn't need explaining

> **For agentic workers:** implement task-by-task. Steps use checkbox (`- [ ]`) syntax.
> **Do NOT run test suites.** No `vitest`, no `go test` — concurrent fleets have taken this
> machine to load 358. Write every test; the controller runs them centrally with
> `--maxWorkers=3`. Gate with `./node_modules/.bin/tsc --noEmit -p tsconfig.json` (a FAKE `tsc`
> on PATH exits 0 while printing "This is not the tsc command you are looking for") and
> `npx biome ci src`. Report a mutation list — exact file, exact edit, which test should fail —
> instead of mutation results you didn't observe. Never report a test result you did not observe.

**Goal:** the transcript reads as one coherent surface, the app remembers where you left it, and
nothing depends on the user knowing whether a session is "running".

**Tech stack:** React 19, TS 6 strict (`noUncheckedIndexedAccess`), Vite 8, vitest 4 + jsdom,
biome. Frontend root `cmd/serf-hub/frontend`.

## Global constraints

- Design tokens ONLY. `src/styles/tokens.css` is the sole color source; `token-contract.test.ts`
  fails on any hex/rgb/hsl/oklch literal elsewhere, **comments included**. Four semantic hues, one
  meaning each: `--attention` amber = a human is needed (nothing else may be amber), `--alive` =
  working, `--danger` = failure/destructive, `--accent` = focus/selection/links.
- Mono (`--font-mono`) for code, paths, timings, identifiers only. Never chrome labels.
- 4px grid (`--space-1`..`--space-7`). Hairline `--edge` borders, never shadows.
- CSS modules via the module-scope `requireClass(styles.x, "<file>.module.css", "x")` CLASS table.
  **Never `styles.x` in render.**
- A new directory under `src/widgets/` REQUIRES a matching `src/dev/gallery-sections/<name>.tsx`
  (the 1:1 guard in `dev/WidgetGallery.test.tsx` has caught this before).
- Accessibility is a requirement: every control keeps a real accessible name; icon-only controls
  keep labels; keyboard operability is not optional.
- Tests robust, not brittle: `data-testid` to FIND elements; keep ONE deliberate accessible-name
  assertion per control rather than navigating by name. Existing hooks: `turn-separator`,
  `tool-call-item`, `composer-*`, `status-row-*`, `rail-row-*`, `session-details-*`.
- Comments state WHAT and WHY, never "used to be X". **NEVER INVENT A TECHNICAL FACT.** Two
  precedents in this repo: a false CSS comment claiming `flex: 1` differs from `flex: 1 1 0`, and a
  stylesheet-grep test that passed by matching its own comment prose. Measure or read; don't
  theorize. If a test greps stylesheet text, strip comments first.
- Never `git stash` (refs/stash is SHARED across worktrees here). Never `git checkout <file>` to
  undo — use a targeted Edit. Never `git add -A`. Never `--no-verify`.
- `npx vite build` deletes tracked `dist/PLACEHOLDER` (content exactly `run make build-web\n`);
  restore with a targeted Write, never commit its deletion.

---

# Stream A — tool call renderers

Jesse: *"our tool renderers feel really clunky compared to main. they were supposed to feel
cleaner and more coherent."* Six specific complaints, all his words, each its own task.

Read first: `panes/session/transcript/ToolCallItem.tsx`, `toolRenderers.ts`, and every file in
`panes/session/transcript/tools/`. Note `toolRenderers.ts` is the dispatch table — the coherence
lever is there, not in each renderer.

### Task A1: one row grammar for every tool type

**Files:** `panes/session/transcript/ToolCallItem.tsx`, `toolcallitem.module.css`,
`tools/{shellTool,editTools,fsTools,jobTools,taskCard,useSkillTool,sandboxEscalation}.tsx`,
`tools/bodies.tsx`
**Test:** each renderer's existing `*.test.tsx`, plus a new `toolRowGrammar.test.tsx`

Jesse: *"Inconsistent between tool types"* and *"Too much vertical space per call"*.

- [ ] Read all seven renderers and write down, as a comment in the shared row component, the row
      grammar they will now share: `[failure glyph?] verb target [· meta] [affordances]`, one line.
- [ ] Extract the shared row into ONE component (`ToolRow`) that every renderer composes. DRY: if
      two renderers differ only in verb and target, they must not each own a layout.
- [ ] Collapse per-call vertical space to a single line where the tool has no inline body.
      Report measured before/after row heights from the browser.
- [ ] Each renderer keeps its own *content* decisions (what the target is, what meta it shows) and
      loses its own *layout* decisions.

**Do not** change what any renderer decides to display, with the single exception of A1b below.
Otherwise this task is layout unification only; a behavior change here would be indistinguishable
from a regression.

### Task A1b: the tool's own "why am I using this" line comes back

**Files:** shared `ToolRow` from A1, `panes/session/transcript/ToolCallItem.tsx`
**Test:** `toolRowGrammar.test.tsx`

Jesse: *"our new UI is no longer showing the 'why am i using this tool' description like it should"*.

Already root-caused, build on this: `ItemModel.description` IS populated — `protocol/reducer.ts:125`
maps the wire's `ThreadItem.description`, and `protocol/model.ts:24-26` documents it as "Tool-call
purpose … Dropped historically by wireItemToModel; now carried." But the ONLY consumer is
`tools/subagentModule.tsx:192,213` (the subagent activity feed). No main-transcript tool renderer
reads it, so the purpose line is silently dropped for every ordinary tool call.

- [ ] The shared row renders `item.description` when present. It is the agent's stated reason, so it
      is the most human-readable thing on the row — decide whether it leads or trails the
      verb/target and say why.
- [ ] Absent description → no placeholder, no empty element, no stray separator.
- [ ] Do NOT duplicate `subagentModule`'s treatment; if both surfaces want the same presentation,
      that's a shared helper, not a copy (DRY).
- [ ] Check how the legacy UI on `main` presented it (`cmd/serf-hub/assets/renderer-tools.js`)
      before designing — Jesse's "like it should" is a comparison to that.

### Task A2: a failed call is marked, and success costs no space

**Files:** the shared `ToolRow` from A1, `toolcallitem.module.css`
**Test:** `toolRowGrammar.test.tsx`

Jesse: *"bash exiting with an error shows 'exit 1' - previously we showed a small red x failure
glyph to the left of a failed tool call (without leaving space for that on a non-failed tool
call)"*.

- [ ] A failed call renders a small `--danger` ✗ glyph to the LEFT of the row.
- [ ] A successful call reserves NO space for it. This is the opposite of the rail's signal-gutter
      decision (which reserves 6px always) and it is deliberate: the rail needs a stable left edge
      down a list of siblings; a tool row is inside prose and reads better flush. Say so in a
      comment so the inconsistency is legible as a choice.
- [ ] The `exit 1` text stops being the failure signal. Keep the exit code reachable — in the
      expanded body or a `title` — but it is not the headline.
- [ ] The glyph needs a real accessible name ("failed"), not a bare character.

### Task A3: a tool call looks clickable

**Files:** shared `ToolRow`, `toolcallitem.module.css`
**Test:** `toolRowGrammar.test.tsx`

Jesse: *"tool calls have no indication that they have a click to open action"*.

- [ ] The row shows it is expandable: a disclosure affordance (chevron/triangle), `cursor` change,
      and a hover state. Check what `widgets/disclosure` already provides before inventing one.
- [ ] `aria-expanded` on the control, and the row is keyboard-operable (Enter/Space).
- [ ] Whatever you add must not reintroduce the vertical space A1 just reclaimed.

### Task A4: the expanded body stops being a heavy box

**Files:** `tools/bodies.tsx`, `RawToolOutput.tsx`, `rawtooloutput.module.css`,
`toolcallitem.module.css`
**Test:** `tools/bodies.test.tsx`, a new `rawToolOutput.test.tsx`

Jesse, on opening a bash call: *"repeats the tool call but not truncated and shows the results in a
big heavy box with what feels like a larger font size and a big heavy row for the word 'copy'
rather than an icon inset into the block. the block by default has infinite scroll rather than
wrapping nicely."*

Five distinct fixes:

- [ ] **Don't repeat the command.** The collapsed row already names it. If the untruncated form is
      needed, that is the row's own text expanding, not a second copy below it.
- [ ] **Font size:** the body should not read larger than the transcript around it. Measure the
      computed `font-size` of both and report the numbers — Jesse said "feels like", so confirm
      whether it IS larger or only reads that way, and fix whichever is true.
- [ ] **Copy affordance:** an icon inset into the block (top-right), not a full-width labelled row.
      Keep an accessible name ("Copy output"). Check whether a copy control already exists
      elsewhere in the tree before writing a new one — `DetailsPanel` was noted as wanting one too.
- [ ] **Wrap, don't scroll.** Long lines wrap. If a horizontal scroller is genuinely right for some
      content (a wide table), say which and why; the default is wrapping.
- [ ] **Weight:** hairline `--edge`, `--surface-1`/`--surface-2`, no heavy fill.

### Task A5: `read_transcript` gets its own renderer

**Files:** `toolRenderers.ts`, new `tools/readTranscript.tsx` + `.module.css`
**Test:** new `tools/readTranscript.test.tsx`

Jesse: *"read_transcript needs a custom renderer"*.

- [ ] Find the real tool name and payload shape in the Go source before designing anything. Do not
      guess at fields. If the shape isn't discoverable, STOP and report rather than inventing it.
- [ ] Register in `toolRenderers.ts` following the existing registration pattern.
- [ ] Its collapsed row should say what was read (which transcript, how much), not dump the call.

### Task A6: disclosures animate

**Files:** `widgets/disclosure/*`, `toolcallitem.module.css`, `thinkblock.module.css`
**Test:** `widgets/disclosure/disclosure.test.tsx`

Jesse: *"all the disclosure open/closes need subtle motion effects."*

- [ ] Use the existing motion tokens (`--motion-duration-*`, `--motion-easing-standard`). Do not
      add new duration values; if none fits, say so.
- [ ] **Honor `prefers-reduced-motion`** — the codebase already does this in
      `widgets/cadence/cadence.module.css`; follow that pattern.
- [ ] Subtle means subtle. Height/opacity, ~120-150ms. No slides, no bounces.
- [ ] jsdom can't verify animation; assert the declarations exist (comments stripped) and verify the
      feel in the browser.

---

# Stream B — the thinking renderer

### Task B1: thinking renders as markdown

**Files:** `panes/session/transcript/messages/ThinkBlock.tsx`
**Test:** `ThinkBlock.test.tsx`

Jesse: *"our thinking renderer seems to not treat thinking as markdown even though the agent seems
to write in markdown sometimes."* Confirmed: `ThinkBlock` renders raw `<p className={CLASS.paragraph}>`
while `AgentMessageItem.tsx:48` renders `<Markdown source={item.text} />`.

- [ ] Settled (collapsed-then-expanded) thoughts render through `<Markdown>`, same as agent messages.
- [ ] **The live streaming path is the hard part.** Read `ThinkBlock.tsx`'s header comment in full
      before touching it: it renders one `StreamingText` per `summaryIndex` specifically because
      indices can interleave, and flattening breaks `StreamingText`'s append-only invariant. A
      markdown parser needs a whole document, which fights incremental streaming. Options: keep
      `StreamingText` while live and switch to `<Markdown>` once settled (simplest, and the
      component already distinguishes those states), or parse per-paragraph. **Pick one, and
      explain the trade-off in a comment.** Do not silently break the interleaving guarantee —
      `ThinkBlock.test.tsx` has a test for it.
- [ ] Verify `<Markdown>` inherits the dimmer thinking type treatment rather than looking like an
      agent message.

---

# Stream C — persistence and shell layout

### Task C1: reload keeps tabs and sidebar

**Files:** `shell/DockHost.tsx`, `shell/workspace.ts`, `stores/prefs.ts`, `shell/rail/RailHost.tsx`
**Test:** `shell/DockHost.test.tsx`, `shell/workspace.test.ts`

Jesse: *"full page reload needs to keep state on which tabs are focused and the full open/close
state of the sidebar, etc."*

**This machinery already exists** — `DockHost.tsx:20` has `LAYOUT_STORAGE_KEY =
"serf.workspace.layout.v1"`, `persistLayout` is called on layout change, and `restoreLayout` runs
on boot. `sidebarHidden`/`sidebarWidth` are already persisted prefs. So this is very likely a BUG,
not a missing feature.

- [ ] **Diagnose before building.** Reproduce in a real browser: open several panes, focus a
      specific tab, reload. Report what actually survives and what doesn't. Candidates worth
      checking: `DockHost.tsx:280-287` runs `restoreLayout(stored)` and THEN replays routed panes
      from the URL — does that replay clobber the restored active tab? Is the active tab in the
      persisted JSON at all? Does `persistLayout` fire on a tab-focus change, or only on
      open/close/move?
- [ ] Fix the actual cause. Do not add a second persistence mechanism beside the one that exists.
- [ ] Cover: active tab within a group, multiple groups, sidebar hidden state, sidebar width.
- [ ] Note `AppShell.test.tsx`/`DockHost.test.tsx` install a `StubResizeObserver` and a
      `MemoryStorage` stand-in (Node 26 shadows jsdom's `localStorage`) — follow those patterns.

### Task C2: hide the tab bar when a group has one pane

**Files:** `shell/DockHost.tsx`, `shell/dockview-theme.css`, `panes/*/…` header if the title moves
**Test:** `shell/DockHost.test.tsx`

Jesse: *"I'm finding that I despise having the tab bar on the 'main' section of the screen"* →
chose **hide it when only one pane**. Then, asked what "never open new panes into the main group"
meant concretely: *"There should, I think, only ever be one pane in the 'main group' in the top left
(to the right of the sidebar) and other newly opened panes should generally open in a group to the
right of that group."*

So there are two rules, and they compose: the main group holds exactly one pane, therefore the main
group never shows a tab bar at all. The secondary group to its right is where everything else
stacks, and that one shows tabs (it can hold several).

- [ ] **Main group holds at most one pane.** A second pane opens in a group to the RIGHT. A third
      joins that same right-hand group rather than making a third column ("generally open in a group
      to the right", singular).
- [ ] The plumbing already exists and is one chokepoint: `workspace.ts`'s
      `openPane(type, params, opts?: {beside?})` records `beside`, and `DockHost.tsx:169` turns it
      into dockview's `position: { referencePanel: pane.beside, direction: "right" }`. Today only
      `paneActions.ts:46` passes `beside`, so everything else lands in the main group by default.
      **Put the policy in `openPane`, not at the 21 call sites** — that is the DRY seam.
- [ ] **Closing the one main pane relaunches the welcome pane there** (Jesse's call — no promotion
      from the right-hand group, no empty main slot). Note `DockHost.tsx:295` already does exactly
      this shape (`if (panes.length === 0) openPane("welcome")`) but only at BOOT; the new rule is
      per-close and scoped to the main group, not the whole workspace. Reuse rather than duplicate:
      one predicate for "the main group is empty, put welcome in it".
- [ ] Tab bar does not render for a group holding exactly one pane. Check dockview's own API for
      this before hand-rolling CSS — it may support it natively. With the rule above, the main group
      qualifies permanently and the right-hand group shows tabs whenever it has more than one.
- [ ] The main pane still needs its identity with no tab: `PaneScaffold` already draws a pane header.
      Verify the title is visible; don't leave an unlabelled pane.
- [ ] `mobile/StackHost.tsx` is a separate single-pane stack (no dockview groups) — confirm this
      change doesn't touch it, and say so.
- [ ] **No migration for existing persisted layouts** (Jesse: "do not care about current local
      storage"). Bump `LAYOUT_STORAGE_KEY` (`DockHost.tsx:20`, currently
      `"serf.workspace.layout.v1"`) to `.v2` so a pre-rule layout is simply never read — cheaper and
      more honest than a migration path for one user's stale value, and it leaves no code claiming to
      handle a shape it was never tested against. Going forward, what C1 persists must satisfy the
      one-pane rule; a restore that would violate it is a bug in C1, not a case to migrate.
- [ ] No setting. Jesse picked the automatic behavior; a toggle is YAGNI until he asks.

### Task C3: older turns auto-load

**Files:** `panes/session/transcript/flow/LoadOlderRow.tsx`, `panes/session/Session.tsx`,
`panes/session/transcript/useTranscript.ts`
**Test:** `useTranscript.test.ts`, a new `loadOlder.test.tsx`

Jesse: *"you added a button to 'load more' when scrolling, rather than auto-loading and windowing
like a proper modern app."*

Windowing already exists (`Session.tsx` uses `VirtualList` in `dynamic` mode). Only the paging
trigger is a button.

- [ ] Replace the button with an `IntersectionObserver` sentinel near the top of the list that
      fetches the next page as it approaches.
- [ ] **Preserve scroll position across the prepend** — this is the part that goes wrong. Content
      inserted above the viewport must not jump the reader. `VirtualList` exposes an imperative
      handle (`getScrollElement`/`scrollToIndex`, see `Session.tsx:118-123`); use it.
- [ ] Don't fire overlapping fetches, and don't re-fetch when `olderCursor` is exhausted.
- [ ] Keep a visible loading indicator while a page is in flight.
- [ ] Jesse chose no fallback button. If a fetch FAILS, decide what the recovery is (retry on next
      scroll? an inline error with a retry?) and say what you chose — silent failure is not an option.
- [ ] jsdom has no `IntersectionObserver`; stub it the way `StubResizeObserver` is stubbed.

---

# Stream D — rail and model switching

### Task D1: the rail's chevron gets a reserved gutter

**Files:** `shell/rail/RailRow.tsx`, `shell/rail/Rail.module.css`
**Test:** `shell/rail/RailRow.test.tsx`

Jesse: *"in the session sidebar, some sessions (with children?) seem to get a weird extra indent"*.
His diagnosis is right. `RailRow.tsx:333` and `:394` render `{info.hasChildren && <Chevron …/>}`,
so a row WITH children gains a chevron's width and a row without gains nothing — the titles don't
line up.

- [ ] Reserve the chevron's slot unconditionally, empty when a row has no children. This is exactly
      the fix already applied to the signal gutter in the same file (`Signal`, always-rendered 6px);
      reuse that shape rather than inventing a second one. **DRY:** consider whether one
      leading-gutter component can serve both.
- [ ] Verify in the browser that every title in a mixed tree shares one x-position, at every
      nesting depth. Report measured `titleX` values.
- [ ] Do not change `.rail`'s width/containment CSS (`flex: none` + `width: var(--rail-width)` +
      `min-width: 0`) or the hover-revealed `⋯` overlay. Both were fixed deliberately; read their
      comments.

### Task D2: the model can be changed on any session

**Files:** `panes/session/chrome/ModelSwitch.tsx`, `stores/threads.ts`
**Test:** `ModelSwitch.test.tsx`

Jesse: *"when a session isn't active, it's impossible to change the model for its next turn. from a
user perspective, they should not need to know or care about whether a session is 'running' or not."*
Chose: **applies to the next turn, no resume.**

`ModelSwitch.tsx:64` is `const disabled = busy || !model.capabilities.changeModel;`. I checked the
hub: `app_threadread.go:247` sets `ChangeModel: true` for an exited session. So the capability is
there and the client is refusing anyway.

- [ ] Drop `busy` from the gate. Keep the capability check — that's the wire's own answer.
- [ ] **This is the fourth instance of one pattern today:** a client-side predicate answering
      "is a turn in flight" was used to gate something that is not about the current turn. The
      composer's follow-up card, its Send button, and its submit route all had it. Check whether any
      OTHER control gates on `isTurnActive` when it shouldn't, and report the list — do not fix them
      in this task, but I want to know.
- [ ] Decide and document where the choice LIVES for an unresumed session: does `setModel` succeed
      against a cold session, or does the client hold it until the next send? Read
      `stores/threads.ts`'s `setModel` and the hub handler before choosing. If it can't be recorded
      without a resume, the honest behavior is to hold it client-side and apply it on send — say
      which you implemented.
- [ ] A failure must surface as a toast, not a silent no-op.
- [ ] Interrupt/steer stay gated on `busy` — those genuinely require a live turn.

---

## Sequencing

- **A1 first**, then A1b/A2/A3/A4/A5/A6 (they all build on the shared row). A1 is the DRY move; doing
  the others first means undoing them.
- **B1, C3, D1, D2 are independent** of stream A and of each other.
- **C1 → C2, in that order, same agent.** Both touch `DockHost.tsx`, and C2's one-pane-per-main-group
  rule changes what a valid persisted layout even is — so the persistence bug has to be understood
  before the layout policy lands on top of it.
- One agent per stream, sequentially per the no-parallel-tests rule; A's tasks are one agent.

## Out of scope (noted, not doing)

- `transcript.hookExitsAll` / `hookExitsNormal` / `promptLoaded` prefs have no consumers and no
  features behind them — "pref shipped before the feature." Needs a build-or-delete decision.
- Turns now have no visual separation when all three meta figures are off (`TurnSeparator` returns
  null). May want a hairline independent of those prefs.
- `widgets/tooltip` has no collision handling; Send's tooltip lands 2px from the viewport edge.
- `widgets/popover` measures mid-animation, spilling ~7px at 390px.
- Spawn's placeholder promises "Leave blank to start it dormant" but a guard refuses an empty
  prompt; the daemon supports it. Delete the guard or the copy.
- One session can appear twice in the rail (Live + its project).
