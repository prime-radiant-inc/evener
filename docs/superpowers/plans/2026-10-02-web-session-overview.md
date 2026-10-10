# Web session Overview Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Overview the web session inspection surface, add About using the existing details body, and retire Activity workspace panes without migration.

**Architecture:** Extend the shared activity sidebar and its per-session view store. About observes the sidebar's explicit public ref through `useThreadModel` and renders `DetailsPanelBody`; existing stores retain hydration, subscriptions, accounting, and recovery. Remove the Activity pane registration so existing unknown-pane restoration drops its placements while preserving other panes and independently saved Overview choices.

**Tech Stack:** TypeScript, React, Zustand, existing widgets and CSS modules, Vitest and Testing Library, Chrome/CDP browser guards, existing AppWire scripted transport fixtures.

**Spec:** `docs/superpowers/specs/2026-10-02-web-session-overview-design.md`

**Review record:** `docs/superpowers/specs/2026-10-02-web-session-overview-review.md`

**Execution:** Jesse chose inline execution, followed by simplify-code and PR shepherding. Use `superpowers:executing-plans` after Jesse approves this plan. This document grants no permission to start before that approval.

## Global Constraints

These requirements are copied from the approved spec. Every task includes them.

- “Use **Overview** for the combined surface and its menu action.”
- “Keep the order **Agents, Jobs, Watches, Tasks, About**.”
- “About shows only its label, with no count and no status-footer chip.”
- “Adapt the tab strip locally if needed; retain the existing sidebar width, design tokens, and touch-target floors.” The existing desktop width is 320px.
- “Do not rename the transcript's Activity detail level, domain APIs, storage keys, route identifiers, or every occurrence of the word Activity.”
- “About adds no lifecycle mutation, provider call, separate metadata cache, collection demand, or retry owner.”
- “Do not migrate its position, focus, or session intent into Overview and do not add a compatibility shim.” This applies to retired `sessionActivity` workspace placements.
- “Existing saved Details workspace panes keep their registration, title, body, and loading behavior.”
- “The recursive Activity Sheet and hidden activity-discovery owner are distinct from the retired workspace pane.” Preserve their contracts where still used.
- “Native mobile and TUI behavior, backend APIs, activity ownership, counts, transcript detail levels, session lifecycle, and generic retry behavior are out of scope.”
- “Use real components and stores. Script only external transport/model reads and clocks.”
- “Implementation runs inline, then simplify-code and PR shepherding follow as requested.”

Additional execution safeguards:

- Before plan approval, change only this plan. Do not install dependencies or edit product code/tests.
- Keep the current isolated worktree, branch `wip/web-session-overview`. Do not touch unrelated files in the main checkout.
- No new dependency is needed. Frontend `node_modules` is absent at planning time. After approval, let `make web-preflight` own installation; never run `npm ci` through a symlink.
- Read `docs/product/README.md`, the Browser workspace row in `docs/product/subsystems.md`, and `docs/developing-evener/testing.md` before implementation. Inspect gate scripts before running them.
- Capture each intentional red test failure, then its passing run. Do not weaken assertions, widen timeouts, hide logs, mock internal stores/renderers, or issue live provider requests.
- All commands below run from the repository root unless a `cd` is shown. File-list paths are repository-relative; the ownership table alone uses frontend-relative paths.

## Review Focus

Each case has a test in its owning task.

1. A remote ref changes, or the same ref is reopened before an old read finishes: show loading/new accepted evidence immediately, never another session's or obsolete evidence. Tasks 1–2.
2. About shares a subscription with summary/transcript readers: release the model holder independently, retain a live transcript, and do not reacquire abandoned collection demand on invalidation/reconnect. Task 2.
3. A phone drawer or menu removes/hides its opener: enter Overview after menu cleanup, trap sequential focus on phones, and return to a visible control for the intended session and pane. Tasks 3 and 5.
4. XL text, three-digit counts, and unbroken identifiers meet narrow widths: retain all labels, complete selectable values, touch floors, and a reachable close control in both themes. Task 5.
5. Activity was the saved main/focused/only pane, with independent sidebar intent or a valid route: drop only Activity, preserve useful survivor focus and preferences, and apply route precedence before Welcome. Task 4.

---

## File structure and ownership

All source paths in this table are relative to `cmd/evener-hub/frontend/`.

| Unit | Files | Responsibility |
| --- | --- | --- |
| About adapter | Create `src/shell/activitybar/AboutTab.tsx` and `AboutTab.test.tsx` | Bind the explicit scope ref to the existing model holder and clock, render shared details, exercise binding/lifetime behavior |
| Categories and intent | `src/shell/statusbar/statusScope.ts`, `src/shell/activitybar/activityTabs.tsx`, `activitySidebarStore.ts`, `activitySidebarStore.test.ts` | Add the chipless category and preserve existing per-session persistence |
| Sidebar interaction | `src/shell/activitybar/ActivitySidebar.tsx`, `ActivitySidebar.test.tsx`, `activitybar.module.css` | Overview naming, local phone entry/containment, existing viewport and disclosure ownership |
| Menu and command adapters | `src/shell/sessionMenu/SessionMenu.tsx`, `src/panes/session/chrome/SessionChrome.tsx`, `src/shell/rail/RailRow.tsx`, `Rail.tsx`, `src/shell/palette/commands.ts` and their existing tests | One Overview action, explicit-session targeting, real composer `/status` |
| Focus return | `src/shell/activitybar/activitySidebarStore.ts`, `src/widgets/focusscope/tabbable.ts`, session menu trigger markup, footer markup | Reuse visibility filtering and preserve connected, visible, session-qualified return targets |
| Saved pane retirement | `src/panes/sessionPanels/index.ts`, `SessionPanelPane.tsx`, `src/shell/paneRegistry.ts`, `routing.ts`, `AppShell.tsx`, `mobile/StackHost.tsx`, `src/stores/panelStoreEviction.ts` and existing tests | Remove only retired registration and production branches, retain Tasks/Details/Sheet ownership |
| Narrow details presentation | `src/panes/session/chrome/DetailsPanel.tsx`, `detailspanel.module.css` | Local full-value wrapping if browser evidence demonstrates clipping; keep shared accounting |
| Browser evidence | `src/dev/shellguard-entry.tsx`, `scripts/shellguard/run.mjs` | Four independent footer identities, five categories, real menu/composer flows, geometry and trusted keyboard input |
| Evergreen docs | Root `docs/product/subsystems.md`, `docs/product/session-activity.md`, `docs/web-ui/README.md`, `docs/web-ui/design-system.md` | Shipped scope, entry points, read ownership, retained/retired surfaces |

Keep existing filenames and exported Activity-domain names unless an actively changed, exclusively user-facing helper needs a clearer name. Do not split or replace entire existing components.

## Preparation after approval

- [ ] Read the three product/testing references above, `docs/developing-evener/README.md`, frontend `package.json`, `scripts/web/web-preflight.sh`, `scripts/web/test-web.sh`, `scripts/web/test-web-browser.sh`, and `make/testing.mk`.
- [ ] Verify checkout and install state, then run the dependency preflight and affected baseline:

```bash
git status --short
git branch --show-current
make help
make web-preflight
cd cmd/evener-hub/frontend
./node_modules/.bin/vitest run --maxWorkers=4 \
  src/shell/activitybar/ActivitySidebar.test.tsx \
  src/shell/activitybar/activitySidebarStore.test.ts \
  src/panes/session/chrome/DetailsPanel.test.tsx \
  src/shell/sessionMenu/SessionMenu.test.tsx \
  src/shell/paneRestore.test.ts
```

Expected: clean tree before execution, correct branch, successful preflight, and baseline tests exiting zero without uncaptured errors. If preflight refuses a mismatched shared install, resolve that install safely rather than bypassing its check. If a test fails, reproduce it and record up to five ordered hypotheses before editing. Fix in-scope failures at their confirmed source.

## Task 1: Add a working, chipless About category

**Files:**
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/AboutTab.tsx`
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/AboutTab.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/statusbar/statusScope.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/activityTabs.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/sessionActivityTestUtils.ts` (transport fixture only)
- Test: `cmd/evener-hub/frontend/src/shell/activitybar/ActivitySidebar.test.tsx`
- Test: `cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.test.ts`
- Test: `cmd/evener-hub/frontend/src/shell/statusbar/StatusBar.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/dev/shellguard-entry.tsx`
- Docs: `docs/product/session-activity.md` (About read owner and category intent)

**Interfaces:**
- Consumes: `ActivityScope.leaf.ref: string`, `useThreadModel(ref: string): ThreadModel | undefined`, `useNowTick(NOW_TICK_MS): number`, `DetailsPanelBody({model, now}: DetailsPanelBodyProps)`.
- Produces: `ActivityTab = "agents" | "jobs" | "watches" | "tasks" | "about"`; `AboutTab({scope}: {scope: ActivityScope})`; existing `openWith`, `openFor`, `setTab`, and `retarget` accept `"about"` without signature or storage-key changes.
- Test fixture addition: `activityDetailsThread(ref: string, overrides?: Partial<Thread>): ThreadReadResponse`, exported from the existing external-transport fixture module.

- [ ] **Step 1: Add the external wire fixture and failing real-sidebar tests.**

Add `Thread` to the type imports in `sessionActivityTestUtils.ts` and this helper. `projectPath` and `gitInfo.branch` are real `Thread` fields; work time and context are real `thread.evener` fields. Snapshot model ID comes from `modelProvider`, not an invented wire `model` field.

```tsx
export function activityDetailsThread(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  const base = activityThread(ref).thread;
  return {
    thread: {
      ...base,
      id: "about-owner",
      modelProvider: "anthropic/claude-sonnet",
      createdAt: 1_780_000_000,
      updatedAt: 1_780_000_060,
      cwd: "/work/session",
      projectPath: "/work",
      gitInfo: { branch: "feature/overview" },
      evener: {
        ...base.evener,
        contextUsed: 42_000,
        contextWindow: 100_000,
        contextPressure: 0.42,
        workMillis: 4_200,
        usage: { inputTokens: 100_000, outputTokens: 20_000 },
        cost: "~$1.00",
      },
      ...overrides,
    },
  };
}
```

Create `AboutTab.test.tsx` using the real sidebar, not a stub category renderer:

```tsx
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { MotionProvider } from "../../motion";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { activityClient, activityDetailsThread } from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { installFocusedScope } from "../statusbar/scopeTestUtils";
import { resetWorkspaceStoreForTests } from "../workspace";
import { ActivitySidebar } from "./ActivitySidebar";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitySidebarStore";

const ref = "remote:about-owner";
function mountSidebar() {
  return render(<MotionProvider><ActivitySidebar /></MotionProvider>);
}
beforeEach(() => installLocalStorage(new MemoryStorage()));
afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
  resetThreadsStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
  vi.restoreAllMocks();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

test("About renders the explicit remote session's hydrated accounting", async () => {
  const client = activityClient();
  client.on("thread/read", ({ ref }) => activityDetailsThread(ref));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("about-owner");
  expect(screen.getByTestId("session-details-context").textContent).toContain("42% used");
  expect(screen.getByTestId("session-details-context").textContent).toContain("58K left");
  expect(screen.getByTestId("session-details-tokens").textContent).toContain("↑100K");
  expect(screen.getByTestId("session-details-tokens").textContent).toContain("↓20K");
  expect(screen.getByTestId("session-details-cost").textContent).toContain("~$1.00");
  expect(screen.getByTestId("session-details-cwd").textContent).toContain("/work/session");
  expect(screen.getByTestId("session-details-project").textContent).toContain("/work");
  expect(screen.getByTestId("session-details-branch").textContent).toContain("feature/overview");
  expect(screen.getByTestId("session-details-created")).toBeTruthy();
  expect(screen.getByTestId("session-details-updated")).toBeTruthy();
  expect(screen.getAllByRole("radio").map(el => el.textContent?.split("\n")[0])).toEqual([
    "Agents", "Jobs", "Watches", "Tasks", "About",
  ]);
  expect(screen.getByRole("radio", { name: "About" }).textContent).toBe("About");
  expect(client.calls.some(call => call.method.endsWith("/list"))).toBe(false);
});
```

Add a `test.each(["ended", "closed"] as const)` variant with `activityDetailsThread(ref, {status: {type: status}})`. Assert no context row, preserved cost/tokens, and visible lifecycle state. Add a sparse variant returning `activityThread(ref)` with empty cwd: assert no Usage or Location heading and no fabricated context, cost, or work time. Keep all existing `DetailsPanel.test.tsx` accounting/partial-token tests; About reuses that exact implementation.

In `ActivitySidebar.test.tsx`, change the existing keyboard test's End expectation to `"about"` and its subsequent Home source radio to About. Keep ArrowRight selecting Jobs. In `StatusBar.test.tsx`, use its existing summary fixture and real `StatusBar` mount; assert the literal footer identities `agents`, `jobs`, `watches`, `tasks` and no `about`, then click each footer and assert its tab and owning pane. Do not compute expectations from `ACTIVITY_TABS`.

- [ ] **Step 2: Run the failing About and keyboard cases.**

```bash
cd cmd/evener-hub/frontend
./node_modules/.bin/vitest run --maxWorkers=4 \
  src/shell/activitybar/AboutTab.test.tsx \
  src/shell/activitybar/ActivitySidebar.test.tsx \
  src/shell/statusbar/StatusBar.test.tsx
```

Expected red: About is absent from the real registry, so its body/radio cannot render and End still selects Tasks. Record the actual failure, rather than treating an unrelated fixture exception as the red proof.

- [ ] **Step 3: Add the adapter, registry entry, and accepted persisted tab.**

```tsx
// src/shell/activitybar/AboutTab.tsx
import { DetailsPanelBody } from "../../panes/session/chrome/DetailsPanel";
import { NOW_TICK_MS, useNowTick } from "../../panes/session/liveness";
import { useThreadModel } from "../../stores/useThreadModel";
import type { ActivityScope } from "../statusbar/statusScope";

export function AboutTab({ scope }: { scope: ActivityScope }) {
  const model = useThreadModel(scope.leaf.ref);
  const now = useNowTick(NOW_TICK_MS);
  if (!model) return <p role="status">Loading session details…</p>;
  return <DetailsPanelBody model={model} now={now} />;
}
```

Add `"about"` to `ActivityTab` and the store's `TABS`. Import `AboutTab` in `activityTabs.tsx` and append:

```tsx
{
  id: "about",
  glyph: null,
  label: "About",
  chipCount: () => "",
  chipLabel: () => "About",
  tabLabel: () => "About",
  chipVisible: () => false,
  Body: AboutTab,
}
```

Keep `ACTIVITY_VIEW_STORAGE_KEY = "evener.activity-sidebar.v1"`, the bounded limit of 100, category view keys, and default Agents unchanged. In shellguard replace `ACTIVITY_TABS.length` as the footer expectation with a literal list:

```tsx
const EXPECTED_PANE_ACTIVITY_TABS = ["agents", "jobs", "watches", "tasks"] as const;
const EXPECTED_PANE_ACTIVITY_CONTROLS = EXPECTED_PANE_ACTIVITY_TABS.length;
```

Use those identities, not just total count, in each footer readiness/measurement check. A five-category registry must not stall shell boot while waiting for a nonexistent fifth chip.

- [ ] **Step 4: Pin reload intent and run the green cases.**

Add this store test using its existing storage/reset setup:

```tsx
test("About reload preserves another category's view intent", () => {
  const store = activitySidebarStore;
  store.getState().retarget("remote:a");
  store.getState().openWith("jobs");
  store.getState().setCategoryView("remote:a", "jobs", {
    shown: 60, anchor: { id: "later-page-job", offset: 17 },
  });
  store.getState().setTab("about");
  store.getState().close();
  resetActivitySidebarStoreForTests({ preserveStorage: true });
  store.getState().retarget("remote:a");
  expect(store.getState()).toMatchObject({ open: false, tab: "about", ref: "remote:a" });
  expect(store.getState().views.get("remote:a")?.categories.jobs).toEqual({
    shown: 60, anchor: { id: "later-page-job", offset: 17 },
  });
  store.getState().openWith();
  expect(store.getState()).toMatchObject({ open: true, tab: "about" });
});
```

Run the Step 2 command plus `activitySidebarStore.test.ts` and `DetailsPanel.test.tsx`. Expected: all pass; the original body retains accounting rules. Update the owning session-activity guide to name About's shared model holder and chipless identity.

- [ ] **Step 5: Format, review, and commit this working slice.**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/shell/activitybar/AboutTab.tsx src/shell/activitybar/AboutTab.test.tsx \
  src/shell/statusbar/statusScope.ts src/shell/statusbar/StatusBar.test.tsx \
  src/shell/activitybar/activityTabs.tsx src/shell/activitybar/activitySidebarStore.ts \
  src/shell/activitybar/activitySidebarStore.test.ts src/shell/activitybar/ActivitySidebar.test.tsx \
  src/stores/sessionActivityTestUtils.ts src/dev/shellguard-entry.tsx
cd ../../..
git diff --check
git add cmd/evener-hub/frontend/src/shell/activitybar/AboutTab.tsx \
  cmd/evener-hub/frontend/src/shell/activitybar/AboutTab.test.tsx \
  cmd/evener-hub/frontend/src/shell/statusbar/statusScope.ts \
  cmd/evener-hub/frontend/src/shell/statusbar/StatusBar.test.tsx \
  cmd/evener-hub/frontend/src/shell/activitybar/activityTabs.tsx \
  cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.ts \
  cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.test.ts \
  cmd/evener-hub/frontend/src/shell/activitybar/ActivitySidebar.test.tsx \
  cmd/evener-hub/frontend/src/stores/sessionActivityTestUtils.ts \
  cmd/evener-hub/frontend/src/dev/shellguard-entry.tsx docs/product/session-activity.md
git diff --cached
git commit -m "feat(web): add chipless About category to session inspection"
```

## Task 2: Prove model lifetimes, live accounting, and retained activity views

**Files:**
- Test: `cmd/evener-hub/frontend/src/shell/activitybar/AboutTab.test.tsx`
- Test: `cmd/evener-hub/frontend/src/shell/activitybar/ActivityViewRetention.test.tsx`
- Test: `cmd/evener-hub/frontend/src/shell/activitybar/ActivityScrollRetention.test.tsx`
- Read/run: `cmd/evener-hub/frontend/src/stores/sessionActivitySubscriptions.test.ts`
- Read/run: `cmd/evener-hub/frontend/src/stores/threads.test.ts`, `cmd/evener-hub/frontend/src/stores/threads.history.test.ts`, `cmd/evener-hub/frontend/src/stores/threads.retirement.test.tsx`
- Production changes only if a new behavior test demonstrates an in-scope cause: `cmd/evener-hub/frontend/src/shell/activitybar/AboutTab.tsx`

**Interfaces:**
- Consumes Task 1's `AboutTab`, `activityDetailsThread`, and real sidebar fixture.
- Consumes `deferred<ThreadReadResponse>()` from `@evener/appwire-client/testing/deferred`, `FakeClient.on`, `emitNotification`, and `calls`.
- Consumes `threadsStore.getState().threads`, `ensureThread(ref): Promise<void>`, `releaseThread(ref): void`, and real `Transcript` pane props `{params: {ref}, paneId, focused}`.
- Produces behavior evidence, not a new retry owner, cache, subscription API, or exported production helper.

- [ ] **Step 1: Add explicit-ref and abandoned-response cases.**

Import `ThreadReadResponse`, `deferred`, `activityThread`, and `Transcript` (default import from `../../panes/transcript/Transcript`) into the About test file. Add the A-to-B test:

```tsx
test("About clears A immediately while B's model read is pending", async () => {
  const pendingB = deferred<ThreadReadResponse>();
  const client = activityClient();
  client.on("thread/read", ({ ref }) => ref === "remote:b"
    ? pendingB.promise
    : activityDetailsThread(ref, { id: "identity-A" }));
  connectionStore.getState().connect(client);
  installFocusedScope("remote:a");
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("identity-A");
  act(() => installFocusedScope("remote:b"));
  expect(screen.queryByText("identity-A")).toBeNull();
  expect(screen.getByRole("status").textContent).toContain("Loading session details");
  await act(async () => pendingB.resolve(activityDetailsThread("remote:b", { id: "identity-B" })));
  await screen.findByText("identity-B");
  expect(screen.queryByText("identity-A")).toBeNull();
});
```

Add a `test.each(["close", "switch-tab"] as const)` sequence: hold the **rich** read where `params.includeTurns === true`, let summary-only reads complete, then close or select Jobs. Reopen About on the same ref, wait for a second rich read, resolve its `activityDetailsThread(ref, {id: "accepted-new"})`, resolve the abandoned rich read last with `id: "obsolete-old"`, and assert only `accepted-new` renders. Use two separate `deferred<ThreadReadResponse>()` gates and a `richReads` counter in the transport handler:

```tsx
const oldRead = deferred<ThreadReadResponse>();
const newRead = deferred<ThreadReadResponse>();
let richReads = 0;
client.on("thread/read", params => {
  if (!params.includeTurns) return activityThread(params.ref);
  richReads += 1;
  return richReads === 1 ? oldRead.promise : newRead.promise;
});
```

After first acquisition, wait for `richReads === 1`; after leaving About, assert `threads.has(ref) === false` even while the open sidebar retains its summary; after reopening, wait for `richReads === 2` before resolving either gate. Do not turn a deduplicated pending read into a fabricated second generation: if this expectation fails, trace the actual holder/lease cancellation before changing code.

Pin same-ref replacement through the supported resync notification, as exercised by `threads.test.ts` and `threads.retirement.test.tsx`:

```tsx
test("About accepts a replacement instance at the same public ref", async () => {
  let instance = "instance-first";
  const client = activityClient();
  client.on("thread/read", ({ ref }) => {
    const response = activityDetailsThread(ref, { id: instance });
    response.thread.evener.instanceId = instance;
    return response;
  });
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("instance-first");
  instance = "instance-replacement";
  act(() => client.emitNotification({
    method: "evener/thread/resync",
    params: { ref, threadId: "instance-first" },
  }));
  await screen.findByText("instance-replacement");
  expect(screen.queryByText("instance-first")).toBeNull();
  expect(threadsStore.getState().threads.get(ref)?.instanceId).toBe("instance-replacement");
});
```

- [ ] **Step 2: Add independent model-holder and later-invalidation proof.**

For sidebar-only demand, open Agents, await its real row, switch to About, await model identity, and snapshot delegate-list call count. Deliver:

```tsx
act(() => client.emitNotification({
  method: "evener/thread/activity/changed",
  params: { ref, threadId: "owner", sessionId: "owner", resources: ["delegates"] },
}));
```

Await the summary/store's settled refresh, then assert no additional delegate-list call and no jobs/watches/tasks collection calls caused by About. Repeat with `client.emitStateChange("reconnecting")` followed by `client.emitReady()`, awaiting the observed summary/rich-read recovery before checking counts. These are the real external client injection APIs used by `taskAggregateReconnect.test.tsx`. Keep this case sidebar-only: a mounted transcript may legitimately own independent activity demand.

For model-holder release, mount About alone, assert an actual `thread/read` with `includeTurns: true`, then switch to another category. Await removal of `threads.get(ref)` while the activity summary remains. Shared subscription count alone is insufficient.

For independent transcript survival, render the production transcript beside the production sidebar:

```tsx
const transcript = render(
  <MotionProvider>
    <Transcript params={{ ref }} paneId="transcript-proof" focused={false} />
  </MotionProvider>,
);
await screen.findByText("No turns yet");
act(() => activitySidebarStore.getState().close());
const unsubscribes = client.calls.filter(call => call.method === "thread/unsubscribe").length;
act(() => client.emitNotification({
  method: "thread/status/changed",
  params: { ref, threadId: "about-owner", status: { type: "active" } },
}));
await waitFor(() => expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("active"));
expect(screen.getByText("No turns yet")).toBeTruthy();
expect(client.calls.filter(call => call.method === "thread/unsubscribe")).toHaveLength(unsubscribes);
transcript.unmount();
await waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(false));
```

This test proves the actual transcript still owns an updating model after About releases it. Do not replace `Transcript`, `useThreadModel`, or store methods with spies/mocks.

- [ ] **Step 3: Add clock, disconnection, and category-return evidence.**

Import `NOW_TICK_MS` from `../../panes/session/liveness`. Hydrate before installing the fake clock, then mount About under that clock so its interval is actually controlled:

```tsx
test("visible About work time advances during an active turn", async () => {
  const now = Date.now();
  const client = activityClient();
  client.on("thread/read", ({ ref }) => {
    const response = activityDetailsThread(ref, { status: { type: "active" } });
    response.thread.evener.activeTurnStartedAt = now - 10_000;
    return response;
  });
  connectionStore.getState().connect(client);
  await threadsStore.getState().ensureThread(ref);
  installFocusedScope(ref);
  vi.useFakeTimers({ now });
  activitySidebarStore.getState().openWith("about");
  await act(async () => { mountSidebar(); });
  act(() => threadsStore.getState().releaseThread(ref));
  const before = screen.getByTestId("session-details-work-time").textContent;
  act(() => vi.advanceTimersByTime(NOW_TICK_MS * 2));
  expect(screen.getByTestId("session-details-work-time").textContent).not.toBe(before);
  expect(activitySidebarStore.getState().tab).toBe("about");
  act(() => client.emitNotification({
    method: "thread/status/changed",
    params: { ref, threadId: "about-owner", status: { type: "ended" } },
  }));
  expect(screen.getByText("ended")).toBeTruthy();
  expect(screen.queryByTestId("session-details-context")).toBeNull();
});
```

Add a separate real-timer recovery case: mount About, await accepted identity/cost, call `client.emitStateChange("reconnecting")` inside `act`, and assert both remain visible. Change the transport's accepted `activityDetailsThread` cost to `"~$2.00"`, call `client.emitReady()`, and await that visible cost. Use the shared store's recovery, not a custom adapter retry. Restore mocks before real timers in cleanup.

In the existing view and scroll retention tests, insert this actual category round trip **after loading multiple pages and recording a visible later-page row**, before their refresh/reconnect assertions:

```tsx
fireEvent.click(screen.getByRole("radio", { name: "About" }));
await screen.findByText("owner");
fireEvent.click(screen.getByRole("radio", { name: /Agents/ }));
```

Keep the existing later-page row, anchor offset, expanded disclosure, and refresh/reconnect assertions unchanged. Extend the existing child/Back journey to select About on a visited child, return to the root, and revisit the child; assert that each session restores its own category. Root activity disclosure/scroll stays intact. About's own viewport stays keyed by `[publicRef, "about"]`.

- [ ] **Step 4: Run new proof cases and the impacted lifetime/retention suite.**

```bash
cd cmd/evener-hub/frontend
./node_modules/.bin/vitest run --maxWorkers=4 \
  src/shell/activitybar/AboutTab.test.tsx \
  src/shell/activitybar/ActivitySidebar.test.tsx \
  src/shell/activitybar/activitySidebarStore.test.ts \
  src/shell/activitybar/ActivityViewRetention.test.tsx \
  src/shell/activitybar/ActivityScrollRetention.test.tsx \
  src/stores/sessionActivitySubscriptions.test.ts \
  src/stores/threads.test.ts src/stores/threads.history.test.ts \
  src/stores/threads.retirement.test.tsx \
  src/panes/session/chrome/DetailsPanel.test.tsx
```

Expected: zero failures and clean logs. These are qualification tests for the adapter and existing owners; some should pass immediately. Record that honestly. If a case fails, follow systematic debugging and change only its confirmed cause. The approved architecture does not authorize replacing shared-store identity/recovery behavior.

- [ ] **Step 5: Format touched test files, inspect their diff, and commit the evidence.**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/shell/activitybar/AboutTab.test.tsx \
  src/shell/activitybar/ActivityViewRetention.test.tsx \
  src/shell/activitybar/ActivityScrollRetention.test.tsx
cd ../../..
git diff --check
git add cmd/evener-hub/frontend/src/shell/activitybar/AboutTab.test.tsx \
  cmd/evener-hub/frontend/src/shell/activitybar/ActivityViewRetention.test.tsx \
  cmd/evener-hub/frontend/src/shell/activitybar/ActivityScrollRetention.test.tsx
git diff --cached
git commit -m "test(web): prove About binding and shared reader lifetimes"
```

If a confirmed adapter fix was required, format and stage that exact additional path and describe the fix in the commit. Do not stage unrelated store changes.

## Task 3: Consolidate entry points and preserve phone focus

**Files:**
- Modify/test: `cmd/evener-hub/frontend/src/shell/sessionMenu/SessionMenu.tsx`, `cmd/evener-hub/frontend/src/shell/sessionMenu/SessionMenu.test.tsx`
- Modify/test: `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.tsx`, `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx`
- Modify/test: `cmd/evener-hub/frontend/src/panes/session/chrome/activityFormat.ts`, `cmd/evener-hub/frontend/src/panes/session/chrome/activityFormat.test.ts`
- Modify/test: `cmd/evener-hub/frontend/src/shell/rail/RailRow.tsx`, `cmd/evener-hub/frontend/src/shell/rail/RailRow.test.tsx`, `cmd/evener-hub/frontend/src/shell/rail/Rail.tsx`, `cmd/evener-hub/frontend/src/shell/rail/Rail.test.tsx`
- Modify/test: `cmd/evener-hub/frontend/src/shell/palette/commands.ts`, `cmd/evener-hub/frontend/src/shell/palette/commands.test.ts`
- Test: `cmd/evener-hub/frontend/src/panes/session/composer/Composer.test.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/chrome/DetailsPanel.test.tsx` (remove obsolete direct-command fixture only)
- Modify/test: `cmd/evener-hub/frontend/src/shell/activitybar/ActivitySidebar.tsx`, `cmd/evener-hub/frontend/src/shell/activitybar/ActivitySidebar.test.tsx`, `cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.ts`, `cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.test.ts`, `cmd/evener-hub/frontend/src/shell/activitybar/activitybar.module.css`
- Modify: `cmd/evener-hub/frontend/src/shell/statusbar/StatusBar.tsx`
- Modify/test: `cmd/evener-hub/frontend/src/widgets/focusscope/tabbable.ts`; run `cmd/evener-hub/frontend/src/widgets/focusscope/focusscope.test.tsx` and `cmd/evener-hub/frontend/src/widgets/focusscope/rehome.test.tsx` unchanged
- Docs: `docs/web-ui/README.md`, `docs/web-ui/design-system.md` (entry points, command, phone surface)

**Interfaces:**
- Consumes Task 1's `openFor(ref: string, tab?: ActivityTab, opener?: HTMLElement): void` and `"about"` tab.
- Replace menu action `onOpenPane(kind)` with `onOpenOverview(): void`; replace `panesOpen` with `overviewOpen: boolean`, and `activityLabel?` with `overviewLabel?: string`. Other actions remain unchanged.
- Add optional `paneId?: string` to `SessionMenuProps`. A trigger's existing inner span carries `data-session-actions-ref={sessionRef}` and `data-pane-id={paneId}`. Rail triggers have a ref and may omit pane ID.
- Replace rail-row callback with `onOpenOverview(session: RailSession): void`, using `RailSession` from `src/shell/rail/railNodes.ts`. Keep `NavigationSessionModel` in SessionMenu where it already belongs.
- Export existing `isRendered(el: Element): boolean` from `widgets/focusscope/tabbable.ts` for return-target reuse. Keep `FocusScope`'s public interface unchanged.
- Preserve `activitySidebarReturnFocusTarget(tab: ActivityTab): HTMLElement | null`, with connected/visible/ref-qualified selection.

- [ ] **Step 1: Make real entry-point tests fail on the old behavior.**

Replace the existing SessionMenu test that expects Details and Activity with:

```tsx
test("inspection group offers Overview without Details", async () => {
  const user = userEvent.setup();
  renderMenu();
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Overview" })).toBeTruthy();
  expect(screen.queryByRole("menuitem", { name: "Details" })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: "Activity" })).toBeNull();
});
```

First run with the existing helper, so it fails on missing Overview. Then update the helper's props/callback to the new interface during implementation. Preserve its Verbosity ordering, rename, pin, archive, delete, shutdown, Stop/Steer, count/unknown, and checked-state assertions. These tests test real menu behavior; callbacks may record dispatch, but no renderer/store is mocked.

In SessionChrome/Rail tests use their existing real mounts and clients: open both menu styles on desktop/phone, assert Overview/no Details, dispatch Overview to a ref different from the focused pane, and assert the existing checked/count/toggle semantics. Replace the `activityActionLabel` output expectation with Overview while keeping the same numeric/unknown rules. Grep its callers first; rename the helper only if all callers are Overview entry points.

Add the real composer command regression to `Composer.test.tsx` using its existing `mountComposerWithHandle`, `textarea`, and `replaceEditorText`:

```tsx
test.each([false, true])("repeated composer /status targets its ref, phone=%s", async (mobile) => {
  vi.stubGlobal("matchMedia", (media: string) => ({
    media,
    matches: mobile && media === "(max-width: 899px)",
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
  const user = userEvent.setup();
  const { fake } = await mountComposerWithHandle("ref_a", {}, {
    focused: true,
    prepare: fake => fake.on("evener/thread/activity/read", params => ({
      ...activitySummary(params.ref), scope: params.scope ?? "session",
    })),
  });
  workspaceStore.setState({
    panes: [
      { id: "command-a", type: "session", params: { ref: "ref_a" }, slot: "main" },
      { id: "focused-b", type: "session", params: { ref: "ref_b" }, slot: "secondary" },
    ],
    focusedPaneId: "focused-b",
  });
  render(<MotionProvider><ActivitySidebar mobile={mobile} /></MotionProvider>);
  for (let invocation = 0; invocation < 2; invocation += 1) {
    const editor = textarea();
    await user.click(editor);
    act(() => replaceEditorText(editor, "/status"));
    await user.keyboard("{Enter}");
    await waitFor(() => expect(activitySidebarStore.getState()).toMatchObject({
      open: true, tab: "about", ref: "ref_a",
    }));
    expect(screen.getByRole("radio", { name: "About" }).getAttribute("aria-checked")).toBe("true");
  }
  expect(workspaceStore.getState().panes.some(pane => pane.type === "sessionDetails")).toBe(false);
  expect(fake.calls.filter(call => call.method === "turn/start")).toHaveLength(0);
});
```

Import real sidebar/Motion/store and `activitySummary` helpers. The existing composer mount supplies the real command-session `thread/read` response. Register real session panes in the file's existing `beforeAll`; add sidebar/workspace resets and `vi.unstubAllGlobals()` to cleanup. The explicit two-pane store fixture tests command targeting; Task 5 supplies actual two-pane browser rendering. The mobile query above is the existing AppShell test's exact query, not a new production viewport API.

Keep useful details/info command search terms and assert completion title equals `Show session details in Overview`. Remove the old direct `/status` test's placeholder `sessionDetails` pane registration in `DetailsPanel.test.tsx` once this real path replaces it; preserve that file's renderer/accounting tests.

- [ ] **Step 2: Run red menu and composer cases.**

```bash
cd cmd/evener-hub/frontend
./node_modules/.bin/vitest run --maxWorkers=4 \
  src/shell/sessionMenu/SessionMenu.test.tsx \
  src/panes/session/chrome/SessionChrome.test.tsx \
  src/shell/rail/Rail.test.tsx \
  src/panes/session/composer/Composer.test.tsx
```

Expected red: old menus expose Activity/Details; composer `/status` toggles Details rather than opening About. Select the new test names with `-t` for fast red iterations, then run the complete files after the fix.

- [ ] **Step 3: Route both menus and `/status` through the existing sidebar.**

SessionMenu's single inspection item has the current count label, checked state, and `actions.onOpenOverview` callback. SessionChrome preserves its existing desktop same-ref toggle and mobile opening behavior. Rail opens/focuses the requested session before `openFor(ref)`. Neither menu selects About unless that session already retained it.

Remove SessionChrome's hidden `DetailsPanel` mount, `detailsRef`, `DetailsPanelHandle` import, details-open subscription, and menu-only `openDetails` path. Keep `ActivityPanel`'s hidden `refreshWhenHidden`/`discoverWhenHidden` owner and its Sheet implementation. Keep the clock used by other chrome components. Do not delete `DetailsPanelBody` or the saved Details renderer.

The status command changes only its discovery title and run action:

```tsx
title: "Show session details in Overview",
// Keep id, scope, and details/info keywords.
run: (ctx) => {
  if (!ctx.sessionRef) return;
  activitySidebarStore.getState().openFor(ctx.sessionRef, "about");
},
```

Keep Tasks' existing `toggleSessionPane` behavior. Change the shared surface's accessible label to Overview, the radiogroup label to Overview kind, the close label to Close Overview, and footer descriptions to `open Overview`. Keep resource-specific loading/empty Activity text.

- [ ] **Step 4: Add focus red tests, then implement a local handoff.**

In the real menu/sidebar tests, after selecting Overview on a phone, await menu dismissal and assert `activity-sidebar.contains(document.activeElement)`. Select About, press Escape, and assert a visible control for the originating pane/ref receives focus. Include a menu opener that unmounts, one hidden by an ancestor, and a phone drawer closed during opening. Unit tests pin intent; Task 5 proves browser traversal and CSS visibility.

Add store selection tests with two pane IDs and two session refs. Build actual `<button><span data-session-actions-ref=... data-pane-id=... /></button>` elements, focus/capture an opener, hide/remove it, and assert `activitySidebarReturnFocusTarget("about")` returns the originating pane's visible trigger, never the other ref's trigger. Repeat after `setTab("jobs")` then `setTab("about")` to prove opener preservation.

Export `isRendered` without changing its logic. In the sidebar store retain the captured public ref as well as opener/pane ID. Capture only external openers; setting a category never captures or replaces the opener. Use the existing visibility helper and `isConnected`, exclude disabled controls, and filter session-action spans by their literal dataset values rather than interpolating an unescaped ref into CSS. Resolve spans with `closest("button")`.

Return preference is: visible connected opener, originating pane's visible session-actions trigger, originating pane's visible category chip when that category has one, another visible session-actions trigger for the intended ref, then that ref's visible category chip. Add `data-session-ref={sessionRef}` to actual StatusBar chips for that last filter. About never gains a chip.

Use this selector implementation with the existing module-level opener/pane variables and a new `activitySidebarOpenerRef: string | null`. Capture that ref in `captureActivitySidebarOpener` after targeting and reset it in the test reset helper:

```tsx
export function activitySidebarReturnFocusTarget(tab: ActivityTab): HTMLElement | null {
  const visible = (element: HTMLElement | null): element is HTMLElement =>
    element !== null && element.isConnected && !element.matches(":disabled") && isRendered(element);
  if (visible(activitySidebarOpener)) return activitySidebarOpener;
  const actions = Array.from(document.querySelectorAll<HTMLElement>("[data-session-actions-ref]"))
    .filter(marker => marker.dataset.sessionActionsRef === activitySidebarOpenerRef)
    .flatMap(marker => {
      const button = marker.closest<HTMLButtonElement>("button");
      return visible(button) ? [{ button, paneId: marker.dataset.paneId }] : [];
    });
  const chips = Array.from(document.querySelectorAll<HTMLElement>("[data-activity-tab]"))
    .filter(chip => chip.dataset.activityTab === tab &&
      chip.dataset.sessionRef === activitySidebarOpenerRef && visible(chip));
  return actions.find(action => action.paneId === activitySidebarOpenerPaneId)?.button ??
    chips.find(chip => chip.dataset.paneId === activitySidebarOpenerPaneId) ??
    actions[0]?.button ?? chips[0] ?? null;
}
```

Phone containment reuses the existing primitive:

```tsx
<FocusScope trap={mobile} autoFocus={false}>
  <div className={CLASS.focusBody}>
    <div className={CLASS.head}>
      <div className={CLASS.scope}>
        <ScopeCrumbs path={scope.path} hierarchy />
        {!scope.ancestryKnown ? <span className={CLASS.pending}>Finding session context…</span> : null}
      </div>
      <IconButton label="Close Overview" icon="×" variant="quiet" size="sm" onClick={close} />
    </div>
    <div className={CLASS.tabs}>
      <SegmentedControl<ActivityTab>
        label="Overview kind" hideLabel size="sm" fullWidth value={tab}
        onChange={next => activitySidebarStore.getState().setTab(next)}
        options={ACTIVITY_TABS.map(spec => ({
          value: spec.id, label: spec.tabLabel(scope.counts), accessibleLabel: spec.chipLabel(scope.counts),
        }))}
      />
    </div>
    <DisclosurePersistenceContext.Provider value={JSON.stringify([scope.leaf.ref, tab])}>
      <ActivityViewport key={JSON.stringify([scope.leaf.ref, tab])}
        sessionRef={scope.leaf.ref} tab={tab} className={CLASS.body}>
        {Body === null ? null : <Body scope={scope} />}
      </ActivityViewport>
    </DisclosurePersistenceContext.Provider>
  </div>
</FocusScope>
```

Wrap these existing nodes inside the current guarded aside, and give the aside `aria-label="Overview"`. Add `CLASS.focusBody` using the existing `requireClass` pattern. Give only the sidebar-local wrapper this flex chain:

```css
.focusBody {
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
  min-block-size: 0;
}
.sidebar > div,
.sidebarMobile > div {
  display: flex;
  flex: 1 1 auto;
  min-block-size: 0;
}
```

Focus entry is a sidebar effect on `open`, explicit `ref`, and `mobile`, **not** model/category changes. Import `tabbable` from the same focus primitive and add:

```tsx
useEffect(() => {
  if (!open || !mobile || ref === null) return;
  const frame = requestAnimationFrame(() => {
    const element = sidebar.current;
    if (!element || !activitySidebarStore.getState().open) return;
    const selected = element.querySelector<HTMLElement>('[role="radio"][aria-checked="true"]');
    (selected ?? tabbable(element)[0])?.focus();
  });
  return () => cancelAnimationFrame(frame);
}, [open, mobile, ref]);
```

Menu `selectItem` calls its action before its FocusScope cleanup restores the opener, so immediate focus from that callback is incorrect. Keep desktop nonmodal; keep Escape's `defaultPrevented` behavior. Resolve close return after the visible underlying surface is available and guard against a newer sidebar opening. Use browser evidence to check this timing rather than increasing delays.

- [ ] **Step 5: Run the whole impacted entry/focus suite and commit.**

Run Step 2 plus `ActivitySidebar.test.tsx`, `activitySidebarStore.test.ts`, `StatusBar.test.tsx`, `activityFormat.test.ts`, `commands.test.ts`, and `DetailsPanel.test.tsx`. Format each touched TypeScript file from the frontend directory. Update the two owning web guides, then stage only this task's listed changed paths, read `git diff --cached`, and commit:

```bash
git diff --check
git add cmd/evener-hub/frontend/src/shell/sessionMenu/SessionMenu.tsx \
  cmd/evener-hub/frontend/src/shell/sessionMenu/SessionMenu.test.tsx \
  cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.tsx \
  cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx \
  cmd/evener-hub/frontend/src/panes/session/chrome/activityFormat.ts \
  cmd/evener-hub/frontend/src/panes/session/chrome/activityFormat.test.ts \
  cmd/evener-hub/frontend/src/panes/session/chrome/DetailsPanel.test.tsx \
  cmd/evener-hub/frontend/src/shell/rail/RailRow.tsx \
  cmd/evener-hub/frontend/src/shell/rail/RailRow.test.tsx \
  cmd/evener-hub/frontend/src/shell/rail/Rail.tsx \
  cmd/evener-hub/frontend/src/shell/rail/Rail.test.tsx \
  cmd/evener-hub/frontend/src/shell/palette/commands.ts \
  cmd/evener-hub/frontend/src/shell/palette/commands.test.ts \
  cmd/evener-hub/frontend/src/panes/session/composer/Composer.test.tsx \
  cmd/evener-hub/frontend/src/shell/activitybar/ActivitySidebar.tsx \
  cmd/evener-hub/frontend/src/shell/activitybar/ActivitySidebar.test.tsx \
  cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.ts \
  cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.test.ts \
  cmd/evener-hub/frontend/src/shell/activitybar/activitybar.module.css \
  cmd/evener-hub/frontend/src/shell/statusbar/StatusBar.tsx \
  cmd/evener-hub/frontend/src/widgets/focusscope/tabbable.ts \
  docs/web-ui/README.md \
  docs/web-ui/design-system.md
git diff --cached
git commit -m "feat(web): consolidate session menus and status in Overview"
```

Do not commit before named-path staging and the green checks. Task 5's browser guard remains required before calling phone focus behavior verified.

## Task 4: Retire Activity workspace placements without migration

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/sessionPanels/index.ts`, `cmd/evener-hub/frontend/src/panes/sessionPanels/SessionPanelPane.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/paneRegistry.ts`, `cmd/evener-hub/frontend/src/shell/routing.ts`, `cmd/evener-hub/frontend/src/shell/AppShell.tsx`, `cmd/evener-hub/frontend/src/shell/mobile/StackHost.tsx`
- Modify: `cmd/evener-hub/frontend/src/stores/panelStoreEviction.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.ts`
- Test: `cmd/evener-hub/frontend/src/panes/sessionPanels/index.test.ts`, `cmd/evener-hub/frontend/src/panes/sessionPanels/sessionPanelPane.test.tsx`
- Test: `cmd/evener-hub/frontend/src/shell/paneRestore.test.ts`, `cmd/evener-hub/frontend/src/shell/DockHost.test.tsx`, `cmd/evener-hub/frontend/src/shell/routing.test.ts`, `cmd/evener-hub/frontend/src/shell/AppShell.test.tsx`
- Test: `cmd/evener-hub/frontend/src/stores/panelStoreEviction.test.ts`
- Test: `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx`, `cmd/evener-hub/frontend/src/shell/rail/Rail.test.tsx`
- Read/preserve: `cmd/evener-hub/frontend/src/panes/session/chrome/ActivityPanel.test.tsx`
- Docs: `docs/product/subsystems.md`, `docs/product/session-activity.md`, affected `docs/web-ui/design-system.md` clauses

**Interfaces:**
- Consume existing opaque saved Dockview layout and `workspaceStore.restoreLayout`, existing `DockHost` route capture/reapply/Welcome sequence, and About's existing persistence key.
- Produce `SessionPanelKind = "tasks" | "details"` and a pane registry without `"sessionActivity"`. Retain `"sessionTasks"` and `"sessionDetails"` IDs and their params.
- No Activity migration function, route alias, subtree scope adapter, or deletion API is produced.

- [ ] **Step 1: Add real-host retirement tests while Activity is still registered.**

Keep `paneRestore.test.ts`'s saved `p3` Activity fixture, change its expectation to omission, and preserve the session/Tasks/Details/doc/transcript survivors. Update registration tests to require only Tasks and Details. Do not re-register retired Activity as a fixture.

Extend `DockHost.test.tsx` using its real-save approach at the existing retired-pane test. Save real pane placements through `DockHost`, unmount to flush the pending save, then change only a serialized pane's `params`:

```tsx
const raw = localStorage.getItem(LAYOUT_KEY);
if (raw === null) throw new Error("Expected a real saved Dockview layout");
const layout = JSON.parse(raw) as { panels: Record<string, { params?: unknown }> };
const pane = layout.panels[retiredPaneId];
if (!pane) throw new Error(`Missing saved pane ${retiredPaneId}`);
pane.params = { paneType: "sessionActivity", paneParams: { ref: "remote:a" } };
localStorage.setItem(LAYOUT_KEY, JSON.stringify(layout));
resetWorkspaceStoreForTests();
```

`retiredPaneId` is the actual ID returned by `openPane("doc", ...)` before the save, not a guessed generated ID. Run this matrix through the real host with the registered production session and Details panes, scripted external thread/summary reads, and a doc survivor:

| Saved input | Assertions after boot |
| --- | --- |
| Mixed session, Details, doc, Activity | Session/Details/doc bodies render; Activity absent from store and DOM; useful survivor focus |
| Same mix, Activity saved main | A surviving pane owns main; no workspace chunk error |
| Same mix, Activity saved focused | Focus resolves to an actual surviving pane; visible secondary tabs remain reachable |
| Each mix, Overview saved closed/About | Closed/About remains closed/About; retirement does not open/retarget it |
| Each mix, Overview saved open/About | Open/About restores for the surviving session and renders its accepted identity |
| Activity only, no valid primary route | Existing “No session open” Welcome renders |
| Activity only, valid session route | Actual routed session renders as main; no spurious Welcome or Activity |

Seed independent Overview preferences through real `retarget`, `openWith("about")`, and `close`; preserve the `evener.activity-sidebar.v1` storage while resetting in-memory sidebar state. Mount real `ActivitySidebar` beside the real workspace host to prove visible open-on-About restoration. For a valid routed primary use the existing AppShell routing path, or the DockHost fixture's captured route setup that AppShell already performs; also exercise at least one valid session URL through real AppShell.

Assert transport `client.calls` has no mutation/deletion methods after restore. Record read-only call method names and explicitly reject the actual deletion/archive/lifecycle methods used by the existing session menu API. Do not assert zero total calls: restored panes legitimately hydrate.

- [ ] **Step 2: Run the red restoration tests.**

```bash
cd cmd/evener-hub/frontend
./node_modules/.bin/vitest run --maxWorkers=4 \
  src/shell/paneRestore.test.ts src/shell/DockHost.test.tsx \
  src/panes/sessionPanels/index.test.ts src/shell/AppShell.test.tsx
```

Expected red: registered Activity survives or renders instead of being omitted. A wrong fixture or missing transport handler is not the retirement regression.

- [ ] **Step 3: Remove the registered type and production branches.**

Delete the `registerPane` block for `sessionActivity` and its union member. Remove `activity` from `SessionPanelKind` and its mapping/body branch. Remove only Activity-specific branches in AppShell's companion selection, routing's non-URL case, StackHost's `panelPaneTypes`, and `panelStoreEviction`'s pane keepalive list. Remove `closeSessionActivityPanes` and its `openFor` call: there is no registered Activity placement left to close.

Keep the existing shared restoration algorithm unchanged unless a red test proves it violates the approved contract. It already removes unknown panels synchronously from the live Dockview API, selects the first survivor as main, and picks useful surviving focus. DockHost already reapplies valid captured routes before its Welcome fallback. Add no Activity-specific restore code.

Keep `DetailsPaneBody` hydration, title, clock, and loading behavior; keep Tasks' current behavior. Remove renderer imports only when the removed pane was their last caller. Keep `ActivityPanelBody` and recursive Sheet/discovery code where still used.

The retained panel union and existing function signatures become:

```tsx
export type SessionPanelKind = "tasks" | "details";
export function sessionPanelPaneType(kind: SessionPanelKind): "sessionTasks" | "sessionDetails" {
  return kind === "tasks" ? "sessionTasks" : "sessionDetails";
}
export function sessionPanelTitle(kind: SessionPanelKind, ref: string, name?: string): string {
  const label = kind === "tasks" ? "Tasks" : "Details";
  return `${label} · ${name || ref}`;
}
```

In `PaneTypeId`, delete only the `"sessionActivity"` member. In the registrations, delete only the Activity descriptor; retain the Details descriptor unchanged. The serialized fixture strings deliberately remain outside that production union.

- [ ] **Step 4: Preserve meaningful old tests and run the green matrix.**

Replace obsolete “open menu closes orphan Activity pane” cases in SessionChrome/Rail tests with the real saved-layout omission proof. Move activity body assertions from removed workspace-pane tests to `ActivityPanel.test.tsx` if they cover retained recursive behavior not already asserted there. Keep every existing Details/Tasks accounting, hydration, and eviction assertion. Restore a registered saved Details pane through the host and assert the actual `session-details-cost`, context, and location body, not a placeholder label.

Run Step 2 plus `sessionPanelPane.test.tsx`, `routing.test.ts`, `panelStoreEviction.test.ts`, SessionChrome/Rail tests, and `ActivityPanel.test.tsx`. Search production source for remaining `sessionActivity` and resolve each real reference; serialized test fixtures must remain:

```bash
rg -n 'sessionActivity|closeSessionActivityPanes' cmd/evener-hub/frontend/src
```

Expected green: all survivors render, independent intent is preserved, valid route precedence wins, Activity-only un-routed layouts reach Welcome, and no resources are deleted. Update the subsystem and activity guides with session scope, retired placements, retained Details/Sheet/discovery ownership, and restoration behavior.

- [ ] **Step 5: Format, stage only Task 4's changed paths, review, and commit.**

Run pinned Biome on each changed source/test path from the frontend directory. Run `git diff --check`, named-path `git add` for the files listed above that actually changed, and `git diff --cached` before:

```bash
git add cmd/evener-hub/frontend/src/panes/sessionPanels/index.ts \
  cmd/evener-hub/frontend/src/panes/sessionPanels/SessionPanelPane.tsx \
  cmd/evener-hub/frontend/src/shell/paneRegistry.ts \
  cmd/evener-hub/frontend/src/shell/routing.ts \
  cmd/evener-hub/frontend/src/shell/AppShell.tsx \
  cmd/evener-hub/frontend/src/shell/mobile/StackHost.tsx \
  cmd/evener-hub/frontend/src/stores/panelStoreEviction.ts \
  cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.ts \
  cmd/evener-hub/frontend/src/panes/sessionPanels/index.test.ts \
  cmd/evener-hub/frontend/src/panes/sessionPanels/sessionPanelPane.test.tsx \
  cmd/evener-hub/frontend/src/shell/paneRestore.test.ts \
  cmd/evener-hub/frontend/src/shell/DockHost.test.tsx \
  cmd/evener-hub/frontend/src/shell/routing.test.ts \
  cmd/evener-hub/frontend/src/shell/AppShell.test.tsx \
  cmd/evener-hub/frontend/src/stores/panelStoreEviction.test.ts \
  cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx \
  cmd/evener-hub/frontend/src/shell/rail/Rail.test.tsx \
  cmd/evener-hub/frontend/src/panes/session/chrome/ActivityPanel.test.tsx \
  docs/product/subsystems.md \
  docs/product/session-activity.md \
  docs/web-ui/design-system.md
git diff --cached
git commit -m "feat(web): retire Activity workspace panes without migration"
```

## Task 5: Qualify real-browser focus and narrow Overview geometry

**Files:**
- Modify: `cmd/evener-hub/frontend/src/dev/shellguard-entry.tsx`
- Modify: `cmd/evener-hub/frontend/scripts/shellguard/run.mjs`
- Modify when reproduced: `cmd/evener-hub/frontend/src/shell/activitybar/activitybar.module.css`
- Modify when reproduced: `cmd/evener-hub/frontend/src/panes/session/chrome/DetailsPanel.tsx`, `cmd/evener-hub/frontend/src/panes/session/chrome/detailspanel.module.css`
- Docs: affected final presentation/interaction clauses in `docs/web-ui/design-system.md`

**Interfaces:**
- Consume the real `AppShell`, real session/rail menu markers from Task 3, real composer, `prefsStore.setFontSize("xl")`, `prefsStore.setTheme("light" | "dark")`, and the harness's existing scripted `FakeClient`.
- Consume `connectPage(...).send`, `applyViewport`, `navigateTo`, `evaluate`, `waitForFonts`, and `window.settledShell` from the existing gated shellguard runner.
- Produce additional shellguard failures/measurements through its existing exit status. Do not create an ungated preview-only harness or use the shared browser/server as the test origin.

- [ ] **Step 1: Add the independent browser oracle and long real-wire values.**

Retain all existing shellguard measurements. Extend the scripted `thread/read` response using Task 1's wire fixture and deterministic long fields:

```tsx
const LONG_MODEL = `anthropic/${"modelidentifier".repeat(12)}`;
const LONG_ID = "sessionidentifier".repeat(12);
const LONG_BRANCH = `feature/${"branchidentifier".repeat(12)}`;
const LONG_PATH = `/work/${"directorysegment".repeat(12)}/session`;
// In the existing external thread/read handler:
const response = activityDetailsThread(ref, {
  id: LONG_ID,
  modelProvider: LONG_MODEL,
  cwd: LONG_PATH,
  projectPath: "/work",
  gitInfo: { branch: LONG_BRANCH },
});
```

Preserve the existing tasks and crowded summary setup. Set task counts in their real owning wire/navigation fields; assert all four rendered category counts contain `100`. Expected labels are the literal five labels, and footer identities are the literal four identities, independent of the production registry.

Add an Overview flow to the existing harness: click the actual rail's session-actions marker for `local:p0-s0`, choose Overview in the real menu, and select the About radio. On phones first click the actual Sessions drawer trigger if the intended rail marker is hidden. Rail Overview opens the actual session before inspection. Do not use the transparent footer geometry fixtures as evidence for menu targeting or focus.

Add a harness measurement returning: theme/font attributes, sidebar bounds, literal category texts and selected value, close-button bounds, each label's text Range bounds, long-value text Range bounds versus every clipping ancestor, document overflow, selectable text, and actual control tap-floor sizes. The sidebar body may scroll vertically; the page must not.

Use `Range.selectNodeContents`/`getClientRects` to test full text against clipping ancestors. A small `scrollWidth` or lack of page overflow cannot prove text inside `overflow:hidden` is readable. For contained scrolling, assert the scroll container can reach the complete value; for wrapping, assert every text rect lies inside its content bounds. Programmatically select each long text and assert `window.getSelection()?.toString()` equals the literal full expected value, then remove the ranges. Check all four long values, not just cwd.

Use a test-side oracle such as this for each exact long string; it consumes a real rendered element and the independent expected fixture value:

```tsx
function measureFullText(element: HTMLElement, expected: string) {
  const range = document.createRange();
  range.selectNodeContents(element);
  const rects = Array.from(range.getClientRects());
  const inlineClips: string[] = [];
  const scrollContainers: { clientWidth: number; scrollWidth: number; reachable: number }[] = [];
  for (let node: HTMLElement | null = element; node; node = node.parentElement) {
    const style = getComputedStyle(node);
    const box = node.getBoundingClientRect();
    if (["hidden", "clip"].includes(style.overflowX) &&
        rects.some(rect => rect.left < box.left - 1 || rect.right > box.right + 1)) {
      inlineClips.push(node.tagName);
    }
    if (["auto", "scroll"].includes(style.overflowX) && node.scrollWidth > node.clientWidth) {
      const previous = node.scrollLeft;
      node.scrollLeft = node.scrollWidth;
      scrollContainers.push({ clientWidth: node.clientWidth, scrollWidth: node.scrollWidth,
        reachable: node.scrollLeft });
      node.scrollLeft = previous;
    }
  }
  const selection = window.getSelection();
  selection?.removeAllRanges();
  selection?.addRange(range);
  const selectable = selection?.toString() === expected;
  selection?.removeAllRanges();
  return { fullText: element.textContent === expected, selectable, inlineClips, scrollContainers,
    textRects: rects.map(rect => ({ left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom })) };
}
```

Find the leaf value element by its exact expected text within the real sidebar, fail if absent, and require `fullText`, `selectable`, and no inline clipping after the local fix. Where contained scrolling is chosen, adjust the oracle to compare rects in that scroll container's content coordinates and assert its full range is reachable; do not exempt unrelated clipped ancestors. Vertical body scrolling is legitimate and is tested by scrolling each value into view, not by requiring all rows on one screen.

- [ ] **Step 2: Add trusted-keyboard and real `/status` browser scenarios.**

Extend the existing runner with a local key helper using supported CDP input:

```js
async function pressKey(send, key, code, keyCode, modifiers = 0) {
  await send("Input.dispatchKeyEvent", {
    type: "keyDown", key, code,
    windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode, modifiers,
  });
  await send("Input.dispatchKeyEvent", {
    type: "keyUp", key, code,
    windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode, modifiers,
  });
}
```

Use Tab (`"Tab", "Tab", 9`) and Shift+Tab (`modifiers = 8`) through CDP. On phones assert every focus step stays in the named Overview surface, including first/last wrap. On desktop assert normal traversal can leave it. Use real Arrow/Home/End events to assert category selection and computed visible focus styling; do not dispatch synthetic Tab events and claim the browser traversed.

Test both opening sources: real menu selection and actual composer `/status` submission. For the latter focus the session's `[role="textbox"][aria-label="Message"]`, enter `/status` with `Input.insertText`, submit with trusted Enter, and assert open/About for that session. Repeat after focusing a different pane on desktop. Assert no Details pane and no provider turn request in the scripted transport log.

On dismissal test close and Escape separately. Include a rail menu launched from a phone drawer that closes, a removed opener, and two same-session desktop panes with different originating IDs. Check the returned active control is rendered, connected, belongs to the expected pane/ref, and is not behind a closed drawer. Switch an activity category to About before dismissal to prove opener retention. Settle on actual DOM/focus conditions and completed finite animations, not fixed sleeps.

- [ ] **Step 3: Run the geometry/focus red matrix before changing presentation.**

Add these cases to `scripts/shellguard/run.mjs`'s existing guarded main, with the same private Vite/Chrome lifecycle and cleanup:

```js
const overviewViewports = [
  { width: 1400, height: 900 },
  { width: 390, height: 844, mobile: true, touch: true },
  { width: 320, height: 844, mobile: true, touch: true },
];
const overviewThemes = ["light", "dark"];
```

For each pair load a fresh guard page, await `settledShell`, set and verify XL/theme state, await fonts/finite animations, run actual flows, and collect assertions. Desktop sidebar must remain 320px. Phone categories/close controls must meet the computed existing tap floor. Assert every label/count, value, selection, and focus property; a harness that failed to render the intended surface must fail the test.

```bash
cd cmd/evener-hub/frontend
npm run shellguard
```

Expected red if equal-width tabs truncate or InspectorCard clips values; record the measured failing bounds. If a scenario already passes, preserve it and report no fix was needed. Do not infer a geometry failure from source alone.

- [ ] **Step 4: Make only the reproduced local presentation fixes.**

If the five equal-width options truncate, change only the sidebar's `.tabs` descendants to a wrapping, content-sized strip. Preserve the shared SegmentedControl widget, radio semantics, key selection, theme tokens, touch floors, and 320px sidebar width. Start with this local rule, then retain only what the browser matrix proves necessary:

```css
.tabs [role="radiogroup"] {
  display: flex;
  flex-wrap: wrap;
}
.tabs [role="radio"] {
  inline-size: auto;
  flex: 1 1 max-content;
  block-size: auto;
  min-block-size: 28px;
  padding-block: var(--space-1);
}
.tabs [role="radio"] > span {
  white-space: pre-line;
  overflow: visible;
  text-overflow: clip;
}
```

Check the phone media rule's existing `--tap-min` floor still wins. Do not shrink text or touch targets to make the assertion green.

If Session identity rows clip, add a local class on the wrapping div around the existing Session InspectorCard and locally allow its values to wrap, with `min-inline-size: 0` and `overflow-wrap: anywhere`. If the widget markup offers no stable local selector, compose the existing local `Section`/`DetailRow` for just the three Session fields; preserve exactly `modelLabel(...)`, status, and thread ID. Do not change generic InspectorCard CSS or accounting. Add `min-inline-size: 0`/wrapping to the existing details value/path rules when needed for branch/path rows.

Keep text selectable. Keep cost supplied by the server, partial-token labels, omitted values/sections, and ended-context suppression unchanged. Rerun `DetailsPanel.test.tsx` after any shared-body presentation edit.

- [ ] **Step 5: Run the green matrix and commit the qualified presentation.**

Run `npm run shellguard` to completion and the impacted sidebar/details unit tests. Format touched in-scope TypeScript through pinned Biome; `.mjs` browser harness files are outside the frontend lint scope, so preserve their established formatting. Update the owning design-system clauses, stage only changed paths from this task, read the staged diff, and commit:

```bash
git diff --check
git add cmd/evener-hub/frontend/src/dev/shellguard-entry.tsx \
  cmd/evener-hub/frontend/scripts/shellguard/run.mjs \
  cmd/evener-hub/frontend/src/shell/activitybar/activitybar.module.css \
  cmd/evener-hub/frontend/src/panes/session/chrome/DetailsPanel.tsx \
  cmd/evener-hub/frontend/src/panes/session/chrome/detailspanel.module.css \
  docs/web-ui/design-system.md
git diff --cached
git commit -m "fix(web): qualify Overview focus and narrow layouts"
```

Capture representative actual Chrome screenshots at desktop/320px/390px with XL text in both themes for human inspection. Keep evidence in `$EVENER_SCRATCH_DIR`. Browser evidence proves Chrome behavior; do not claim Safari/native-device qualification.

## Acceptance coverage

| Spec proof obligation | Owning task and check |
| --- | --- |
| 1, session/rail Overview menus, counts, checks, targeting | Task 3 real-menu suite, Task 5 real browser menu flows |
| 2, five categories, four footer chips, keyboard/counter behavior | Task 1 real sidebar/footer suite, Task 5 independent browser identities |
| 3, hydrated live/ended/sparse/remote details and absence rules | Task 1 About renderer plus unchanged Details accounting suite |
| 4, active clock and visible model updates | Task 2 clock and notification cases |
| 5, explicit ref, pending/abandoned response and same-ref replacement | Task 2 binding/generation cases |
| 6, rich holder/release, live transcript, later collection invalidation | Task 2 separate lifetime fixtures |
| 7, per-session/child/Back/reload/disclosure/later-page retention | Tasks 1–2 store and real retention journeys |
| 8, real composer `/status`, idempotence, wrong focused pane, no turn | Task 3 composer suite, Task 5 trusted browser submission |
| 9, saved Details body remains registered/restored | Task 4 real-host Details restoration |
| 10, phone entry/traversal, visible return to intended pane/ref | Tasks 3 and 5 intent and trusted-keyboard proofs |
| 11, 320px desktop and 320/390px phone, XL, counts, both themes | Task 5 full geometry/value/tap/focus matrix |
| 12, mixed/main/focused/only Activity placements, intent, route, no deletion | Task 4 real-host/AppShell restoration matrix |

## Final verification, simplification, and PR handoff

- [ ] Review the finished behavior against every spec section and the table above. Verify the retained recursive Sheet/discovery contracts and all non-goals. Resolve each missing proof before submission.
- [ ] Read all four owning guides as current-behavior documents. Remove stale menu/standalone Activity-pane promises from the touched clauses while retaining the distinct Activity Sheet and Activity transcript detail level. Do not rewrite historical specs.
- [ ] Run `simplify-code:simplify-code` as Jesse requested, preserving behavior and all red/green proof. Rerun checks for every simplification edit and commit it separately with named-path staging.
- [ ] Run a fresh whole-branch code review using `superpowers:requesting-code-review`; give the reviewer both spec/plan paths, the base/head commits, changed files, and evidence. Reproduce actionable findings red-first and resolve all in-scope failures. Inline execution applies to implementation; it does not skip independent final review.
- [ ] Inspect `scripts/web/test-web.sh` and `scripts/web/test-web-browser.sh` and their invoked harnesses before reporting what they tested. Format all touched `src/` files with the pinned frontend Biome, then run from the repository root:

```bash
git diff --check
make test-web
make test-web-browser
```

Both gates must run to completion, exit zero, and show no uncaptured failures. The browser gate includes shellguard alongside its other real-browser guards. A missing Chrome, failed launch, timeout, or install refusal is not a passing gate. Do not run a long local `make test` merely to duplicate CI.

- [ ] Report the implementation result and evidence to Jesse, including any unverified platform behavior. Keep each evidence file in the scratch directory and leave the requested deliverables in the worktree.
- [ ] Before opening the PR, read the final diff and clean/staged state. Load `shepherd-pr:shepherd-pr` as requested. Push this branch once per complete review round; let CI own full-repository lint/vet/test gates.
- [ ] Read RoboRev's combined findings and this branch's per-commit open reviews. Use the shepherd settle detector as one background job, with no orphaning `&`. Reconcile every finding by failing/passing evidence or source-grounded refutation. Do not close unrelated lanes' reviews.
- [ ] Merge the required base before approval, check head stability, and follow the repository's approval/merge rules. Green review execution does not imply clean review findings. Final reporting names the PR, verified head, delivered behavior, and anything left.

## Plan self-review

- Spec coverage: all 12 proof obligations map to tasks above; scope/retirement and four guide updates have explicit owners.
- Type/API check: About uses real `ActivityScope`, `ThreadReadResponse`, `Thread`, model/clock APIs, and real composer/host helpers. Browser input uses CDP rather than synthetic default traversal. No invented standalone URL, wire model field, or mobile helper is required.
- Independence: footer/category expected identities are literals; geometry examines complete text against clipping ancestors, not only page overflow. Lease and summary evidence are separate.
- Review Focus: each of the five risk classes has its own owner and check. Shared recovery remains shared; no migration or broadening is introduced.
- Gate state: this is the implementation plan awaiting Jesse's approval. No product changes, dependency installs, or test execution were performed while writing it.
