# Compact Input Footer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the session footer's wrapping status area with one responsive line that removes failure and cost, groups model with effort, and keeps context and session actions visible down to a 320 px pane.

**Architecture:** Keep `SessionChrome` as the owner of the complete footer and `StatusRow` as the owner of status facts. Use one semantic context meter with wide and compact visual children, one model/effort identity cluster, and CSS container queries on `SessionChrome.body` for deterministic compression. Preserve the existing `ResizeObserver` that moves Details, Tasks, and Jobs into the actions menu below 640 px; add no JavaScript width state.

**Tech Stack:** React 19, TypeScript 6, CSS Modules, Vitest and Testing Library, headless-Chrome `layoutguard`, real-session `overflowguard`.

## Global Constraints

- Support session panes down to 320 CSS pixels.
- The complete footer must stay on one line, must not scroll horizontally, and must not extend outside its container.
- Keep model and reasoning effort as separate native/established controls inside one visual cluster.
- Keep context visible whenever `contextWindow > 0`; show a 64 px bar at 400 px and wider and an integer percentage below 400 px of `.body` width.
- At `.body` widths below 560 px, hide cumulative work time and the goal chip.
- At `.body` widths below 480 px, compact `N queued` to `QN` while retaining the full accessible phrase and tooltip.
- Remove failure count and cost from live and ended footer states without removing those fields from `ThreadModel` or other surfaces.
- Preserve the existing 640 px Details/Tasks/Jobs collapse behavior and all session-action menu contents.
- Do not add dependencies, resize listeners, viewport state, or protocol/store changes.
- Default tests must remain deterministic and offline, as required by `docs/testing.md`.
- Do not stage or alter unrelated workspace changes.

---

## File Structure

### Files to modify

- `cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.tsx` — status facts, model/effort cluster, context semantics, live/ended rendering, queue variants.
- `cmd/serf-hub/frontend/src/panes/session/chrome/statusrow.module.css` — status-row flex behavior and 560/480/400 px container-query variants.
- `cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx` — status content, accessibility, and ended-state contracts.
- `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.tsx` — full-model tooltip on a visibly truncated trigger.
- `cmd/serf-hub/frontend/src/panes/session/chrome/modelswitch.module.css` — shrinkable trigger and ellipsized model value.
- `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.test.tsx` — tooltip and accessible-name preservation.
- `cmd/serf-hub/frontend/src/panes/session/chrome/sessionchrome.module.css` — non-wrapping complete footer and `.body` query container.
- `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx` — footer composition, no-wrap, container ownership, and preserved trailing controls.
- `cmd/serf-hub/frontend/src/panes/session/chrome/goalcontrol.module.css` — hide only the inline goal anchor below 560 px.
- `cmd/serf-hub/frontend/src/panes/session/chrome/GoalControl.test.tsx` — source-level check for the container-query rule; existing behavioral tests remain authoritative.

### Files to create

- `cmd/serf-hub/frontend/scripts/layoutguard/cases/compact-session-footer/case.json` — real stylesheet and forced-state manifest.
- `cmd/serf-hub/frontend/scripts/layoutguard/cases/compact-session-footer/harness.html` — representative footer markup at all required widths.
- `cmd/serf-hub/frontend/scripts/layoutguard/cases/compact-session-footer/assert.mjs` — one-line, containment, visibility, truncation, and threshold assertions.

No production component should be split or newly created. The existing boundaries already match the design.

---

### Task 1: Simplify and Restructure Status Facts

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx:84-812`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.tsx:1-360`

**Interfaces:**
- Consumes: existing `StatusRowProps`, `ModelSwitch`, `Meter`, `ThreadModel`, `formatTokenCount`, `formatWorkDuration`, `totalWorkMillis`, and `contextTone`.
- Produces: `data-testid="status-row-identity"`, one `role="meter"` context wrapper, `data-testid="status-row-context-meter"`, `data-testid="status-row-context-percent"`, `data-testid="status-row-queue-full"`, and `data-testid="status-row-queue-compact"` for CSS and browser-guard tasks.

- [ ] **Step 1: Replace obsolete cost, failure, and ended-summary tests with the new content contract**

Keep the existing model-switch, reasoning-effort, clock, context-tone, and empty-queue tests. Delete tests whose required result is a rendered `status-row-cost`, `status-row-failures`, failure glyph, or ended work summary. Replace them with focused tests like these:

```tsx
test.each([
  { status: { type: "active" as const }, activeTurnId: "turn_1", failedToolCalls: 4, cost: "~$1.23" },
  { status: { type: "notLoaded" as const }, failedToolCalls: 4, cost: "~$1.23", workMillis: 90_000 },
])("omits failure count and cost from the footer in every lifecycle state", (overrides) => {
  render(<StatusRow sessionRef="ref_a" model={testModel(overrides)} now={1_000_000} />);
  expect(screen.queryByTestId("status-row-failures")).toBeNull();
  expect(screen.queryByTestId("status-row-cost")).toBeNull();
  expect(screen.getByTestId("status-row").textContent).not.toContain("failed");
  expect(screen.getByTestId("status-row").textContent).not.toContain("~$1.23");
});

test("groups model and effort visually while retaining two independent controls", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({
        supportsReasoning: true,
        reasoningEffortLevels: ["low", "medium", "high"],
        reasoningEffort: "medium",
      })}
      now={1_000_000}
    />,
  );
  const identity = screen.getByTestId("status-row-identity");
  expect(identity.contains(screen.getByRole("button", { name: /change model/i }))).toBe(true);
  expect(identity.contains(screen.getByRole("combobox", { name: /reasoning effort/i }))).toBe(true);
  expect(identity.textContent).toContain("·");
});

test.each(["ended", "closed", "notLoaded"] as const)(
  "a %s session keeps applicable model, effort, and context without settled work or cost",
  (type) => {
    render(
      <StatusRow
        sessionRef="ref_a"
        model={testModel({
          status: { type },
          supportsReasoning: true,
          reasoningEffortLevels: ["low", "high"],
          reasoningEffort: "high",
          contextUsed: 64_000,
          contextWindow: 128_000,
          contextPressure: 0.5,
          workMillis: 840_000,
          cost: "~$1.83",
        })}
        now={1_000_000}
      />,
    );
    expect(screen.getByTestId("model-switch-trigger")).toBeTruthy();
    expect(screen.getByRole("combobox", { name: /reasoning effort/i })).toBeTruthy();
    expect(screen.getByRole("meter", { name: /64k of 128k tokens used/i })).toBeTruthy();
    expect(screen.queryByTestId("status-row-work-time")).toBeNull();
    expect(screen.queryByTestId("status-row-cost")).toBeNull();
  },
);
```

Update the existing “no raw token counts” test so it asserts only that raw `↑/↓` usage does not render; it must no longer expect cost.

- [ ] **Step 2: Add failing tests for one context semantic and two visual variants**

Use a single accessibility owner so both CSS variants can exist in jsdom without producing duplicate meters:

```tsx
test("context has one meter semantic with wide and compact visual variants", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={runningModel({ contextUsed: 64_000, contextWindow: 128_000, contextPressure: 0.5 })}
      now={1_000_000}
    />,
  );
  const meter = screen.getByRole("meter", { name: /64k of 128k tokens used, 50 percent/i });
  expect(meter.getAttribute("aria-valuenow")).toBe("64000");
  expect(meter.getAttribute("aria-valuemax")).toBe("128000");
  expect(screen.getByTestId("status-row-context-meter").getAttribute("aria-hidden")).toBe("true");
  expect(screen.getByTestId("status-row-context-percent").textContent).toBe("50%");
  expect(screen.getByTestId("status-row-context-percent").getAttribute("aria-hidden")).toBe("true");
  expect(screen.getAllByRole("meter")).toHaveLength(1);
});
```

Retain the existing tone tests by continuing to render `Meter` inside `status-row-context-meter`.

- [ ] **Step 3: Add a failing test for full and compact queue visuals with one accessible phrase**

```tsx
test("a queued count supplies full and compact visuals through one accessible item", () => {
  render(<StatusRow sessionRef="ref_a" model={runningModel({ queue: { revision: 0, depth: 3 } })} now={1_000_000} />);
  const queue = screen.getByTestId("status-row-queue");
  expect(queue.getAttribute("aria-label")).toBe("3 queued");
  expect(queue.getAttribute("title")).toBe("3 queued");
  expect(screen.getByTestId("status-row-queue-full").textContent).toBe("3 queued");
  expect(screen.getByTestId("status-row-queue-compact").textContent).toBe("Q3");
  expect(screen.getByTestId("status-row-queue-full").getAttribute("aria-hidden")).toBe("true");
  expect(screen.getByTestId("status-row-queue-compact").getAttribute("aria-hidden")).toBe("true");
});
```

- [ ] **Step 4: Run the focused tests and verify the new assertions fail for the intended reasons**

Run:

```bash
cd cmd/serf-hub/frontend
npm test -- src/panes/session/chrome/StatusRow.test.tsx
```

Expected: failures name missing identity/context/queue test IDs and obsolete ended/cost/failure behavior until production code changes.

- [ ] **Step 5: Replace `FailureCount` and `EndedSummary` with one lifecycle-neutral status row**

In `StatusRow.tsx`:

1. Remove `FailureGlyph`, `cadenceStateForStatus`, `FailureCount`, `EndedSummary`, `ENDED_STATUSES`, `CLASS.summary`, and all cost rendering.
2. Keep `running = model.activeTurnStartedAt !== undefined`; this naturally suppresses the clock in ended states.
3. Move the separator into `ReasoningEffortControl` so it appears only when effort exists.
4. Render model and effort in one identity wrapper.
5. Build one context label and place accessibility attributes on the outer context item. Put the existing `Meter` inside an `aria-hidden` visual wrapper.
6. Render both queue visual strings under one labeled item.

The resulting structure should follow this shape:

```tsx
const contextPercent = Math.round(model.contextPressure * 100);
const contextLabel = `Context: ${formatTokenCount(model.contextUsed)} of ${formatTokenCount(model.contextWindow)} tokens used, ${contextPercent} percent`;

return (
  <div className={CLASS.row} data-testid="status-row">
    <span className={CLASS.identity} data-testid="status-row-identity">
      <ModelSwitch sessionRef={sessionRef} model={model} />
      <ReasoningEffortControl sessionRef={sessionRef} model={model} />
    </span>
    {hasContext && (
      // biome-ignore lint/a11y/useSemanticElements: a themed, responsive meter cannot use the browser-native meter presentation
      <span
        className={CLASS.context}
        data-testid="status-row-context"
        role="meter"
        aria-label={contextLabel}
        aria-valuemin={0}
        aria-valuenow={Math.min(model.contextWindow, Math.max(0, model.contextUsed))}
        aria-valuemax={model.contextWindow}
        title={`context ${formatTokenCount(model.contextUsed)} / ${formatTokenCount(model.contextWindow)}`}
      >
        <span className={CLASS.contextMeter} data-testid="status-row-context-meter" aria-hidden="true">
          <Meter
            label={contextLabel}
            value={model.contextUsed}
            max={model.contextWindow}
            tone={contextTone(model.contextPressure)}
          />
        </span>
        <span
          className={`${CLASS.contextPercent} ${CLASS.mono}`}
          data-testid="status-row-context-percent"
          aria-hidden="true"
        >
          {`${contextPercent}%`}
        </span>
      </span>
    )}
    {running && workMs > 0 && (
      <span className={`${CLASS.item} ${CLASS.mono} ${CLASS.workTime}`} data-testid="status-row-work-time">
        {formatWorkDuration(workMs)}
      </span>
    )}
    {queueDepth > 0 && (
      <span
        className={`${CLASS.item} ${CLASS.mono} ${CLASS.queue}`}
        data-testid="status-row-queue"
        aria-label={`${queueDepth} queued`}
        title={`${queueDepth} queued`}
      >
        <span className={CLASS.queueFull} data-testid="status-row-queue-full" aria-hidden="true">
          {`${queueDepth} queued`}
        </span>
        <span className={CLASS.queueCompact} data-testid="status-row-queue-compact" aria-hidden="true">
          {`Q${queueDepth}`}
        </span>
      </span>
    )}
  </div>
);
```

At the start of `ReasoningEffortControl`'s visible children, render:

```tsx
<span className={CLASS.separator} aria-hidden="true">·</span>
```

- [ ] **Step 6: Run the focused test file and make all status semantics pass**

Run:

```bash
cd cmd/serf-hub/frontend
npm test -- src/panes/session/chrome/StatusRow.test.tsx
```

Expected: PASS. If an old test still requires failures, cost, or the ended summary, update that obsolete expectation rather than restoring removed UI.

- [ ] **Step 7: Commit the status-content change**

```bash
git add -- \
  cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.tsx \
  cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx
git commit -m "refactor(webui): simplify session footer facts"
```

---

### Task 2: Implement the Responsive Single-Line Layout

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/modelswitch.module.css`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/statusrow.module.css`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx:430-488`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/sessionchrome.module.css`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/GoalControl.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/goalcontrol.module.css`

**Interfaces:**
- Consumes: Task 1's identity, context, work-time, and queue test IDs/classes; existing `SessionChrome` `.body` and `.right` structure; existing 640 px `useNarrowerThan` behavior.
- Produces: `.body` as the `inline-size` query container; deterministic 560/480/400 px variants; truncatable model trigger with full `title`; single-line footer geometry for Task 3.

- [ ] **Step 1: Add a failing ModelSwitch tooltip test**

Append to `ModelSwitch.test.tsx`:

```tsx
test("the trigger exposes the full model label when its visible value truncates", () => {
  render(<ModelSwitch sessionRef="ref_a" model={testModel({ modelProvider: "anthropic", model: "claude-opus-5" })} />);
  const trigger = screen.getByTestId("model-switch-trigger");
  expect(trigger.getAttribute("title")).toBe("anthropic/claude-opus-5");
  expect(trigger.textContent).toContain("anthropic/claude-opus-5");
});
```

Run:

```bash
cd cmd/serf-hub/frontend
npm test -- src/panes/session/chrome/ModelSwitch.test.tsx
```

Expected: FAIL because the trigger has no `title`.

- [ ] **Step 2: Add the full-value tooltip without changing the accessible name**

In `ModelSwitch.tsx`, compute the label once before the return:

```tsx
const currentModelLabel = modelLabel(model.modelProvider, model.model);
```

Use it for `title`, visible text, and `ModelCatalogPanel.value`:

```tsx
<button
  ref={triggerRef}
  type="button"
  className={CLASS.trigger}
  data-testid="model-switch-trigger"
  title={currentModelLabel}
  onClick={() => (open ? closePicker() : void openPicker())}
  disabled={disabled}
>
```

Keep the full text node in the DOM; CSS ellipsis must not shorten the accessible name.

- [ ] **Step 3: Add failing source-contract tests for no-wrap and the named container thresholds**

Replace `SessionChrome.test.tsx`'s old assertion that `.body` owns wrapping with assertions for the new contract:

```tsx
test("the chrome CSS makes body a non-wrapping inline-size query container", () => {
  const css = readFileSync(join(here, "sessionchrome.module.css"), "utf8");
  const chrome = css.match(/\.chrome \{([^}]*)\}/);
  const body = css.match(/\.body \{([^}]*)\}/);
  const right = css.match(/\.right \{([^}]*)\}/);
  expect(chrome?.[1]).toContain("flex-wrap: nowrap");
  expect(chrome?.[1]).toContain("min-width: 0");
  expect(body?.[1]).toContain("flex-wrap: nowrap");
  expect(body?.[1]).toContain("min-width: 0");
  expect(body?.[1]).toContain("container-type: inline-size");
  expect(right?.[1]).toContain("flex: none");
});
```

Add source tests in `StatusRow.test.tsx` and `GoalControl.test.tsx` only for rules jsdom cannot evaluate:

```tsx
test("status CSS declares the approved 560, 480, and 400px compression thresholds", () => {
  const css = readFileSync(join(here, "statusrow.module.css"), "utf8");
  expect(css).toContain("@container (max-width: 559px)");
  expect(css).toContain("@container (max-width: 479px)");
  expect(css).toContain("@container (max-width: 399px)");
});

test("goal CSS hides only the inline anchor below the full-row threshold", () => {
  const css = readFileSync(join(here, "goalcontrol.module.css"), "utf8");
  expect(css).toMatch(/@container \(max-width: 559px\)[\s\S]*?\.anchor\s*\{[^}]*display:\s*none/);
});
```

Import `readFileSync`, `join`, and `fileURLToPath` following the existing `SessionChrome.test.tsx` pattern where needed.

Run:

```bash
cd cmd/serf-hub/frontend
npm test -- \
  src/panes/session/chrome/SessionChrome.test.tsx \
  src/panes/session/chrome/StatusRow.test.tsx \
  src/panes/session/chrome/GoalControl.test.tsx
```

Expected: FAIL because the current `.body` and status row wrap and no container thresholds exist.

- [ ] **Step 4: Make `SessionChrome` one non-wrapping line and establish the query container**

Rewrite the relevant rules in `sessionchrome.module.css`:

```css
.chrome {
  display: flex;
  align-items: center;
  flex-wrap: nowrap;
  gap: var(--space-3);
  width: 100%;
  min-width: 0;
  overflow: hidden;
  flex: none;
}

.body {
  container-type: inline-size;
  display: flex;
  align-items: center;
  flex-wrap: nowrap;
  gap: var(--space-3);
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
}

.right {
  display: flex;
  align-items: center;
  flex-wrap: nowrap;
  gap: var(--space-2);
  flex: none;
}
```

Update stale comments in `SessionChrome.tsx`, `SessionChrome.test.tsx`, and this stylesheet: `.body` now owns compression, not wrapping. Do not change `NARROW_CHROME_WIDTH_PX`, `useNarrowerThan`, `overflowItems`, or the `.right` component tree.

- [ ] **Step 5: Add status-row flex priorities and container variants**

Replace `statusrow.module.css`'s wrapping rules and obsolete summary styles with these responsibilities:

```css
.row {
  display: flex;
  align-items: center;
  flex-wrap: nowrap;
  gap: var(--space-2);
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  color: var(--ink-mid);
}

.identity {
  display: inline-flex;
  align-items: center;
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
}

.identity > span:first-child {
  flex: 1 1 72px;
  min-width: 0;
  overflow: hidden;
}

.item,
.context,
.effortTrigger {
  flex: none;
}

.contextMeter {
  display: block;
  width: 64px;
}

.contextPercent,
.queueCompact {
  display: none;
}

.queue {
  margin-left: 0;
}

@container (max-width: 559px) {
  .workTime {
    display: none;
  }
}

@container (max-width: 479px) {
  .queueFull {
    display: none;
  }

  .queueCompact {
    display: inline;
  }
}

@container (max-width: 399px) {
  .contextMeter {
    display: none;
  }

  .contextPercent {
    display: inline;
  }
}
```

Keep the existing transparent native effort `<select>`. Change the effort focus style from an outside outline to an inset ring so `.row` clipping cannot hide it:

```css
.effortTrigger:focus-within {
  box-shadow: inset 0 0 0 2px var(--accent);
}
```

Remove obsolete `.summary` rules. Keep `.separator` for the middle dot.

- [ ] **Step 6: Make the model control shrink and truncate without clipping its focus state**

Update `modelswitch.module.css`:

```css
.trigger {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  width: 100%;
  min-width: 0;
  max-width: 100%;
  padding: var(--space-1);
  border: none;
  border-radius: var(--radius-control);
  background: transparent;
  cursor: pointer;
}

.value {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--ink-mid);
  font-family: var(--font-mono);
  font-size: var(--font-size-caption);
}

.trigger:focus-visible {
  outline: none;
  box-shadow: inset 0 0 0 2px var(--accent);
}

.chevron {
  display: inline-flex;
  align-items: center;
  flex: none;
  color: var(--ink-mid);
}
```

The Popover wrapper is `StatusRow.identity`'s first child; the Task 2 selector gives it a 72 px preferred basis and allows it to shrink below that only when required to preserve effort and context.

- [ ] **Step 7: Hide the inline goal chip below 560 px without changing goal behavior**

Append to `goalcontrol.module.css`:

```css
@container (max-width: 559px) {
  .anchor {
    display: none;
  }
}
```

Do not hide the goal dialog or remove “Set goal…” from `SessionActionsMenu`. Because only `.anchor` changes, all existing goal store, dialog, and menu behavior remains intact.

- [ ] **Step 8: Run all owning component tests**

Run:

```bash
cd cmd/serf-hub/frontend
npm test -- \
  src/panes/session/chrome/StatusRow.test.tsx \
  src/panes/session/chrome/ModelSwitch.test.tsx \
  src/panes/session/chrome/SessionChrome.test.tsx \
  src/panes/session/chrome/GoalControl.test.tsx
```

Expected: PASS. Also verify that existing `SessionChrome` tests still prove Details/Tasks/Jobs move into the menu at the unchanged 640 px observer threshold.

- [ ] **Step 9: Commit the responsive component layout**

```bash
git add -- \
  cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.tsx \
  cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.test.tsx \
  cmd/serf-hub/frontend/src/panes/session/chrome/modelswitch.module.css \
  cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx \
  cmd/serf-hub/frontend/src/panes/session/chrome/statusrow.module.css \
  cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.tsx \
  cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx \
  cmd/serf-hub/frontend/src/panes/session/chrome/sessionchrome.module.css \
  cmd/serf-hub/frontend/src/panes/session/chrome/GoalControl.test.tsx \
  cmd/serf-hub/frontend/src/panes/session/chrome/goalcontrol.module.css
git commit -m "feat(webui): keep session footer on one line"
```

---

### Task 3: Add Real-Browser Geometry Coverage

**Files:**
- Create: `cmd/serf-hub/frontend/scripts/layoutguard/cases/compact-session-footer/case.json`
- Create: `cmd/serf-hub/frontend/scripts/layoutguard/cases/compact-session-footer/harness.html`
- Create: `cmd/serf-hub/frontend/scripts/layoutguard/cases/compact-session-footer/assert.mjs`

**Interfaces:**
- Consumes: Task 2's real CSS class names and query thresholds.
- Produces: a deterministic `npm run layoutguard -- compact-session-footer` regression case covering `.body` container widths 320, 360, 400, 479, 480, 559, 560, and 900; Task 4 separately covers complete pane widths with the real-session overflowguard.

- [ ] **Step 1: Write the layoutguard manifest**

Create `case.json`:

```json
{
  "name": "compact-session-footer",
  "description": "the complete session footer stays on one line and compresses status facts in priority order",
  "cssFiles": [
    "styles/global.css",
    "panes/session/chrome/sessionchrome.module.css",
    "panes/session/chrome/statusrow.module.css",
    "panes/session/chrome/modelswitch.module.css",
    "panes/session/chrome/goalcontrol.module.css",
    "widgets/meter/meter.module.css"
  ],
  "viewport": {
    "width": 1200,
    "height": 900,
    "deviceScaleFactor": 1,
    "mobile": false
  },
  "forcePseudoStates": [
    { "selector": ".focusModel", "pseudoClasses": ["focus-visible"] }
  ]
}
```

- [ ] **Step 2: Build representative static markup at every required width**

Create `harness.html` with:

- `tokens.css` and `resolved.css` links.
- Eight fixtures whose `.body` element has `style="width: Npx; flex: none"` for `N = 320, 360, 400, 479, 480, 559, 560, 900`; size each enclosing `.chrome` to that body width plus its trailing `.right` group so each query boundary measures the intended `.body` content box.
- The real `.body`, `.cadenceSlot`, `.row`, `.identity`, model `.trigger/.value/.chevron`, effort trigger/select, context meter/percent, work time, queue full/compact, `.anchor`, and `.right` class names.
- A long model value such as `openrouter/anthropic/claude-sonnet-4.5-extended-thinking` and a long goal chip.
- Only the session-actions button in `.right` for widths below 640; Details/Tasks/Jobs plus session actions at 900, matching the existing observer behavior.
- One focused model copy carrying both `trigger` and `focusModel` classes.
- Harness-only rules only for page stacking, fixture labels, neutral button reset for stand-in trailing controls, and disabled transitions. Do not restate any layout property under test.

Define `window.measure()` to return an array with, for every width:

```js
{
  width,
  chrome: box(chrome),
  body: box(body),
  right: box(right),
  status: box(status),
  effort: visibleBox(effort),
  contextMeterDisplay: getComputedStyle(contextMeter).display,
  contextPercentDisplay: getComputedStyle(contextPercent).display,
  workDisplay: getComputedStyle(work).display,
  queueFullDisplay: getComputedStyle(queueFull).display,
  queueCompactDisplay: getComputedStyle(queueCompact).display,
  goalDisplay: getComputedStyle(goal).display,
  actions: box(actions),
  model: {
    clientWidth: modelValue.clientWidth,
    scrollWidth: modelValue.scrollWidth,
    textOverflow: getComputedStyle(modelValue).textOverflow,
    whiteSpace: getComputedStyle(modelValue).whiteSpace
  },
  focusedModelBoxShadow: getComputedStyle(focusedModel).boxShadow
}
```

Use helpers that return `left`, `right`, `top`, `bottom`, `width`, `height`, `clientWidth`, and `scrollWidth`. Read container geometry, not the intentionally ellipsized model element, when deciding whether the footer itself overflows.

- [ ] **Step 3: Write assertions for geometry and each threshold boundary**

Create `assert.mjs` that loops over measurements and fails with width-specific messages. Assert:

```js
const tolerance = 1;

if (m.chrome.scrollWidth > m.chrome.clientWidth + tolerance) fail("footer scroll width exceeds client width");
if (m.body.scrollWidth > m.body.clientWidth + tolerance) fail("status body scroll width exceeds client width");
if (Math.abs(m.body.top - m.right.top) > tolerance) fail("body and trailing controls are not on one line");
if (m.actions.right > m.chrome.right + tolerance) fail("session actions escape the footer");
if (m.effort.right > m.body.right + tolerance) fail("effort is clipped");
if (m.model.textOverflow !== "ellipsis" || m.model.whiteSpace !== "nowrap") fail("model does not ellipsize");
if (m.focusedModelBoxShadow === "none" || !m.focusedModelBoxShadow.includes("inset")) fail("focused model ring is not inset");
```

Apply exact threshold expectations:

```js
const full = m.width >= 560;
const fullQueue = m.width >= 480;
const barContext = m.width >= 400;

expectDisplay(m.workDisplay, full, "work time");
expectDisplay(m.goalDisplay, full, "goal chip");
expectDisplay(m.queueFullDisplay, fullQueue, "full queue label");
expectDisplay(m.queueCompactDisplay, !fullQueue, "compact queue label");
expectDisplay(m.contextMeterDisplay, barContext, "context meter");
expectDisplay(m.contextPercentDisplay, !barContext, "context percentage");
```

Return `{ pass: true, reason: "footer stays on one line and follows every compression threshold" }` only when no failures exist.

- [ ] **Step 4: Run the new browser case**

Run:

```bash
cd cmd/serf-hub/frontend
node scripts/layoutguard/run.mjs compact-session-footer
```

Expected: PASS.

- [ ] **Step 5: Mutation-test the guard**

Temporarily change `.body` from `flex-wrap: nowrap` to `flex-wrap: wrap` in `sessionchrome.module.css` and rerun:

```bash
cd cmd/serf-hub/frontend
node scripts/layoutguard/run.mjs compact-session-footer
```

Expected: FAIL at one or more narrow widths with a second-line or containment message. Restore the exact `flex-wrap: nowrap` declaration, rerun, and expect PASS. Confirm `git diff` contains no mutation residue.

- [ ] **Step 6: Run the full layoutguard suite**

```bash
cd cmd/serf-hub/frontend
npm run layoutguard
```

Expected: every case PASS. Investigate and fix any existing case affected by the real stylesheet changes; do not weaken its assertions.

- [ ] **Step 7: Commit the browser regression case**

```bash
git add -- cmd/serf-hub/frontend/scripts/layoutguard/cases/compact-session-footer
git commit -m "test(webui): guard compact session footer layout"
```

---

### Task 4: Verify the Real Session and Complete the Change

**Files:**
- Modify only if verification exposes a root cause in files already listed above.

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: final test evidence and a clean, reviewable diff.

- [ ] **Step 1: Run all owning component tests together**

```bash
cd cmd/serf-hub/frontend
npm test -- \
  src/panes/session/chrome/StatusRow.test.tsx \
  src/panes/session/chrome/ModelSwitch.test.tsx \
  src/panes/session/chrome/SessionChrome.test.tsx \
  src/panes/session/chrome/GoalControl.test.tsx
```

Expected: PASS with no warnings or unhandled errors.

- [ ] **Step 2: Run both browser guards at the design widths**

The static layoutguard proves the CSS thresholds. The real-session overflowguard proves that the actual React tree and reducer do not create a sideways scroll container:

```bash
cd cmd/serf-hub/frontend
npm run layoutguard
node scripts/overflowguard/run.mjs 320 360 400 479 480 559 560 900
```

Expected: all layout cases PASS and overflowguard reports no horizontal scroll containers at every width. Do not treat intentional model-label `scrollWidth > clientWidth` as a footer failure; the model value must clip with ellipsis, while the footer and pane must not become scroll containers.

- [ ] **Step 3: Run the canonical frontend gate**

From the repository root:

```bash
make test-web
```

Expected: typecheck, Vitest, and Biome all PASS.

- [ ] **Step 4: Inspect the final diff and workspace state**

```bash
git diff --check
git status --short
git log -5 --oneline
```

Expected:

- no whitespace errors
- only the planned footer implementation and guard files differ from the pre-task workspace
- no debug scripts, screenshots, generated browser profiles, or mutation residue
- the three implementation commits are present after the two already-approved design commits

- [ ] **Step 5: Commit any verification-driven correction separately**

If Steps 1–4 required a correction, stage only its owning files and commit with a message that names the root cause, for example:

```bash
git add -- cmd/serf-hub/frontend/src/panes/session/chrome/statusrow.module.css cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx
git commit -m "fix(webui): preserve compact footer containment"
```

If no correction was needed, do not create an empty commit.
