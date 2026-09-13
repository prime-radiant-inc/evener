# Transcript Entity Links Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect `job_`/`dlg_`/`watch_` ids in transcript prose and structured id fields, link jobs and delegates to the entity beside, and show hover cards summarizing current client-side state.

**Architecture:** A pure identity module validates ids against the server's rules; a derived entity view indexes the session's retained activity tree, live delegate projections, and loaded `job_watch` payloads; a shared `EntityRef` renders the trigger plus the standard `OpenButton`; a prose enhancer walks sanitized Markdown text nodes. No backend, AppWire, cache, or refresh-owner changes.

**Tech Stack:** TypeScript, React 18, Zustand, Vitest + @testing-library/react, Biome. Frontend only: `cmd/evener-hub/frontend/`.

**Spec:** `docs/superpowers/specs/2026-09-13-transcript-entity-links-design.md`

## Global Constraints

- No Go, AppWire, or daemon changes.
- Resolution is current-session only. Any id not resolvable from the current session's loaded state stays plain text.
- Watch ids never link; hover card only.
- Never rewrite fenced/inline code, raw tool output, ANSI logs, JSON, or copy strings. Plain user-message text is the one exception.
- Reuse existing helpers; do not add a second validator, tree walker, row shape, formatter, floating-layer lifecycle, or request engine.
- Frontend tests are deterministic: no network, no provider credentials, no model behavior.
- One `OpenButton` per open action. The id trigger never navigates.
- Run `npx biome check --write` on touched files before each commit.

---

### Task 1: Resolve initial-discovery ownership

**Why first:** The spec leaves one open decision. A fresh session's activity
tree is only established when `ActivityPanelBody` mounts (opened sheet);
`ActivityPanel`'s background effect returns early while `!summary.established`
(`ActivityPanel.tsx:335`). Job ids therefore stay unresolved until the Activity
panel has been opened once. Settle this before building on it.

**Files:**
- Read: `src/panes/session/chrome/ActivityPanel.tsx`, `src/stores/activitySummary.ts`, `src/panes/session/chrome/SessionChrome.tsx`
- Modify (option 1): `src/panes/session/chrome/ActivityPanel.tsx`, new `src/panes/session/chrome/useActivityRefresh.ts`
- Record: `docs/superpowers/specs/2026-09-13-transcript-entity-links-design.md` (Ownership section)

**Interfaces:**
- Produces: a decision, and (option 1) `useActivityRefresh(ref: string, model: ThreadModel): void` owning the complete refresh effect.

- [ ] **Step 1: Confirm the current behavior with a test**

Add to `src/panes/session/chrome/ActivityPanel.test.tsx` a case that mounts
`ActivityPanel` with `hideTrigger refreshWhenHidden` and an unestablished
summary, and asserts no `evener/jobs/list` request is issued. Run it to confirm
the gap is real.

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/chrome/ActivityPanel.test.tsx`
Expected: the new test passes, proving no request fires.

- [ ] **Step 2: Decide**

Apply option 1 (preferred): extract the body's refresh effect into
`useActivityRefresh` (conditions `retainedNonReady`/`unprovenFreshness`, `force`
handling, the `hydrationGeneration` dependency, completion-independent deps),
use it from `ActivityPanelBody` and from a body-less owner mounted by the
entity view's surface, and skip a forced refresh while `summary.loading` so
co-mounted owners cannot queue duplicate forced follow-ups.

If the extraction proves too invasive, apply option 2: keep the current
behavior and record in the spec that job entities require a prior Activity-panel
opening; then narrow Task 8/9 acceptance to delegates and watches.

- [ ] **Step 3: Prove the decision**

For option 1, change the Step 1 test to assert exactly one `evener/jobs/list`
request fires with chrome mounted and the summary unestablished, and that a
second co-mounted owner adds no follow-up request.

Run: `npx vitest run src/panes/session/chrome/ActivityPanel.test.tsx`
Expected: PASS, one request.

- [ ] **Step 4: Update the spec's Ownership section** to state the resolved
  behavior (and delete the open-decision text if option 1 landed).

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/session/chrome/ docs/superpowers/specs/2026-09-13-transcript-entity-links-design.md
git commit -m "feat(activity): own initial discovery so entity links resolve on a fresh session"
```

---

### Task 2: Entity id detection

**Files:**
- Create: `src/protocol/entityIds.ts`
- Test: `src/protocol/entityIds.test.ts`

**Interfaces:**
- Produces:
  - `type EntityKind = "job" | "delegate" | "watch"`
  - `interface EntityIdMatch { kind: EntityKind; id: string; start: number; end: number }`
  - `findEntityIds(text: string): EntityIdMatch[]`
  - `entityKindOf(id: string): EntityKind | undefined`
  - `jobOwnerSessionId(jobId: string): string | undefined`

- [ ] **Step 1: Write the failing tests**

```ts
import { describe, expect, test } from "vitest";
import { entityKindOf, findEntityIds, jobOwnerSessionId } from "./entityIds";

// Real shapes from identifier/*.go; the owner payload is a valid UUIDv7.
const OWNER = "02wMz5TxvEMoJEDTDGOTil";
const JOB = `job_${OWNER}_000000000123`;
const DELEGATE = "dlg_02wMz5TxvEMoJEDTDGOTil"; // 4 + 22
const WATCH = "watch_02wMz5TxvEMoJEDTDGOTil"; // 6 + 22

test("detects each kind at its exact length", () => {
  const found = findEntityIds(`a ${JOB} b ${DELEGATE} c ${WATCH} d`);
  expect(found.map((m) => m.kind)).toEqual(["job", "delegate", "watch"]);
  expect(found.map((m) => m.id)).toEqual([JOB, DELEGATE, WATCH]);
  expect(found[0].start).toBe(2);
  expect(found[0].end).toBe(2 + JOB.length);
});

test("rejects wrong lengths, bad alphabet, and bad UUIDv7 payloads", () => {
  expect(findEntityIds(JOB.slice(0, -1))).toEqual([]);
  expect(findEntityIds(`job_${OWNER}_00000000012!`)).toEqual([]);
  expect(findEntityIds("dlg_0000000000000000000000")).toEqual([]); // decodes to non-v7
});

test("rejects a match embedded in a longer token", () => {
  expect(findEntityIds(`x${DELEGATE}`)).toEqual([]);
  expect(findEntityIds(`${DELEGATE}z`)).toEqual([]);
  expect(findEntityIds(`${DELEGATE}_more`)).toEqual([]);
});

test("entityKindOf and jobOwnerSessionId agree with detection", () => {
  expect(entityKindOf(DELEGATE)).toBe("delegate");
  expect(entityKindOf(JOB)).toBe("job");
  expect(entityKindOf("nope")).toBeUndefined();
  expect(jobOwnerSessionId(JOB)).toBe(OWNER);
  expect(jobOwnerSessionId(DELEGATE)).toBeUndefined();
});
```

- [ ] **Step 2: Run and confirm failure**

Run: `npx vitest run src/protocol/entityIds.test.ts`
Expected: FAIL, module not found.

- [ ] **Step 3: Implement**

```ts
// Mirrors identifier/job.go, identifier/uuid.go, identifier/domains.go. The
// server is authoritative; this module is pinned to it and its tests use the
// same golden ids.
export type EntityKind = "job" | "delegate" | "watch";

export interface EntityIdMatch {
  kind: EntityKind;
  id: string;
  start: number;
  end: number;
}

const BASE62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
const BASE62_SET = new Set(BASE62.split(""));
const UUID_WIDTH = 22;
const JOB_SUFFIX = 12;

function isBase62Run(value: string): boolean {
  for (const ch of value) if (!BASE62_SET.has(ch)) return false;
  return true;
}

// Decodes base62 to bytes and enforces the UUIDv7 version/variant bits the
// server checks (ValidateUUIDv7Payload). Anything that does not decode to
// <=128 bits, or is not version 7 / RFC4122 variant, is not an id.
function isUUIDv7Payload(payload: string): boolean {
  if (payload.length !== UUID_WIDTH || !isBase62Run(payload)) return false;
  let n = 0n;
  for (const ch of payload) n = n * 62n + BigInt(BASE62.indexOf(ch));
  if (n >> 128n !== 0n) return false;
  const bytes = new Uint8Array(16);
  for (let i = 15; i >= 0; i--) {
    bytes[i] = Number(n & 0xffn);
    n >>= 8n;
  }
  const version = bytes[6] >> 4;
  const variant = bytes[8] >> 6;
  return version === 7 && variant === 2;
}

function matchAt(text: string, start: number, kind: EntityKind, length: number): EntityIdMatch | undefined {
  const end = start + length;
  if (end > text.length) return undefined;
  const before = start === 0 ? "" : text[start - 1];
  const after = end === text.length ? "" : text[end];
  if (before !== "" && /[0-9A-Za-z_]/.test(before)) return undefined;
  if (after !== "" && /[0-9A-Za-z_]/.test(after)) return undefined;
  const id = text.slice(start, end);
  if (!isValidId(kind, id)) return undefined;
  return { kind, id, start, end };
}

function isValidId(kind: EntityKind, id: string): boolean {
  if (kind === "delegate") return id.startsWith("dlg_") && isUUIDv7Payload(id.slice(4));
  if (kind === "watch") return id.startsWith("watch_") && isUUIDv7Payload(id.slice(6));
  if (!id.startsWith("job_")) return false;
  const owner = id.slice(4, 4 + UUID_WIDTH);
  const suffix = id.slice(5 + UUID_WIDTH);
  return id.length === 5 + UUID_WIDTH + JOB_SUFFIX && id[4 + UUID_WIDTH] === "_" && isUUIDv7Payload(owner) && isBase62Run(suffix);
}

export function findEntityIds(text: string): EntityIdMatch[] {
  const out: EntityIdMatch[] = [];
  // Cheap prefix prefilter: no decode unless the prefix is present.
  const patterns: Array<[EntityKind, string, number]> = [
    ["job", "job_", 5 + UUID_WIDTH + JOB_SUFFIX],
    ["delegate", "dlg_", 4 + UUID_WIDTH],
    ["watch", "watch_", 6 + UUID_WIDTH],
  ];
  for (let i = 0; i < text.length; i++) {
    for (const [kind, prefix, length] of patterns) {
      if (!text.startsWith(prefix, i)) continue;
      const match = matchAt(text, i, kind, length);
      if (match) {
        out.push(match);
        i = match.end - 1;
      }
      break;
    }
  }
  return out;
}

export function entityKindOf(id: string): EntityKind | undefined {
  if (isValidId("delegate", id)) return "delegate";
  if (isValidId("watch", id)) return "watch";
  if (isValidId("job", id)) return "job";
  return undefined;
}

export function jobOwnerSessionId(jobId: string): string | undefined {
  return isValidId("job", jobId) ? jobId.slice(4, 4 + UUID_WIDTH) : undefined;
}
```

- [ ] **Step 4: Run and confirm pass**

Run: `npx vitest run src/protocol/entityIds.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/protocol/entityIds.ts src/protocol/entityIds.test.ts
git commit -m "feat(transcript): entity id detection mirroring identifier rules"
```

---

### Task 3: Extract watch row normalization

**Files:**
- Create: `src/protocol/watchRows.ts`
- Modify: `src/panes/session/transcript/tools/jobWatch.tsx` (import, delete the moved helpers)
- Test: `src/protocol/watchRows.test.ts`

**Interfaces:**
- Consumes: `ItemModel` from `src/protocol/model.ts`.
- Produces:
  - `type WatchDisplayState = "watching" | "pending" | "missing" | "ended" | "cleared" | "terminal-catch-up"`
  - `interface WatchSummary { id: string; state: WatchDisplayState; source?: string; condition?: string; deliveries?: number; note?: string; endReason?: string }`
  - `foldWatchSummaries(items: ItemModel[]): Map<string, WatchSummary>`
  - re-exports of `normalizeRow`, `WatchRow`, `sourceLabel`, `humanizeSeconds`, `humanizeInterval`, `parseConditionText`, `conditionSpec` moved verbatim from `jobWatch.tsx`

- [ ] **Step 1: Write the failing tests**

```ts
import { expect, test } from "vitest";
import type { ItemModel } from "./model";
import { foldWatchSummaries } from "./watchRows";

function watchItem(id: string, raw: unknown, turnId = "t1"): ItemModel {
  return { id, turnId, type: "commandExecution", toolName: "job_watch", raw } as unknown as ItemModel;
}

test("latest snapshot wins regardless of load order", () => {
  const watching = watchItem("a", { watch_id: "watch_x", watching: true, source: "job_y" }, "t2");
  const ended = watchItem("b", { watch_id: "watch_x", watching: false, end_reason: "job_completed" }, "t1");
  const forward = foldWatchSummaries([ended, watching]); // loaded newest-first
  const backward = foldWatchSummaries([watching, ended]);
  expect(forward.get("watch_x")?.state).toBe(backward.get("watch_x")?.state);
  expect(forward.get("watch_x")?.state).toBe("ended"); // t2 watching is later than t1 ended
});

test("absent watching is not coerced to false", () => {
  const view = foldWatchSummaries([watchItem("a", { watch_id: "watch_x", deliveries: 3 })]);
  expect(view.get("watch_x")?.state).toBe("pending");
});
```

- [ ] **Step 2: Run and confirm failure**

Run: `npx vitest run src/protocol/watchRows.test.ts`
Expected: FAIL, module not found.

- [ ] **Step 3: Move the helpers**

Move `WatchRow`, `normalizeRow`, `sourceLabel`, `humanizeSeconds`,
`humanizeInterval`, `conditionSpec`, `parseConditionText`, and their private
dependencies from `jobWatch.tsx` into `src/protocol/watchRows.ts` unchanged, and
re-import them in `jobWatch.tsx`. Do not change their bodies; the existing
`jobWatch.test.tsx` is the proof this is a pure move.

- [ ] **Step 4: Add the fold and summary**

```ts
export function foldWatchSummaries(items: ItemModel[]): Map<string, WatchSummary> {
  const byId = new Map<string, { position: number; summary: WatchSummary }>();
  items.forEach((item, position) => {
    const raw = item.raw as Record<string, unknown> | undefined;
    if (!raw || item.toolName !== "job_watch" || typeof raw.watch_id !== "string") return;
    const ids = Array.isArray(raw.watches) ? (raw.watches as unknown[]) : Array.isArray(raw.recent_watches) ? (raw.recent_watches as unknown[]) : [raw];
    for (const entry of ids) {
      const row = normalizeRow(entry);
      if (!row) continue;
      const prior = byId.get(row.id);
      const summary: WatchSummary = prior ? { ...prior.summary, ...mergePresent(prior.summary, row) } : summaryOf(row);
      byId.set(row.id, { position, summary });
    }
  });
  return new Map([...byId].map(([id, entry]) => [id, entry.summary]));
}
```

`summaryOf(row)` maps the existing `WatchRow` fields to `WatchSummary`,
choosing state by the operation markers already used in `jobWatch.tsx`
(`replaced_existing`/`fired` → cleared; `terminal_catchup` → terminal-catch-up;
`end_reason` → ended; `watching === true` → watching; absent → pending;
inspect miss → missing). `mergePresent` copies only present fields.

- [ ] **Step 5: Run both suites**

Run: `npx vitest run src/protocol/watchRows.test.ts src/panes/session/transcript/tools/jobWatch.test.tsx`
Expected: PASS, including the unchanged `jobWatch` tests.

- [ ] **Step 6: Commit**

```bash
git add src/protocol/watchRows.ts src/protocol/watchRows.test.ts src/panes/session/transcript/tools/jobWatch.tsx
git commit -m "refactor(transcript): extract watch row normalization and fold"
```

---

### Task 4: Disclosure-independent entity index

**Files:**
- Modify: `src/protocol/activityRows.ts`
- Test: `src/protocol/activityRows.test.ts`

**Interfaces:**
- Produces: `indexActivityEntities(tree: ActivityTree): Map<string, ActivityJobRow | ActivityDelegateRow>` keyed by `job.jobId` / `delegate.delegateId`. Also export `jobRowFields(job: ActivityJob, parentRef: string)` and `delegateRowFields(delegate: ActivityDelegate, parentRef: string)` factored out of `buildActivityRows` so both use one derivation.

- [ ] **Step 1: Write the failing test**

```ts
import { expect, test } from "vitest";
import { indexActivityEntities } from "./activityRows";

test("indexes completed entries hidden behind a collapsed fold", () => {
  const tree = treeWithTerminalJob(); // a root session with one inactive (terminal) job
  expect(indexActivityEntities(tree).has(TERMINAL_JOB_ID)).toBe(true);
});
```

- [ ] **Step 2: Run and confirm failure**

Run: `npx vitest run src/protocol/activityRows.test.ts -t "collapsed fold"`
Expected: FAIL, `indexActivityEntities` not exported.

- [ ] **Step 3: Implement**

Factor the `transcriptRef`/`parentRef` derivation out of `buildActivityRows`
into the two exported helpers, call them from `buildActivityRows`, and add:

```ts
// Walks every loaded entry, ignoring fold/disclosure state, so an entity
// resolves identically whether or not its fold is expanded.
export function indexActivityEntities(tree: ActivityTree): Map<string, ActivityJobRow | ActivityDelegateRow> {
  const index = new Map<string, ActivityJobRow | ActivityDelegateRow>();
  const visit = (session: ActivitySessionNode, parentRef: string) => {
    for (const entry of session.entries) {
      if (entry.kind === "shell") index.set(entry.job.jobId, jobRowFields(entry.job, parentRef));
      else {
        const row = delegateRowFields(entry.delegate, parentRef);
        index.set(entry.delegate.delegateId, row);
        if (entry.delegate.child) visit(entry.delegate.child, row.transcriptRef);
      }
    }
  };
  visit(tree.root, tree.root.ref);
  return index;
}
```

Use the same `level`/`id`/`defaultDetailOpen` values `buildActivityRows` uses.

- [ ] **Step 4: Run and confirm pass**

Run: `npx vitest run src/protocol/activityRows.test.ts`
Expected: PASS, existing tests still pass.

- [ ] **Step 5: Commit**

```bash
git add src/protocol/activityRows.ts src/protocol/activityRows.test.ts
git commit -m "feat(activity): disclosure-independent entity index"
```

---

### Task 5: Entity view

**Files:**
- Create: `src/protocol/entityView.ts`
- Test: `src/protocol/entityView.test.ts`

**Interfaces:**
- Consumes: `indexActivityEntities`, `foldWatchSummaries`, `entityIds`.
- Produces:
  - `interface OpenTarget { ref: string; parentRef: string }`
  - `type EntityView = JobEntityView | DelegateEntityView | WatchEntityView`
  - `buildEntityView(sources: { sessionRef: string; tree?: ActivityTree; delegates?: EvenerDelegateInfo[]; turns: TurnModel[]; stale: boolean; ended: boolean }): Map<string, EntityView>`
  - `entityOpenTarget(view: EntityView): OpenTarget | undefined`
  - `watchItems(turns: TurnModel[]): ItemModel[]` (flattens to `job_watch` items)

- [ ] **Step 1: Write the failing tests**

```ts
test("job uses the row transcriptRef and parentRef", () => {
  const view = buildEntityView({ sessionRef: "local:s", tree: treeWithJob("job_x"), items: [], stale: false, ended: false });
  expect(entityOpenTarget(view.get("job_x")!)).toEqual({ ref: "job:job_x", parentRef: "local:s" });
});

test("live-only delegate cards and navigates from delegates[]", () => {
  const stable = { delegateId: "dlg_x", transcriptRef: "local:child", projectionRevision: 3 } as EvenerDelegateInfo;
  const view = buildEntityView({ sessionRef: "local:s", delegates: [stable], items: [], stale: false, ended: false });
  const entity = view.get("dlg_x")!;
  expect(entity.kind).toBe("delegate");
  expect(entityOpenTarget(entity)).toEqual({ ref: "local:child", parentRef: "local:s" });
});

test("higher projectionRevision wins; live wins ties", () => {
  // tree delegate revision 5, live revision 7 -> live; tree 9, live 7 -> tree; tree absent -> live
});

test("stale and ended propagate to every entity", () => {
  const view = buildEntityView({ sessionRef: "local:s", tree: treeWithJob("job_x"), items: [], stale: true, ended: false });
  expect(view.get("job_x")!.stale).toBe(true);
});
```

- [ ] **Step 2: Run and confirm failure**

Run: `npx vitest run src/protocol/entityView.test.ts`
Expected: FAIL, module not found.

- [ ] **Step 3: Implement**

```ts
export function buildEntityView(sources: EntityViewSources): Map<string, EntityView> {
  const out = new Map<string, EntityView>();
  const index = sources.tree ? indexActivityEntities(sources.tree) : new Map();
  for (const [id, row] of index) {
    out.set(id, row.kind === "job"
      ? { kind: "job", id, row, open: { ref: row.transcriptRef ?? `job:${id}`, parentRef: row.parentRef }, stale: sources.stale, ended: sources.ended }
      : { kind: "delegate", id, row, open: { ref: row.transcriptRef, parentRef: row.parentRef }, stale: sources.stale, ended: sources.ended });
  }
  for (const stable of sources.delegates ?? []) {
    const existing = out.get(stable.delegateId);
    if (existing?.kind === "delegate" && existing.row) {
      const treeRevision = existing.row.delegate.projectionRevision;
      if (treeRevision !== undefined && treeRevision > stable.projectionRevision) continue;
    }
    out.set(stable.delegateId, { kind: "delegate", id: stable.delegateId, stable, open: { ref: stable.transcriptRef, parentRef: sources.sessionRef }, stale: sources.stale, ended: sources.ended });
  }
  for (const [id, watch] of foldWatchSummaries(watchItems(sources.turns))) {
    out.set(id, { kind: "watch", id, watch, lastKnown: true, stale: true, ended: false });
  }
  return out;
}

export function entityOpenTarget(view: EntityView): OpenTarget | undefined {
  return view.kind === "watch" ? undefined : view.open;
}
```

- [ ] **Step 4: Share one index per ref, keyed on real watch inputs**

```ts
// One derived view per ref. The watch-fold key must not be the turns array:
// prose deltas replace it. Key on the job_watch items' position and payload
// identity instead.
export function watchFoldKey(turns: TurnModel[]): string {
  const parts: string[] = [];
  for (const turn of turns) {
    for (const item of turn.items) {
      if (item.toolName !== "job_watch") continue;
      parts.push(`${item.id}:${item.status ?? ""}:${item.argumentsJSON?.length ?? 0}:${item.output?.length ?? 0}`);
    }
  }
  return parts.join("|");
}

export function useEntityView(sessionRef: string, model: ThreadModel): Map<string, EntityView> {
  const tree = useActivityPanelStore((s) => retainedActivityTree(s.entries.get(sessionRef)));
  const load = useActivityPanelStore((s) => s.entries.get(sessionRef)?.load);
  // Staleness is its own subscription: staleError can change while the tree is
  // byte-identical, so it cannot live in the index key.
  const stale = load?.kind === "ready" && load.staleError !== undefined;
  const ended = load?.kind === "ended";
  const key = watchFoldKey(model.turns);
  return useMemo(
    () => buildEntityView({ sessionRef, tree, delegates: model.delegates, turns: model.turns, stale, ended }),
    [sessionRef, tree, model.delegates, key, stale, ended],
  );
}
```

- [ ] **Step 5: Run and confirm pass**

Run: `npx vitest run src/protocol/entityView.test.ts`
Expected: PASS, including a case that rerendering with only agent prose changed does not rebuild the watch entries.

- [ ] **Step 6: Commit**

```bash
git add src/protocol/entityView.ts src/protocol/entityView.test.ts
git commit -m "feat(transcript): derived entity view with open targets"
```

---

### Task 6: Shared Tooltip lifecycle

**Files:**
- Create: `src/widgets/hovercard/useFloatingLabel.ts`
- Modify: `src/widgets/tooltip/index.tsx`
- Test: existing `src/widgets/tooltip/tooltip.test.tsx` (must stay green)

**Interfaces:**
- Produces: `useFloatingLabel(): { visible: boolean; wrapperRef; triggerProps }` covering the show delay, hide, scroll/resize dismiss, and `ResizeObserver` re-measure, parameterized by a `measure` callback.

- [ ] **Step 1: Extract with no behavior change**

Move the timer, visibility, scroll/resize dismiss, and `ResizeObserver`
re-measure from `Tooltip` into `useFloatingLabel`, and have `Tooltip` consume
it. No markup or CSS change.

- [ ] **Step 2: Run the existing suite**

Run: `npx vitest run src/widgets/tooltip/tooltip.test.tsx`
Expected: PASS with the test file unchanged. Confirm `git diff` touches only
`tooltip/index.tsx` and the new hook.

- [ ] **Step 3: Commit**

```bash
git add src/widgets/hovercard/useFloatingLabel.ts src/widgets/tooltip/index.tsx
git commit -m "refactor(widgets): share tooltip floating lifecycle"
```

---

### Task 7: Hover card and EntityRef

**Files:**
- Create: `src/widgets/hovercard/index.tsx`, `src/widgets/hovercard/hovercard.module.css`
- Create: `src/panes/session/transcript/EntityRef.tsx`, `src/panes/session/transcript/entityref.module.css`
- Test: `src/panes/session/transcript/EntityRef.test.tsx`

**Interfaces:**
- Consumes: `EntityView`, `entityOpenTarget`, `useFloatingLabel`, `OpenButton`, `openTranscript`, existing formatters.
- Produces: `<EntityRef view={EntityView} display?: string />`.

- [ ] **Step 1: Write the failing tests**

```ts
test("unresolved renders plain text", () => {
  render(<EntityRef view={undefined} id="dlg_x" />);
  expect(screen.queryByRole("button")).toBeNull();
});

test("navigable renders exactly one OpenButton that opens the pane", () => {
  render(<EntityRef view={jobView} id="job_x" />);
  const buttons = screen.getAllByRole("button");
  expect(buttons).toHaveLength(1);
  fireEvent.click(buttons[0]);
  expect(workspaceStore.getState().panes).toMatchObject([{ type: "transcript", params: { ref: "job:job_x", parentRef: "local:s" } }]);
});

test("the id trigger does not navigate", () => {
  render(<EntityRef view={jobView} id="job_x" />);
  fireEvent.click(screen.getByTestId("entity-trigger"));
  expect(workspaceStore.getState().panes).toEqual([]);
});

test("watch renders a card trigger with no OpenButton", () => {
  render(<EntityRef view={watchView} id="watch_x" />);
  expect(screen.queryByRole("button")).toBeNull();
});

test("shows the card on focus", () => {
  render(<EntityRef view={jobView} id="job_x" />);
  fireEvent.focus(screen.getByTestId("entity-trigger"));
  expect(screen.getByRole("tooltip")).toBeTruthy();
});
```

- [ ] **Step 2: Run and confirm failure**

Run: `npx vitest run src/panes/session/transcript/EntityRef.test.tsx`
Expected: FAIL, module not found.

- [ ] **Step 3: Implement**

`HoverCard` renders a non-interactive `role="tooltip"` bubble with rich
children, positioned by `computeTooltipPosition`, using `useFloatingLabel`.
`EntityRef`:

```tsx
export function EntityRef({ view, id, display }: { view?: EntityView; id: string; display?: string }) {
  const text = display ?? id;
  if (!view) return <span>{text}</span>;
  const target = entityOpenTarget(view);
  const card = <HoverCard label={cardLabel(view)} />;
  return (
    <span className={CLASS.group}>
      <span data-testid="entity-trigger" tabIndex={0} className={CLASS.trigger} aria-describedby={card.describedBy}>
        {text}
      </span>
      {target && (
        <OpenButton
          label={view.kind === "job" ? "Open job log" : "Open delegate transcript"}
          onClick={() => openTranscript(target.ref, target.parentRef)}
        />
      )}
      {card.element}
    </span>
  );
}
```

`cardLabel(view)` renders the per-kind body from the reused row/stable/watch
fields and the existing formatters (`formatElapsed`, `formatClockTime`,
`formatQuietAge`, `formatUsagePair`, `activityDelegateState`,
`classifyJobStatus`, `stableDelegateDisplayStatus`). Mark stale cards with a
"stale" caption and ended trees as ended.

- [ ] **Step 4: Run and confirm pass**

Run: `npx vitest run src/panes/session/transcript/EntityRef.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/widgets/hovercard/ src/panes/session/transcript/EntityRef.tsx src/panes/session/transcript/entityref.module.css src/panes/session/transcript/EntityRef.test.tsx
git commit -m "feat(transcript): EntityRef with hover card and open affordance"
```

---

### Task 8: Prose enhancement

**Files:**
- Create: `src/panes/session/transcript/EntityText.tsx`
- Modify: `src/panes/session/transcript/messages/AgentMarkdown.tsx`, `src/panes/session/transcript/messages/UserMessageItem.tsx`
- Test: `src/panes/session/transcript/messages/agentEntityLinks.test.tsx`

**Interfaces:**
- Consumes: `findEntityIds`, `EntityView`, `EntityRef`.
- Produces: `useEntityTextEnhancement(rootRef, deps)` and `<EntityText text={…} />`.

- [ ] **Step 1: Write the failing tests**

```ts
test("an id in agent prose links and opens", () => { /* render AgentMessageItem with the id in a paragraph; click OpenButton; assert panes */ });
test("ids in inline and fenced code are untouched", () => {
  render(message("`dlg_x` and\n\n```\ndlg_x\n```"));
  expect(screen.queryByTestId("entity-trigger")).toBeNull();
});
test("an effect replay with unchanged source preserves visible text", () => {
  const { rerender } = render(message(text));
  const before = container.textContent;
  rerender(message(text));
  expect(container.textContent).toBe(before);
});
test("user message text links via the string form", () => { /* render UserMessageItem */ });
```

- [ ] **Step 2: Run and confirm failure**

Run: `npx vitest run src/panes/session/transcript/messages/agentEntityLinks.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement**

`useEntityTextEnhancement` core (the risky part):

```ts
export function useEntityTextEnhancement(rootRef: RefObject<HTMLElement>, deps: unknown[]) {
  const [portals, setPortals] = useState<ReactPortal[]>([]);
  useLayoutEffect(() => {
    const root = rootRef.current;
    if (!root) return;
    const mounts: ReactPortal[] = [];
    const cleanups: Array<() => void> = [];
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    const nodes: Text[] = [];
    while (walker.nextNode()) {
      const node = walker.currentNode as Text;
      const parent = node.parentElement;
      if (!parent || parent.closest("code, pre, script, style, a, [data-entity-host]")) continue;
      if (findEntityIds(node.data).length > 0) nodes.push(node);
    }
    for (const node of nodes) {
      const original = node.data;
      const matches = findEntityIds(original);
      const fragment = document.createDocumentFragment();
      let cursor = 0;
      for (const match of matches) {
        fragment.append(original.slice(cursor, match.start));
        const host = document.createElement("span");
        host.setAttribute("data-entity-host", "");
        fragment.append(host);
        mounts.push(createPortal(<EntityRef id={match.id} />, host, match.id));
        cursor = match.end;
      }
      fragment.append(original.slice(cursor));
      node.replaceWith(fragment);
      cleanups.push(() => { node.data = original; });
    }
    setPortals(mounts);
    return () => { for (const cleanup of cleanups) cleanup(); };
  }, deps);
  return portals;
}
```

`<EntityText text>` string form follows the `webTools.linkifyLine` `matchAll` +
cursor idiom:

```tsx
export function EntityText({ text }: { text: string }) {
  const matches = findEntityIds(text);
  const out: ReactNode[] = [];
  let cursor = 0;
  for (const match of matches) {
    out.push(text.slice(cursor, match.start));
    out.push(<EntityRef key={`${match.start}:${match.id}`} id={match.id} />);
    cursor = match.end;
  }
  out.push(text.slice(cursor));
  return <>{out}</>;
}
```

Wire `AgentMarkdown` to the hook and `UserMessageItem` to `<EntityText>`. Skipping
`[data-entity-host]` makes repeated passes idempotent; restoring `node.data`
keeps an unchanged-source replay byte-identical.

- [ ] **Step 4: Run and confirm pass**

Run: `npx vitest run src/panes/session/transcript/messages/agentEntityLinks.test.tsx src/panes/session/transcript/messages/agentFileLinks.test.tsx`
Expected: PASS, including the file-link suite.

- [ ] **Step 5: Commit**

```bash
git add src/panes/session/transcript/EntityText.tsx src/panes/session/transcript/messages/AgentMarkdown.tsx src/panes/session/transcript/messages/UserMessageItem.tsx src/panes/session/transcript/messages/agentEntityLinks.test.tsx
git commit -m "feat(transcript): inline entity links in message prose"
```

---

### Task 9: Structured id fields

**Files:**
- Modify: `src/panes/session/transcript/tools/jobTools.tsx`, `src/panes/session/transcript/tools/jobWatch.tsx`, `src/panes/session/transcript/tools/delegateStatus.tsx`
- Test: the matching test files.

**Interfaces:**
- Consumes: `EntityRef`, the entity view.

- [ ] **Step 1: Write the failing tests**

```ts
test("job_list rows render an entity ref per identity", () => { /* id present -> trigger; no second button in the row header */ });
test("jobWatch rows never link", () => { /* trigger present, no OpenButton */ });
test("delegateStatus keeps its single footer control", () => {
  render(<DelegateStatusBody … />);
  expect(screen.getAllByRole("button", { name: /Open/ })).toHaveLength(1);
});
```

- [ ] **Step 2: Run and confirm failure**

Run: `npx vitest run src/panes/session/transcript/tools/jobTools.test.tsx src/panes/session/transcript/tools/jobWatch.test.tsx`
Expected: FAIL for the new cases.

- [ ] **Step 3: Implement** each renderer to use `<EntityRef>` for the identity
  (job list), the watch id (full id as key, clipped as display), and the
  delegate status header (trigger only, footer control unchanged).

- [ ] **Step 4: Run and confirm pass**

Run: `npx vitest run src/panes/session/transcript/tools/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/panes/session/transcript/tools/
git commit -m "feat(transcript): entity refs in structured tool fields"
```

---

### Task 10: Gates and final verification

- [ ] **Step 1: Format touched files**

Run: `cd cmd/evener-hub/frontend && npx biome check --write src/`

- [ ] **Step 2: Frontend gate**

Run: `make test-web`
Expected: PASS (unit, typecheck, Biome).

- [ ] **Step 3: Browser gate (Chrome-capable host)**

Run: `make test-web-browser`
Expected: PASS. If no Chrome is available, record that this gate did not run
and why; do not claim it passed.

- [ ] **Step 4: Confirm no stray artifacts**

Run: `git status --short`
Expected: only intended files. Remove any scratch files.

- [ ] **Step 5: Commit any gate fixes**, then report the exact commands and
  results.

---

## Self-Review

**Spec coverage:** identity rules → Task 2; current-session sources and ownership → Tasks 1, 4, 5; open targets → Task 5; watch normalization/ordering → Task 3; hover card + lifecycle reuse → Tasks 6, 7; EntityRef branches and accessibility → Task 7; prose enhancement and preservation → Task 8; structured fields → Task 9; gates → Task 10. The spec's open initial-discovery decision is Task 1.

**Placeholder scan:** no "TBD"/"add error handling" left. Tasks 3, 7, 8, and 9
give the exact helper names and behavioral contracts, with the full bodies for
extractions constrained by "keep the existing tests green"; their steps name the
code to move or the props/fields to render rather than full listings.

**Type consistency:** `EntityIdMatch`, `EntityKind`, `OpenTarget`, `EntityView`,
`WatchSummary`, `WatchDisplayState`, `buildEntityView`, `entityOpenTarget`,
`indexActivityEntities`, `foldWatchSummaries`, `useFloatingLabel`, and
`useEntityTextEnhancement` are used with the same names and shapes across
tasks. `OpenTarget.parentRef` is required, matching every `openTranscript` call
site in Tasks 5 and 7.
