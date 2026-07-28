# Transcript Tiered Density Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the session transcript's visual hierarchy around three density tiers (conversation / activity / meta) per `docs/web-ui/specs/2026-07-27-transcript-tiered-density-design.md`.

**Architecture:** No new components. Each existing transcript item renderer is adjusted to its tier: agent prose drops to body size, user/agent voices get stacked speaker eyebrows at exchange boundaries, the think block shows its preview only when collapsed, settled tool calls compose to one line, and meta items (notifications, round timings, turn failures) lose their card/box chrome by default.

**Tech Stack:** React 19 + TypeScript, CSS Modules, Vitest + Testing Library, Biome. All work in `cmd/serf-hub/frontend` of the `transcript-view-design` worktree.

## Global Constraints

- Worktree: `/Users/jesse/prime-radiant/toil-suite/serf/.claude/worktrees/transcript-view-design`, branch `transcript-view-design`. All commands run from `cmd/serf-hub/frontend` unless noted.
- No new colours, no new type sizes, no new motion (spec §Non-goals).
- Colour allowlist: non-widget stylesheets may not reference `--danger`/`--attention`/`--alive`; tone comes from `Chip`/`FailureGlyph` widgets only (`styles/token-contract.test.ts` enforces).
- Eyebrow idiom: `--font-size-caption`, `--font-weight-medium`, `--ink-low`, sentence case, no uppercase transform (design-system.md §Type).
- Tests stay deterministic per root `AGENTS.md`: no live provider, no network.
- Test command for one file: `npx vitest run <path-from-frontend-dir>`. Whole suite: `npm test`. Types: `npm run typecheck`. Lint: `npm run lint`. Guards: `npm run layoutguard && npm run overflowguard`.
- Spec evidence screenshots ("before" set): `docs/web-ui/specs/assets/2026-07-27-transcript-tiered-density/`.

---

### Task 1: Agent prose drops to body size

**Files:**
- Modify: `src/panes/session/transcript/messages/agentmessageitem.module.css:17-20`
- Test: `src/panes/session/transcript/messages/agentMessageSize.contract.test.ts` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: `.message` no longer sets `--prose-font-size`; downstream Tasks 2/5 assume agent and user prose share one size ramp.

- [ ] **Step 1: Write the failing contract test**

The repo already tests CSS statically (`styles/token-contract.test.ts`); do the same here. Create `src/panes/session/transcript/messages/agentMessageSize.contract.test.ts`:

```ts
// Contract test for the tiered-density spec (docs/web-ui/specs/
// 2026-07-27-transcript-tiered-density-design.md, ratification item 1):
// agent prose wins on CONTRAST (ink-hi vs the user's ink-mid), not on size.
// The 16px pane-title override fired on every narrative fragment, dozens
// per session, cancelling the signal it was meant to be.
import { readFileSync } from "node:fs";

const css = readFileSync(new URL("./agentmessageitem.module.css", import.meta.url), "utf8");

test("agent prose is not size-promoted above body text", () => {
  expect(css).not.toContain("--prose-font-size");
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx vitest run src/panes/session/transcript/messages/agentMessageSize.contract.test.ts`
Expected: FAIL — the stylesheet still contains `--prose-font-size`.

- [ ] **Step 3: Remove the size override**

In `agentmessageitem.module.css`, replace the `.message` block and rewrite the file header comment:

```css
/* Layout/spacing only - typography is owned entirely by Markdown's and
 * StreamingText's own stylesheets (both already source font-family/
 * line-height/color from the same tokens), so the live and settled paths
 * can never visually mismatch.
 *
 * Agent prose is body-size like everything else (tiered-density spec,
 * 2026-07-27): the hierarchy against the user's ink-mid text is carried
 * by CONTRAST alone (Markdown's root is --ink-hi). The earlier pane-title
 * size bump fired on every mid-turn narrative fragment, dozens per
 * session, and cancelled itself. */
.message {
  padding: var(--space-2) 0;
}
```

- [ ] **Step 4: Run test to verify it passes, then the neighbouring suites**

Run: `npx vitest run src/panes/session/transcript/messages/agentMessageSize.contract.test.ts src/panes/session/transcript/messages/AgentMessageItem.test.tsx`
Expected: PASS (both).

- [ ] **Step 5: Commit**

```bash
git add src/panes/session/transcript/messages/agentmessageitem.module.css src/panes/session/transcript/messages/agentMessageSize.contract.test.ts
git commit -m "transcript: agent prose at body size (tiered density task 1)"
```

---

### Task 2: User message eyebrow replaces the inline "You" gutter

**Files:**
- Modify: `src/panes/session/transcript/messages/UserMessageItem.tsx:111-134`
- Modify: `src/panes/session/transcript/messages/usermessageitem.module.css` (whole file)
- Test: `src/panes/session/transcript/messages/UserMessageItem.test.tsx`

**Interfaces:**
- Consumes: nothing.
- Produces: `UserMessageView` renders `.header` (eyebrow "You" + actions) stacked above `.body`; `.tag` class is gone. `SteeringItem.tsx` reuses `UserMessageView` — its callers need no change (the eyebrow renders for it too, matching its current always-labelled behaviour).

- [ ] **Step 1: Write the failing tests**

In `UserMessageItem.test.tsx`, add (matching the file's existing render helpers):

```tsx
test("renders a stacked eyebrow, not an inline gutter tag", () => {
  render(<UserMessageView item={userItem("hello world")} />);
  const root = screen.getByTestId("user-message-item");
  // eyebrow is the first child, stacked above the body
  const header = root.firstElementChild;
  expect(header).not.toBeNull();
  expect(header!.textContent).toBe("You");
  // the old fixed-width gutter tag is gone
  expect(root.querySelector("[class*=tag]")).toBeNull();
});

test("actions live in the eyebrow header row", () => {
  render(<UserMessageView item={userItem("hello")} actions={<button type="button">act</button>} />);
  const root = screen.getByTestId("user-message-item");
  const header = root.firstElementChild as HTMLElement;
  expect(header.contains(screen.getByRole("button", { name: "act" }))).toBe(true);
});
```

(If the existing test file names its factory differently — e.g. `makeItem` — use that name; the assertions stand.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `npx vitest run src/panes/session/transcript/messages/UserMessageItem.test.tsx`
Expected: FAIL — the current render has a `.tag` span and no header row.

- [ ] **Step 3: Restructure UserMessageView**

In `UserMessageItem.tsx`, update the CLASS map (replace `tag` with `header` and `eyebrow`):

```tsx
const CLASS = {
  message: requireClass(styles.message, "usermessageitem.module.css", "message"),
  header: requireClass(styles.header, "usermessageitem.module.css", "header"),
  eyebrow: requireClass(styles.eyebrow, "usermessageitem.module.css", "eyebrow"),
  body: requireClass(styles.body, "usermessageitem.module.css", "body"),
  actions: requireClass(styles.actions, "usermessageitem.module.css", "actions"),
  text: requireClass(styles.text, "usermessageitem.module.css", "text"),
};
```

and the view body:

```tsx
export function UserMessageView({
  item,
  actions,
  opensExchange = true,
}: {
  item: ItemModel;
  actions?: ReactNode;
  opensExchange?: boolean;
}) {
  return (
    <div
      className={CLASS.message}
      data-testid="user-message-item"
      data-opens-exchange={opensExchange ? "true" : undefined}
    >
      <div className={CLASS.header}>
        <span className={CLASS.eyebrow}>You</span>
        {actions !== undefined && <div className={CLASS.actions}>{actions}</div>}
      </div>
      <div className={CLASS.body}>
        <ImageGallery images={item.images} />
        <div className={CLASS.text}>{item.text}</div>
      </div>
    </div>
  );
}
```

Then rewrite `usermessageitem.module.css`:

```css
/* Quiet, demoted treatment (design-system.md mockup #3: "the user knows
 * what they said"). The speaker is a stacked caption eyebrow, not an
 * inline gutter tag (tiered-density spec, 2026-07-27, ratification item
 * 3): at a 76rem measure the 40px gutter column left the tag floating
 * disconnected from its text, and the geometry matched nothing else in
 * the transcript. The eyebrow is the design system's sanctioned short-
 * label idiom: caption size, medium weight, ink-low, sentence case. */
.message {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  padding: var(--space-2) 0;
}

/* The exchange boundary, and the transcript's only structural break.
 * (Rationale for 32px of space rather than a hairline rule is unchanged;
 * see git history for the measured contrast numbers.) Fires only on
 * exchange openers - a turn is one LLM round-trip, and marking each
 * would slice one continuous piece of agent work into arbitrary slabs. */
.message[data-opens-exchange] {
  margin-top: var(--space-6);
}

.header {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
}

.eyebrow {
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  font-weight: var(--font-weight-medium);
  color: var(--ink-low);
}

/* Hover/focus-revealed, same instant-swap idiom as before, now anchored
 * to the header row's trailing edge instead of floating beside the
 * message's first line. */
.actions {
  margin-left: auto;
  display: flex;
  gap: var(--space-1);
  opacity: 0;
}

.message:hover .actions,
.message:focus-within .actions {
  opacity: 1;
}

.body {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}

.text {
  white-space: pre-wrap;
  overflow-wrap: break-word;
  font-family: var(--font-sans);
  font-size: var(--font-size-body);
  line-height: var(--line-height-body);
  color: var(--ink-mid);
}
```

- [ ] **Step 4: Run the user + steering suites**

`SteeringItem.tsx` renders `UserMessageView` — its suite must pass with the new structure.

Run: `npx vitest run src/panes/session/transcript/messages/UserMessageItem.test.tsx src/panes/session/transcript/messages/SteeringItem.test.tsx`
Expected: PASS. If a SteeringItem test asserted the old `.tag` class, update the assertion to the eyebrow (same visible text "You").

- [ ] **Step 5: Commit**

```bash
git add src/panes/session/transcript/messages/UserMessageItem.tsx src/panes/session/transcript/messages/usermessageitem.module.css src/panes/session/transcript/messages/UserMessageItem.test.tsx src/panes/session/transcript/messages/SteeringItem.test.tsx
git commit -m "transcript: stacked 'You' eyebrow replaces inline gutter tag (task 2)"
```

---

### Task 3: `exchangeOpenersFor` helper

**Files:**
- Create: `src/panes/session/transcript/exchangeOpeners.ts`
- Test: `src/panes/session/transcript/exchangeOpeners.test.ts` (create)

**Interfaces:**
- Consumes: `TurnModel` from `src/protocol/model`.
- Produces: `exchangeOpenersFor(turns: TurnModel[]): ReadonlySet<string>` — ids of `agentMessage` items that are the first agent message after a `userMessage`, scanning turns in order. Tasks 4 and 5 consume this.

- [ ] **Step 1: Write the failing tests**

Create `src/panes/session/transcript/exchangeOpeners.test.ts`:

```ts
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { exchangeOpenersFor } from "./exchangeOpeners";

function item(id: string, type: string): ItemModel {
  return { id, type, text: id, status: "completed" } as ItemModel;
}

function turn(id: string, items: ItemModel[]): TurnModel {
  return { id, items, status: "completed" } as TurnModel;
}

test("first agent message after a user message opens the agent's exchange half", () => {
  const openers = exchangeOpenersFor([
    turn("t1", [item("u1", "userMessage"), item("a1", "agentMessage"), item("a2", "agentMessage")]),
  ]);
  expect([...openers]).toEqual(["a1"]);
});

test("queued user messages: the opener is still the first agent reply", () => {
  const openers = exchangeOpenersFor([
    turn("t1", [item("u1", "userMessage")]),
    turn("t2", [item("u2", "userMessage"), item("a1", "agentMessage")]),
  ]);
  expect([...openers]).toEqual(["a1"]);
});

test("agent messages before any user message open nothing", () => {
  const openers = exchangeOpenersFor([turn("t1", [item("a1", "agentMessage")])]);
  expect(openers.size).toBe(0);
});

test("each new exchange gets its own opener, across turns", () => {
  const openers = exchangeOpenersFor([
    turn("t1", [item("u1", "userMessage"), item("a1", "agentMessage")]),
    turn("t2", [item("a2", "agentMessage")]),
    turn("t3", [item("u2", "userMessage")]),
    turn("t4", [item("a3", "agentMessage"), item("a4", "agentMessage")]),
  ]);
  expect([...openers]).toEqual(["a1", "a3"]);
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx vitest run src/panes/session/transcript/exchangeOpeners.test.ts`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement the helper**

Create `src/panes/session/transcript/exchangeOpeners.ts`:

```ts
// Exchange-open detection for the speaker-eyebrow treatment (tiered-density
// spec, 2026-07-27). An EXCHANGE is one thing the user asked plus everything
// the agent did about it; the eyebrow fires on the first agentMessage item
// after each userMessage, scanning turns in wire order. Computed once per
// transcript model at the Session level - a TurnBlock renders one turn in
// isolation and cannot see this relation across turn boundaries.
import type { TurnModel } from "../../../protocol/model";

export function exchangeOpenersFor(turns: TurnModel[]): ReadonlySet<string> {
  const openers = new Set<string>();
  let awaitingAgent = false;
  for (const turn of turns) {
    for (const item of turn.items) {
      if (item.type === "userMessage") {
        // Queued/steered user messages before any reply do not each open an
        // exchange - the first agent reply still owns the one opener slot.
        awaitingAgent = true;
      } else if (item.type === "agentMessage") {
        if (awaitingAgent) openers.add(item.id);
        awaitingAgent = false;
      }
    }
  }
  return openers;
}
```

(Same import depth as the neighbouring `types.ts`: `transcript/` is three levels below `src/`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `npx vitest run src/panes/session/transcript/exchangeOpeners.test.ts`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add src/panes/session/transcript/exchangeOpeners.ts src/panes/session/transcript/exchangeOpeners.test.ts
git commit -m "transcript: exchangeOpenersFor helper (task 3)"
```

---

### Task 4: `ItemRenderProps` gains `opensExchange`/`agentLabel`; TurnBlock threads them

**Files:**
- Modify: `src/panes/session/transcript/types.ts` (ItemRenderProps + ignoringTurn)
- Modify: `src/panes/session/transcript/TurnBlock.tsx:26-38,102-106`
- Test: `src/panes/session/transcript/types.test.ts`
- Test: `src/panes/session/transcript/TurnBlock.test.tsx`

**Interfaces:**
- Consumes: `exchangeOpenersFor` output shape from Task 3 (a `ReadonlySet<string>`).
- Produces: `ItemRenderProps.opensExchange?: boolean`, `ItemRenderProps.agentLabel?: string`; `TurnBlockProps.exchangeOpeners?: ReadonlySet<string>`, `TurnBlockProps.agentLabel?: string`. Task 5 consumes both.

- [ ] **Step 1: Write the failing tests**

In `types.test.ts`, add:

```ts
test("ignoringTurn re-renders when opensExchange or agentLabel changes", () => {
  const base = { item: {} as never, turn: {} as never, live: false };
  expect(ignoringTurn(base, { ...base, opensExchange: true })).toBe(false);
  expect(ignoringTurn(base, { ...base, agentLabel: "k3" })).toBe(false);
  expect(ignoringTurn({ ...base, opensExchange: true }, { ...base, opensExchange: true })).toBe(true);
});
```

In `TurnBlock.test.tsx`, add (adapt to the file's existing `turnWith` helper and a spy renderer registered via `registerItemRenderer`, the pattern its registry tests already use):

```tsx
test("passes opensExchange and agentLabel through ItemRenderProps", () => {
  const seen: Array<{ opensExchange?: boolean; agentLabel?: string }> = [];
  registerItemRenderer("agentMessage", (props) => {
    seen.push({ opensExchange: props.opensExchange, agentLabel: props.agentLabel });
    return null;
  });
  const agentItem = { id: "a1", type: "agentMessage", text: "hi", status: "completed" };
  render(
    <TurnBlock
      turn={turnWith([agentItem])}
      exchangeOpeners={new Set(["a1"])}
      agentLabel="k3"
    />,
  );
  expect(seen).toEqual([{ opensExchange: true, agentLabel: "k3" }]);
});
```

(If `TurnBlock.test.tsx` already registers a test renderer for `agentMessage`, reuse its registration helper instead of adding a second one. Registry is module-global — restore the real renderer afterward if the file has a save/restore pattern; follow it.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `npx vitest run src/panes/session/transcript/types.test.ts src/panes/session/transcript/TurnBlock.test.tsx`
Expected: FAIL — `opensExchange`/`agentLabel` are not in the props type and not passed.

- [ ] **Step 3: Extend the registry contract and TurnBlock**

In `types.ts`, extend the interface and comparator:

```ts
export interface ItemRenderProps {
  item: ItemModel;
  turn: TurnModel;
  live: boolean;
  // The owning session's ref, threaded from Session.tsx via TurnBlock so an
  // item renderer can scope a disclosure or action to the owning session.
  sessionRef?: string;
  // True on the first agentMessage item of an exchange (exchangeOpeners.ts),
  // threaded from Session.tsx via TurnBlock. Only renderers with a speaker
  // eyebrow read it; absent/false everywhere else.
  opensExchange?: boolean;
  // The session's short provider/model label (statusFormat.ts's modelLabel),
  // for the agent eyebrow. Absent in the read-only "open beside" pane.
  agentLabel?: string;
}
```

and in `ignoringTurn`, add the two fields to the comparison (a stale `true` would pin an eyebrow onto the wrong render):

```ts
export function ignoringTurn(prev: ItemRenderProps, next: ItemRenderProps): boolean {
  return (
    prev.item === next.item &&
    prev.live === next.live &&
    prev.sessionRef === next.sessionRef &&
    prev.opensExchange === next.opensExchange &&
    prev.agentLabel === next.agentLabel
  );
}
```

In `TurnBlock.tsx`, extend props and the render call:

```tsx
export interface TurnBlockProps {
  turn: TurnModel;
  sessionRef?: string;
  showSeenDivider?: boolean;
  // exchangeOpeners.ts's set for the whole transcript, threaded from
  // Session.tsx; membership marks this turn's exchange-opening agent item.
  exchangeOpeners?: ReadonlySet<string>;
  // statusFormat.ts's modelLabel for the agent eyebrow.
  agentLabel?: string;
}
```

```tsx
export function TurnBlock({ turn, sessionRef, showSeenDivider = false, exchangeOpeners, agentLabel }: TurnBlockProps) {
  // ...
  const ItemRenderer = itemRendererFor(item.type);
  return (
    <ItemRenderer
      key={item.id}
      item={item}
      turn={shownTurn}
      live={isItemLive(item)}
      sessionRef={sessionRef}
      opensExchange={exchangeOpeners?.has(item.id)}
      agentLabel={agentLabel}
    />
  );
}
```

- [ ] **Step 4: Run the transcript suites**

Run: `npx vitest run src/panes/session/transcript/types.test.ts src/panes/session/transcript/TurnBlock.test.tsx`
Expected: PASS. Then run the whole transcript folder to catch comparator side effects:

Run: `npx vitest run src/panes/session/transcript`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/panes/session/transcript/types.ts src/panes/session/transcript/TurnBlock.tsx src/panes/session/transcript/types.test.ts src/panes/session/transcript/TurnBlock.test.tsx
git commit -m "transcript: thread opensExchange/agentLabel through ItemRenderProps (task 4)"
```

---

### Task 5: Agent eyebrow + Session/Transcript wiring

**Files:**
- Modify: `src/panes/session/transcript/messages/AgentMessageItem.tsx` (whole file)
- Modify: `src/panes/session/transcript/messages/agentmessageitem.module.css`
- Modify: `src/panes/session/Session.tsx:235-238` (renderRow) plus imports and two `useMemo` lines near line 171-176
- Modify: `src/panes/transcript/Transcript.tsx:141` (renderRow)
- Test: `src/panes/session/transcript/messages/AgentMessageItem.test.tsx`

**Interfaces:**
- Consumes: `ItemRenderProps.opensExchange`/`agentLabel` (Task 4), `exchangeOpenersFor` (Task 3), `modelLabel(modelProvider, model)` from `src/panes/session/chrome/statusFormat.ts:55`.
- Produces: visible "Agent · {label}" eyebrow on exchange-opening agent items; `.srOnly` class removed.

- [ ] **Step 1: Write the failing tests**

In `AgentMessageItem.test.tsx`, add:

```tsx
test("exchange-opening agent message shows a visible eyebrow with the model label", () => {
  render(
    <AgentMessageItem
      item={settledAgentItem("the reply")}
      turn={turn}
      live={false}
      opensExchange
      agentLabel="k3"
    />,
  );
  const root = screen.getByTestId("agent-message-item");
  expect(root.dataset.opensExchange).toBe("true");
  expect(root.firstElementChild!.textContent).toBe("Agent · k3");
});

test("continuation fragments render no eyebrow", () => {
  render(<AgentMessageItem item={settledAgentItem("more")} turn={turn} live={false} />);
  const root = screen.getByTestId("agent-message-item");
  expect(root.dataset.opensExchange).toBeUndefined();
  expect(root.textContent).not.toContain("Agent");
});
```

(Adapt factory names to the file's existing helpers; the second test's `not.toContain("Agent")` assumes the helper's text doesn't itself contain that word — use "more work" as the body.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `npx vitest run src/panes/session/transcript/messages/AgentMessageItem.test.tsx`
Expected: FAIL — no eyebrow, `data-opens-exchange` unset.

- [ ] **Step 3: Implement the eyebrow and wire the panes**

`AgentMessageItem.tsx` — CLASS map (replace `srOnly` with `eyebrow`) and component:

```tsx
const CLASS = {
  message: requireClass(styles.message, "agentmessageitem.module.css", "message"),
  eyebrow: requireClass(styles.eyebrow, "agentmessageitem.module.css", "eyebrow"),
};

export const AgentMessageItem = memo(function AgentMessageItem({ item, live, opensExchange, agentLabel }: ItemRenderProps) {
  // The speaker eyebrow fires at exchange boundaries only (exchangeOpeners.ts):
  // continuation fragments inside one exchange are continuous work, not new
  // voices. Replaces the old SR-only "Agent" span - the visible eyebrow is
  // also the screen-reader label, in linear reading order.
  const eyebrow = opensExchange ? (
    <div className={CLASS.eyebrow}>{agentLabel ? `Agent · ${agentLabel}` : "Agent"}</div>
  ) : null;
  if (live) {
    const chunks = item.pendingText;
    if (!chunks || chunks.length === 0) return null;
    return (
      <div className={CLASS.message} data-testid="agent-message-item" data-live="true" data-opens-exchange={opensExchange ? "true" : undefined}>
        {eyebrow}
        <StreamingText chunks={chunks} />
      </div>
    );
  }
  if (!item.text) return null;
  return (
    <div className={CLASS.message} data-testid="agent-message-item" data-live="false" data-opens-exchange={opensExchange ? "true" : undefined}>
      {eyebrow}
      <Markdown source={item.text} />
    </div>
  );
}, ignoringTurn);
```

`agentmessageitem.module.css` — delete the `.srOnly` block (lines 34-44) and add:

```css
/* The agent's speaker eyebrow (tiered-density spec, 2026-07-27): same
 * sanctioned idiom as the user's - caption, medium, ink-low, sentence
 * case. Fires at exchange boundaries only. */
.eyebrow {
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  font-weight: var(--font-weight-medium);
  color: var(--ink-low);
}
```

`Session.tsx` — add imports and compute, next to the existing hooks (~line 175):

```tsx
import { exchangeOpenersFor } from "./transcript/exchangeOpeners";
import { modelLabel } from "./chrome/statusFormat";
// ...
const openers = useMemo(() => (model ? exchangeOpenersFor(model.turns) : undefined), [model]);
const agentLabel = model ? modelLabel(model.modelProvider, model.model) : undefined;
```

and in renderRow (line 235-238):

```tsx
renderRow={(index) => {
  const t = turnAt(index);
  return (
    <TurnBlock
      turn={t}
      sessionRef={ref}
      showSeenDivider={t.id === seenDividerTurnId}
      exchangeOpeners={openers}
      agentLabel={agentLabel}
    />
  );
}}
```

`Transcript.tsx` (read-only pane, line 141) — pass openers so the eyebrow also appears there; no `agentLabel` (eyebrow falls back to "Agent"):

```tsx
renderRow={(index) => <TurnBlock turn={turnAt(index)} exchangeOpeners={openers} />}
```

with `const openers = useMemo(() => exchangeOpenersFor(model.turns), [model]);` and the matching import in that file.

- [ ] **Step 4: Run the affected suites and typecheck**

Run: `npx vitest run src/panes/session/transcript/messages/AgentMessageItem.test.tsx src/panes/session/Session.test.tsx src/panes/session/transcript`
Expected: PASS.

Run: `npm run typecheck`
Expected: clean (check `Transcript.tsx`'s `model` is in scope at the renderRow — if the file names it differently, use its name).

- [ ] **Step 5: Commit**

```bash
git add src/panes/session/transcript/messages/AgentMessageItem.tsx src/panes/session/transcript/messages/agentmessageitem.module.css src/panes/session/transcript/messages/AgentMessageItem.test.tsx src/panes/session/Session.tsx src/panes/transcript/Transcript.tsx
git commit -m "transcript: visible agent eyebrow at exchange boundaries (task 5)"
```

---

### Task 6: ThinkBlock preview only when collapsed

**Files:**
- Modify: `src/panes/session/transcript/messages/ThinkBlock.tsx:121-163`
- Test: `src/panes/session/transcript/messages/ThinkBlock.test.tsx`

**Interfaces:**
- Consumes: nothing new (uses the existing `disclosureStore` state already read in the settled branch).
- Produces: open think blocks show the short label `Thought [for Ns]`; closed blocks keep `Thought [for Ns] · <preview>`. Fixes the right-column squeeze (spec §Tier 2, root cause `thinkblock.module.css:64-68` + 120-char nowrap summary).

- [ ] **Step 1: Write the failing test**

In `ThinkBlock.test.tsx`, add (adapt to the file's settled-item factory and its disclosureStore reset pattern):

```tsx
test("opening the disclosure drops the preview from the summary", () => {
  const item = settledReasoningItem(["First line of reasoning\n\nsecond paragraph"]);
  render(<ThinkBlock item={item} live={false} sessionRef="s1" turn={turn} />);
  const summary = screen.getByText(/Thought/);
  // collapsed: preview rides the summary
  expect(summary.textContent).toContain("·");
  expect(summary.textContent).toContain("First line of reasoning");
  fireEvent.click(summary);
  // open: short label only - the long nowrap preview was what squeezed the
  // body into a right-side column (spec evidence-thinkblock-columns.png)
  expect(summary.textContent).not.toContain("First line of reasoning");
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx vitest run src/panes/session/transcript/messages/ThinkBlock.test.tsx`
Expected: FAIL — the summary keeps the preview when open.

- [ ] **Step 3: Gate the preview on the closed state**

In `ThinkBlock.tsx`'s settled branch, after `const open = isDisclosureOpen(disclosureKey, false);`, change the summary content:

```tsx
// The preview rides the COLLAPSED line only. Open, the summary reverts to
// the short label: the ~120-char nowrap preview claimed most of the flex
// row (details[open] is display:flex so the label sits beside the body,
// golden-reference geometry) and squeezed the body into a right column.
<summary
  className={CLASS.summary}
  onClick={(e) => {
    e.preventDefault();
    toggleDisclosure(disclosureKey, false);
  }}
>
  {open ? thoughtLabel(durationMs, "") : thoughtLabel(durationMs, preview)}
</summary>
```

- [ ] **Step 4: Run the suite**

Run: `npx vitest run src/panes/session/transcript/messages/ThinkBlock.test.tsx src/panes/session/transcript/messages/reasoningFormat.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/panes/session/transcript/messages/ThinkBlock.tsx src/panes/session/transcript/messages/ThinkBlock.test.tsx
git commit -m "transcript: think-block preview only when collapsed (task 6)"
```

---

### Task 7: Settled tool calls compose to one line

**Files:**
- Modify: `src/panes/session/transcript/ToolRow.tsx:79-152` and its header comment (lines 6-25)
- Modify: `src/panes/session/transcript/toolcallitem.module.css:48-76`
- Test: `src/panes/session/transcript/toolRowGrammar.test.tsx`

**Interfaces:**
- Consumes: nothing new (`ToolRow` already receives `expanded`).
- Produces: settled/collapsed rows with both purpose and summary carry `data-oneline="true"` and render `purpose — summary · duration` on one line with ellipsis; expanded rows keep the stacked purpose. Full purpose text remains on `title`.

- [ ] **Step 1: Write the failing tests**

In `toolRowGrammar.test.tsx`, add:

```tsx
test("collapsed row with purpose and summary composes one line", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      purpose="Running the foo tests"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
    />,
  );
  const row = screen.getByTestId("tool-row");
  expect(row.dataset.oneline).toBe("true");
  expect(row.textContent).toBe("Running the foo tests — npm test -- src/foo");
});

test("expanded row keeps the stacked grammar", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      purpose="Running the foo tests"
      failed={false}
      expandable
      expanded
      onToggle={() => {}}
    />,
  );
  expect(screen.getByTestId("tool-row").dataset.oneline).toBeUndefined();
});

test("purpose-less rows are unaffected", () => {
  render(<ToolRow summary="npm test" failed={false} expandable expanded={false} onToggle={() => {}} />);
  expect(screen.getByTestId("tool-row").dataset.oneline).toBeUndefined();
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npx vitest run src/panes/session/transcript/toolRowGrammar.test.tsx`
Expected: FAIL — no `data-oneline`, no em-dash composition.

- [ ] **Step 3: Implement the one-line composition**

In `ToolRow.tsx`, add to the component:

```tsx
const statedPurpose = statedPurposeOf({ description: purpose });
const hasPurpose = statedPurpose !== undefined;
const hasSummary = summary.trim() !== "";
// Tiered density: a COLLAPSED row with both parts composes them onto one
// line (purpose — summary), the summary demoted behind an em-dash. Two
// default lines per settled call was the transcript's biggest vertical
// cost (spec §Tier 2). Expanded rows keep the stacked grammar: the
// purpose then heads the body it revealed.
const oneLine = hasPurpose && hasSummary && !expanded;
```

then in the content, mark the row and insert the separator:

```tsx
{hasPurpose && (
  <span className={CLASS.purpose} data-testid="tool-row-purpose" title={oneLine ? statedPurpose : undefined}>
    {statedPurpose}
  </span>
)}
{oneLine && (
  <span className={CLASS.separator} aria-hidden="true">
    {" — "}
  </span>
)}
{hasSummary && (
  <span
    className={hasPurpose ? `${CLASS.summary} ${CLASS.demoted}` : CLASS.summary}
    data-testid="tool-row-summary"
    title={oneLine ? summary : undefined}
  >
    {summary}
    {duration && (
      <span className={CLASS.duration} data-testid="tool-row-duration">
        {" · "}
        {duration}
      </span>
    )}
  </span>
)}
```

and add `data-oneline={oneLine ? "true" : undefined}` to both the plain `div.row` and the `summary.row` return paths, plus `separator: requireClass(styles.separator, "toolcallitem.module.css", "separator")` to the CLASS map. Update the header comment's grammar block:

```
// THE ROW GRAMMAR, one line, in document order:
//
//     [chevron?] [✗ failure glyph?] [purpose] verb target [· meta] [affordances]
//
//   - a COLLAPSED row with both a purpose and a summary composes them onto
//     ONE line: purpose — summary · duration, the summary demoted, both
//     ellipsis-clamped (full text on title). An expanded row stacks the
//     purpose above its revealed body instead.
//   - the chevron LEADS ... (keep the rest of the existing comment)
```

In `toolcallitem.module.css`, replace the `.purpose`/`.summary` flex-basis comments with the one-line rules (keep the existing two classes for the expanded/stacked case, add):

```css
/* Collapsed one-line composition (tiered density): both spans stop
 * growing, clamp to one line, and ellipsis - the em-dash separator
 * between them owns the demotion boundary. The stacked two-line shape
 * survives only on expanded rows. */
.row[data-oneline="true"] {
  flex-wrap: nowrap;
}

.row[data-oneline="true"] .purpose,
.row[data-oneline="true"] .summary {
  flex: 0 1 auto;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.separator {
  flex: none;
  font-family: var(--font-sans);
  font-size: var(--font-size-ui);
  color: var(--ink-low);
}
```

- [ ] **Step 4: Run the tool-row suites and overflow guard**

Run: `npx vitest run src/panes/session/transcript/toolRowGrammar.test.tsx src/panes/session/transcript/ToolCallItem.test.tsx`
Expected: PASS (update any grammar assertions that expected two-line composition).

Run: `npm run overflowguard`
Expected: PASS (the nowrap+ellipsis change is exactly the kind this guard audits).

- [ ] **Step 5: Commit**

```bash
git add src/panes/session/transcript/ToolRow.tsx src/panes/session/transcript/toolcallitem.module.css src/panes/session/transcript/toolRowGrammar.test.tsx src/panes/session/transcript/ToolCallItem.test.tsx
git commit -m "transcript: settled tool calls compose to one line (task 7)"
```

---

### Task 8: NotificationCard defaults to a one-line row

**Files:**
- Modify: `src/panes/session/transcript/messages/NotificationCard.tsx:112-154`
- Modify: `src/panes/session/transcript/messages/notificationcard.module.css`
- Test: `src/panes/session/transcript/messages/NotificationCard.test.tsx`

**Interfaces:**
- Consumes: nothing new. Local `useState` disclosure, same precedent as `ToolCallCluster.tsx:35` (a derived presentation; no durable disclosure state).
- Produces: collapsed by default = single row (tone chip for warning/error + title + secondary); expanding reveals the Card with metadata, excerpt/message, concerns, and the raw disclosure. `data-testid="notification-card"` stays on the clickable row; `data-testid="notification-card-root"` exists only when open.

- [ ] **Step 1: Write the failing tests**

In `NotificationCard.test.tsx`, add:

```tsx
test("collapses to a single row by default; card chrome appears on expand", () => {
  render(<NotificationCard notification={makeNotification({ tone: "neutral", title: "explorer finished" })} />);
  // collapsed: the row is there, the card body is not
  const row = screen.getByTestId("notification-card");
  expect(row.textContent).toContain("explorer finished");
  expect(screen.queryByTestId("notification-card-root")).toBeNull();
  // expand
  fireEvent.click(row);
  expect(screen.getByTestId("notification-card-root")).not.toBeNull();
  expect(screen.getByTestId("notification-raw-disclosure")).not.toBeNull();
  // collapse again
  fireEvent.click(row);
  expect(screen.queryByTestId("notification-card-root")).toBeNull();
});

test("warning tone chip is visible even when collapsed", () => {
  render(<NotificationCard notification={makeNotification({ tone: "warning", title: "watcher reported" })} />);
  expect(screen.getByTestId("notification-card").textContent).toContain("warning");
});
```

(Use the test file's own notification factory name if it differs.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `npx vitest run src/panes/session/transcript/messages/NotificationCard.test.tsx`
Expected: FAIL — the card body renders unconditionally today.

- [ ] **Step 3: Restructure the card**

In `NotificationCard.tsx`, replace the `NotificationCard` component:

```tsx
export function NotificationCard({
  notification,
  sessionRef,
}: {
  notification: ParsedNotification;
  sessionRef?: string;
}) {
  // Tiered density: routine completions are one quiet line. The Card chrome
  // renders only when expanded; local state suffices (ToolCallCluster
  // precedent - a derived presentation, no durable disclosure state).
  const [open, setOpen] = useState(false);
  const chip = toneChip(notification.tone);
  const transcriptRef = isValidTranscriptRef(notification.transcriptRef) ? notification.transcriptRef : undefined;
  return (
    <details className={CLASS.disclosure} open={open}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; controlled for the same single-source-of-truth reason as ToolRow */}
      <summary
        className={CLASS.head}
        data-testid="notification-card"
        data-tone={notification.tone}
        aria-expanded={open}
        onClick={(e) => {
          e.preventDefault();
          setOpen((current) => !current);
        }}
      >
        {chip && <Chip tone={chip.chipTone}>{chip.label}</Chip>}
        <span className={CLASS.title}>{notification.title}</span>
        {notification.secondary && <span className={CLASS.secondary}>{notification.secondary}</span>}
        {transcriptRef && (
          <span className={CLASS.action}>
            <OpenTranscriptButton transcriptRef={transcriptRef} parentRef={sessionRef} label="Open subagent" />
          </span>
        )}
      </summary>
      {open && (
        <Card>
          <div className={CLASS.root} data-testid="notification-card-root">
            <NotificationMetadata notification={notification} />
            {notification.message ? (
              <div className={CLASS.excerpt} data-testid="notification-field-excerpt">
                <Markdown source={notification.message.slice(0, MESSAGE_MAX)} />
              </div>
            ) : (
              <Excerpt text={notification.excerpt} />
            )}
            {notification.concerns.length > 0 && (
              <div className={CLASS.concerns}>Concerns: {notification.concerns.join("; ")}</div>
            )}
            <details className={CLASS.raw} data-testid="notification-raw-disclosure">
              <summary className={CLASS.summary}>Raw notification</summary>
              <pre className={CLASS.rawBody} data-testid="notification-raw">
                {notification.rawText}
              </pre>
            </details>
          </div>
        </Card>
      )}
    </details>
  );
}
```

Add `useState` to the React import and `disclosure: requireClass(styles.disclosure, "notificationcard.module.css", "disclosure")` to the CLASS map. In `notificationcard.module.css`, add:

```css
/* Tier 3 collapsed treatment: a plain text row, no card chrome. The Card
 * only mounts when the disclosure opens (see the component). */
.disclosure {
  padding: var(--space-1) 0;
}

.head {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  cursor: pointer;
  list-style: none;
  font-family: var(--font-sans);
  font-size: var(--font-size-ui);
  color: var(--ink-mid);
}

.head::-webkit-details-marker {
  display: none;
}
```

(Keep the existing `.root`/`.title`/etc. rules; `.head` previously lived inside the Card, so check its old rule still makes sense at row scope — adjust padding to zero if the old rule assumes card padding.)

- [ ] **Step 4: Run the notification + steering suites**

`SteeringItem.tsx:151` renders these cards — its suite must pass.

Run: `npx vitest run src/panes/session/transcript/messages/NotificationCard.test.tsx src/panes/session/transcript/messages/SteeringItem.test.tsx`
Expected: PASS (update assertions that expected the card body by default — clicking the row first is the new path).

- [ ] **Step 5: Commit**

```bash
git add src/panes/session/transcript/messages/NotificationCard.tsx src/panes/session/transcript/messages/notificationcard.module.css src/panes/session/transcript/messages/NotificationCard.test.tsx src/panes/session/transcript/messages/SteeringItem.test.tsx
git commit -m "transcript: notifications collapse to one-line rows (task 8)"
```

---

### Task 9: Round timings lose the bordered box

**Files:**
- Modify: `src/panes/session/transcript/messages/systemnoticeitem.module.css:89-118`
- Test: `src/panes/session/transcript/messages/roundTimingsChrome.contract.test.ts` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: `.timings` no longer draws a border/radius; `.timingsBody` no longer draws a top rule. Class names are unchanged (no `requireClass` churn, no `SystemNoticeItem.tsx` change).

- [ ] **Step 1: Write the failing contract test**

Create `src/panes/session/transcript/messages/roundTimingsChrome.contract.test.ts`:

```ts
// Contract test for the tiered-density spec (ratification item 5): a
// once-per-round notice belongs to topic 07's quiet one-liner rule, not
// the hairline-bordered scaffold box the system prompt uses.
import { readFileSync } from "node:fs";

const css = readFileSync(new URL("./systemnoticeitem.module.css", import.meta.url), "utf8");

// Extract each named rule block and inspect its declarations.
function ruleBlock(name: string): string {
  const start = css.indexOf(`.${name} {`);
  if (start === -1) throw new Error(`.${name} rule not found`);
  const end = css.indexOf("}", start);
  return css.slice(start, end);
}

test("round timings render without box chrome", () => {
  expect(ruleBlock("timings")).not.toContain("border");
  expect(ruleBlock("timingsBody")).not.toContain("border-top");
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx vitest run src/panes/session/transcript/messages/roundTimingsChrome.contract.test.ts`
Expected: FAIL — `.timings` still declares a border.

- [ ] **Step 3: Unbox the timings rules**

In `systemnoticeitem.module.css`, replace the `.timings` and `.timingsBody` blocks:

```css
/* Round-timings disclosure (SystemNoticeItem.tsx's RoundTimingsLine): a
 * quiet one-liner like every other per-round notice (topic 07) - the
 * bordered scaffold box is reserved for the system prompt / compaction
 * summary, which fire a handful of times per session, not once per round. */
.timings {
  margin: var(--space-1) 0;
}

.timingsSummary {
  cursor: pointer;
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  color: var(--ink-low);
}

.timingsBody {
  margin-top: var(--space-1);
  padding-left: var(--space-3);
}

.timingsPhase {
  padding: var(--space-1) 0;
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  color: var(--ink-low);
}
```

(The expanded phase list indents under the summary instead of hanging off a rule — same inset idiom as `.groupBody` without its border.)

- [ ] **Step 4: Run the suites**

Run: `npx vitest run src/panes/session/transcript/messages/roundTimingsChrome.contract.test.ts src/panes/session/transcript/messages/SystemNoticeItem.test.tsx src/panes/session/transcript/messages/roundTimingsView.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/panes/session/transcript/messages/systemnoticeitem.module.css src/panes/session/transcript/messages/roundTimingsChrome.contract.test.ts
git commit -m "transcript: round timings drop the bordered box (task 9)"
```

---

### Task 10: TurnFailureEndCap compacts to one head row

**Files:**
- Modify: `src/panes/session/transcript/TurnFailureEndCap.tsx:105-120`
- Modify: `src/panes/session/transcript/turnfailure.module.css` (whole file)
- Test: `src/panes/session/transcript/TurnFailureEndCap.test.tsx`

**Interfaces:**
- Consumes: nothing new (retry path `threadsStore.send` and `classifyTurnError` unchanged).
- Produces: single head row = danger Chip + message + inline "What can I do?" disclosure + inline Retry. `.actions` class removed; `.hintDisclosure`/`.hintSummary` added. `data-testid="turn-failure"` unchanged.

- [ ] **Step 1: Write the failing tests**

In `TurnFailureEndCap.test.tsx`, add (adapt to the file's error/turn factories):

```tsx
test("hint sits behind a disclosure; retry is inline in the head row", () => {
  render(<TurnFailureEndCap error={providerError} turn={failedTurn} sessionRef="s1" />);
  const cap = screen.getByTestId("turn-failure");
  const head = cap.firstElementChild as HTMLElement;
  // retry button is in the head row, not a block of its own
  expect(head.contains(screen.getByRole("button", { name: /retry/i }))).toBe(true);
  // the boilerplate hint is not visible until its disclosure opens
  const hintSummary = screen.getByText("What can I do?");
  expect(hintSummary.closest("details")!.open).toBe(false);
  expect(cap.querySelector("[class*=hint]")!.closest("details")).not.toBeNull();
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx vitest run src/panes/session/transcript/TurnFailureEndCap.test.tsx`
Expected: FAIL — hint renders unconditionally, retry sits in a separate actions block.

- [ ] **Step 3: Compact the cap**

In `TurnFailureEndCap.tsx`, replace the CLASS map (drop `actions`, add `hintDisclosure`, `hintSummary`) and the return:

```tsx
return (
  <div className={CLASS.cap} data-testid="turn-failure" data-turn-error="true">
    <div className={CLASS.head}>
      <Chip tone="danger">{info.badge}</Chip>
      <span className={CLASS.message}>{info.message}</span>
      {info.hint && (
        <details className={CLASS.hintDisclosure}>
          <summary className={CLASS.hintSummary}>What can I do?</summary>
          <div className={CLASS.hint}>{info.hint}</div>
        </details>
      )}
      {canRetry && (
        <Button variant="primary" size="sm" onClick={() => void retry()}>
          {info.recoveryLabel}
        </Button>
      )}
    </div>
  </div>
);
```

Rewrite `turnfailure.module.css`:

```css
/* One head row carries the whole cap (tiered-density spec, 2026-07-27):
 * the failure FACT (chip + message + retry) inline, the advice behind a
 * disclosure. Danger stays confined to the Chip widget - this stylesheet
 * may not reference --danger (token-contract.test.ts). */
.cap {
  padding: var(--space-1) 0;
}

.head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--space-2);
}

.message {
  font-family: var(--font-mono);
  font-size: var(--font-size-caption);
  color: var(--ink-hi);
  overflow-wrap: anywhere;
}

.hintDisclosure {
  display: inline;
}

.hintSummary {
  display: inline;
  cursor: pointer;
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  color: var(--ink-low);
}

.hint {
  flex-basis: 100%;
  margin-top: var(--space-1);
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  color: var(--ink-mid);
}
```

(The nested `<details>` inside the flex row drops its body to a new flex line via `flex-basis: 100%` on `.hint` — check the computed layout in the visual pass; if the inline details misbehaves in Firefox, switch `.hintDisclosure` to `display: contents` with the same `.hint` rule.)

- [ ] **Step 4: Run the suites and lint**

Run: `npx vitest run src/panes/session/transcript/TurnFailureEndCap.test.tsx src/panes/session/transcript/turnFailure.test.ts`
Expected: PASS.

Run: `npm run lint`
Expected: clean (new classes must satisfy the requireClass contract).

- [ ] **Step 5: Commit**

```bash
git add src/panes/session/transcript/TurnFailureEndCap.tsx src/panes/session/transcript/turnfailure.module.css src/panes/session/transcript/TurnFailureEndCap.test.tsx
git commit -m "transcript: compact turn-failure end cap (task 10)"
```

---

### Task 11: Full verification + visual pass

**Files:**
- Modify (documentation only): `docs/web-ui/decisions.md` — update topics 03/04/05/07 verdicts to point at this change.

**Interfaces:**
- Consumes: all previous tasks.

- [ ] **Step 1: Whole frontend suite**

Run: `npm test`
Expected: PASS, no snapshot churn outside the touched suites.

- [ ] **Step 2: Types, lint, guards**

Run: `npm run typecheck && npm run lint && npm run layoutguard && npm run overflowguard`
Expected: all clean.

- [ ] **Step 3: Build**

Run: `npm run build`
Expected: clean build into `dist/` (the `restore-dist-placeholder` plugin keeps `dist/PLACEHOLDER` byte-identical — confirm `git status` shows no PLACEHOLDER diff).

- [ ] **Step 4: Visual pass, both themes**

Serve the build (or `npm run dev` against a live hub) and screenshot, dark and light:
1. A dense live session — one-line tool rows, quiet thoughts, prose standing out.
2. An expanded think block — body spans the content column, no right-side squeeze.
3. A session with subagent completions — one-line notification rows.
4. A failed turn — compact end cap, hint behind "What can I do?".
5. An exchange boundary — "You" and "Agent · {model}" eyebrows.

Compare against the "before" set in `docs/web-ui/specs/assets/2026-07-27-transcript-tiered-density/`. Save the "after" set beside it as `after-*.png`.

- [ ] **Step 5: Update decisions.md**

In `docs/web-ui/decisions.md`, amend the verdict lines for topic 03 (geometry → stacked eyebrow), topic 04 (size rule → contrast rule; no-label rule → visible eyebrow), topic 05 (preview closed-only), and topic 07 (round timings join the quiet one-liner rule), each citing this spec (`docs/web-ui/specs/2026-07-27-transcript-tiered-density-design.md`) and its ratification list. Keep the measured rationale that still applies (contrast numbers, exchange-boundary measurement).

- [ ] **Step 6: Commit**

```bash
git add docs/web-ui/decisions.md docs/web-ui/specs/assets/2026-07-27-transcript-tiered-density/
git commit -m "transcript: tiered-density verification pass + decisions.md updates (task 11)"
```

---

## Self-review notes (author)

- **Spec coverage:** agent prose size (T1), user eyebrow (T2), agent eyebrow (T3-T5), think block (T6), one-line tool rows (T7), notifications (T8), round timings (T9), failure cap (T10), decisions-ratification record (T11 step 5). Measure/non-goals: untouched by construction. Every spec component-table row maps to a task.
- **Type consistency:** `exchangeOpenersFor` (T3) ↔ `TurnBlockProps.exchangeOpeners` (T4) ↔ Session/Transcript wiring (T5); `ItemRenderProps.opensExchange/agentLabel` (T4) ↔ AgentMessageItem (T5); `ignoringTurn` compares both new fields (T4).
- **Known soft spots for the implementer:** test-factory names in existing suites (`userItem`, `settledAgentItem`, `makeNotification`, `turnWith`) are placeholders for whatever each file already calls its helpers — use the file's own. The `.hintDisclosure` inline-details layout (T10) is the one rule that may need the documented fallback after the visual pass.
