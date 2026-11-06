import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { AnyNotification, Thread } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { appliedReceipt } from "../../mutationReceiptTestUtils";
import { resetActivitySidebarStoreForTests } from "../../shell/activitybar/activitySidebarStore";
import { ClientProvider } from "../../shell/clientContext";
import * as StatusBarModule from "../../shell/statusbar/StatusBar";
import { resetMobileViewportForTests } from "../../shell/useIsMobile";
import { resetWorkspaceStoreForTests } from "../../shell/workspace";
import { resetActivityPanelStoreForTests } from "../../stores/activityPanel";
import { resetConnectionStoreForTests } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../stores/threads";
import { ALL_CAPABILITIES as CAPABILITIES } from "../../threadTestUtils";
import * as SessionChromeModule from "./chrome/SessionChrome";
import { resetAskDockStoreForTests } from "./composer/askDock/askDockStore";
import { askPendingStatusChanged } from "./composer/askDock/askDockTestUtils";
import * as ComposerModule from "./composer/Composer";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "./composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./composer/queue/testing/flushPendingTurnsProjection";
import "./index";
import { CommittedSession as Session } from "./testing/CommittedSession";
import { seedHeld } from "./transcript/messages/testing/heldSteerTestUtils";
import { resetTranscriptPagingForTests } from "./transcript/useTranscript";
import "./testing/editorGeometry";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { readModuleCss } from "../../styles/cssBlock";
import { connectReadyFakeClient } from "../../testing/connectionTestUtils";
import { readResponse, scrollRootOf, stubScrolledAway, versionedReadResponse } from "./sessionTestUtils";
import * as useTranscriptScrollModule from "./transcript/flow/useTranscriptScroll";

// The session footer's composer boundary is swapped for a visible stub here
// ONLY to prove Session.tsx mounts it with the right ref and no longer adds a
// standalone SessionChrome sibling. Composer.test.tsx proves the real composer
// owns the inline SessionChrome; its marker is mirrored inside this boundary so
// this suite can pin the Session-level placement without duplicating Composer's
// own behavior tests.
//
// vi.spyOn, not a hoisted vi.mock, so individual tests can hand the real
// Composer/SessionChrome back mid-file and render them. Re-stubbed in
// beforeEach below too, not just once here: a test that restores a slot
// without re-stubbing it (test.each has no onTestFinished) would otherwise
// leave the real component in place for every later test in this file.
function stubSessionSlots(): void {
  vi.spyOn(ComposerModule, "Composer").mockImplementation(({ ref }: { ref: string }) => (
    <div data-testid="composer-slot">
      {ref}
      <div data-testid="session-chrome-inline" />
    </div>
  ));
  vi.spyOn(SessionChromeModule, "SessionChrome").mockImplementation(({ ref }: { ref: string }) => (
    <div data-testid="session-chrome">{ref}</div>
  ));
  vi.spyOn(StatusBarModule, "StatusBar").mockImplementation(() => <></>);
}

stubSessionSlots();

// jsdom performs no real layout (every element's offsetHeight is 0, no
// ResizeObserver) - VirtualList's own test suite stubs this for the exact
// same reason (see widgets/virtuallist/virtuallist.test.tsx's file-level
// comment): without it, @tanstack/react-virtual sees a 0px-tall viewport
// and never renders a single row, which wouldn't exercise TurnBlock at all.
const CONTAINER_HEIGHT = 500;

let offsetHeightDescriptor: PropertyDescriptor | undefined;

let mutationStorage: MutationOutboxIndexedDB;

// jsdom computes no layout, so the transcript's scroll port reads as
// zero-tall (every scroll* property is 0). The offsetHeight stub below is what
// gives it a rendered box, which is what scrollMetrics.shouldAutoLoadOlder
// requires before it reads "nothing overflows" as the "too short to fill"
// shape - so a pane rendered here pages on its own, the way a real browser's
// short first page does. LoadOlderRow's own suite drives the geometry and the
// resize re-check explicitly.

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  stubSessionSlots();
  globalThis.indexedDB = new IDBFactory();
  globalThis.IDBKeyRange = IDBKeyRange;
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
  resetAskDockStoreForTests();
  resetNavigationStoreForTests();
  resetActivityPanelStoreForTests();
  resetActivitySidebarStoreForTests();
  resetMobileViewportForTests();
  mutationStorage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(mutationStorage);
  resetPendingTurnsStoreForTests();
  localStorage.clear();
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: CONTAINER_HEIGHT });
});

afterEach(() => {
  cleanup();
  resetPendingTurnsStoreForTests();
  resetAskDockStoreForTests();
  resetActivityPanelStoreForTests();
  resetActivitySidebarStoreForTests();
  resetMobileViewportForTests();
  resetWorkspaceStoreForTests();
  window.history.pushState({}, "", "/");
  vi.useRealTimers();
  vi.unstubAllGlobals();
  if (offsetHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  }
});

// --- Composer / SessionChrome placement ----------------------------------

test("mounts Composer with inline session controls and no standalone footer chrome", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  const composer = await screen.findByTestId("composer-slot");
  const footer = screen.getByTestId("pane-footer");
  expect(composer.textContent).toBe("ref_a");
  expect(within(composer).getByTestId("session-chrome-inline")).toBeTruthy();
  expect(within(footer).queryByTestId("session-chrome")).toBeNull();
});

test("mounts Composer even when the transcript is empty (no turns yet) - the composer is always available to send the first message", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a")); // testThread's default has no turns

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await screen.findByTestId("empty-state");
  expect(await screen.findByTestId("composer-slot")).toBeTruthy();
});

// A real evener session's transcript is never literally turns.length === 0:
// apptranscript.go's PreludeTurn (or, live, appprojector's bundled
// SESSION_START announcements) always synthesizes one turn - "turn_system" -
// from the session's (never-empty) system prompt, the moment thread/read
// returns. Before this, that made the "no turns yet" empty state above
// unreachable for any dormant session in practice (kata bz2z): a session
// that has never run a turn showed its transcript branch instead, with
// nothing in it to show but the collapsed system-prompt scaffold - not the
// invitation to send a first message. A transcript whose only turn is that
// synthetic prelude must count as empty the same way zero turns does.
test("treats a transcript whose only turn is the synthetic prelude (turn_system) as empty, not as content", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_system",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_system_prompt",
              turnId: "turn_system",
              type: "systemMessage",
              text: "You are evener, an agent...",
              status: "completed",
              eventKind: "system_prompt",
            },
          ],
        },
      ],
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await screen.findByTestId("empty-state");
  expect(screen.queryByTestId("turn-block")).toBeNull();
  expect(await screen.findByTestId("composer-slot")).toBeTruthy();
});

// The instant a real conversation exists alongside the prelude turn (the
// common, non-dormant shape: PreludeTurn's system prompt PLUS turn_1's
// actual exchange), the transcript is not empty and the prelude's own
// boilerplate stays visible right where it belongs - above the
// conversation, exactly as it always has for every session that has run.
test("does not treat the prelude turn as empty once a real turn exists alongside it", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_system",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_system_prompt",
              turnId: "turn_system",
              type: "systemMessage",
              text: "You are evener, an agent...",
              status: "completed",
              eventKind: "system_prompt",
            },
          ],
        },
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "hello", status: "completed" }],
        },
      ],
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  expect(await screen.findByText("hello")).toBeTruthy();
  expect(screen.queryByTestId("empty-state")).toBeNull();
});

// Overflow containment (2026-07-30-mobile-session-layout-design.md, decision
// 5): the transcript chain between PaneScaffold's clipped body and the
// virtual list must be able to shrink - a missing min-width: 0 on any flex
// link pins the whole column to its widest child.
test("the transcript flex chain carries min-width: 0", () => {
  const css = readModuleCss(import.meta.url, "session.module.css");
  for (const cls of ["transcriptContent", "transcriptList"]) {
    const rule = css.match(new RegExp(`\\.${cls} \\{([^}]*)\\}`));
    expect(rule, `session.module.css must define .${cls}`).not.toBeNull();
    expect(rule![1]).toContain("min-width: 0");
  }
});

// --- speaker geometry has exactly one declaration site: tokens.css --------
//
// TranscriptBody's shared .turn (transcript/turnblock.module.css) is also
// reused standalone by the preview and read-only surfaces - no pane-specific
// component class is an ancestor of every consumer, so the speaker geometry
// (--speaker-avatar-size/-gap/-gutter) lives in tokens.css and NEITHER
// stylesheet may redeclare any of it. This pins that contract from both sides.
test("speaker geometry is declared only in tokens.css, not in session or turnblock css", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const stripped = (path: string) => readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const sessionCss = stripped(join(here, "session.module.css"));
  const turnblockCss = stripped(join(here, "transcript", "turnblock.module.css"));
  for (const name of ["--speaker-avatar-size:", "--speaker-gap:", "--speaker-gutter:"]) {
    expect(sessionCss).not.toContain(name);
    expect(turnblockCss).not.toContain(name);
  }
  const tokensCss = stripped(join(here, "..", "..", "styles", "tokens.css"));
  expect(tokensCss.match(/--speaker-gap:\s*10px;/g) ?? []).toHaveLength(1);
  expect(tokensCss.match(/--speaker-gutter:\s*34px;/g) ?? []).toHaveLength(1);
});

// --- session-open lands at the transcript end (kata cmjb) ------------------
//
// A real evener session's transcript is never literally turns.length === 0 -
// apptranscript.go's PreludeTurn always synthesizes one turn from the
// session's system prompt before the first real turn exists (see
// transcriptVisibility.ts's own isDormantTranscript comment). A dormant
// session (composer visible, no real turn yet) that then gets its first
// real turn WHILE THE PANE STAYS MOUNTED is the realistic, common shape of
// "just spawned a session and it started replying" - and useTranscriptScroll's
// mount effect used to key its one-time "no saved position -> scroll to the
// end" initialization off turns.length > 0, which was ALREADY true from the
// prelude turn alone, before the real (VirtualList-backed) transcript had
// ever mounted. That transition then never re-triggered the effect (the
// dependency didn't change), so the mount positioning, the scroll listener,
// and stick-to-bottom never initialized at all for the rest of that pane's
// life - not just "didn't land at the end", but never followed anything
// again. This proves the fix by exercising the consequence that's actually
// observable in jsdom (no real scrollTop/scrollHeight - see
// useTranscriptScroll.ts's own comment on the injectable measure seam):
// stick-to-bottom reacting to a live turn that arrives right after the
// dormant -> real transition.
test("a dormant session's transcript follows new content the instant its first real turn arrives, wired through the real VirtualList", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    versionedReadResponse("ref_a", {
      status: { type: "active" },
      turns: [
        {
          id: "turn_system",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_system_prompt",
              turnId: "turn_system",
              type: "systemMessage",
              text: "You are evener, an agent...",
              status: "completed",
              eventKind: "system_prompt",
            },
          ],
        },
      ],
    }),
  );

  const { container } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  // The ordinary entry route publishes the model a task behind the first
  // paint; the live turn below must arrive against a real model/repository.
  await waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(true));
  await screen.findByTestId("empty-state");

  // The dormant session's first real turn - the transition that must
  // re-initialize useTranscriptScroll's mount effect.
  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "full",
          },
        ],
        items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "hello", status: "completed" }].map(
          (it) => ({ ...it, turnId: it.turnId ?? "turn_1" }),
        ),
      },
    } as AnyNotification);
  });
  await waitFor(() => expect(screen.getAllByTestId("turn-block").length).toBeGreaterThan(0));

  // Scroll away, then a third live turn arrives. If the mount effect never
  // (re)ran at the dormant -> real transition, initializedRef is stuck
  // false and NOTHING below reacts - not the scroll listener (never
  // attached), not the pill, nothing (every later effect in the hook bails
  // on !initializedRef.current). A pill that never appears is
  // indistinguishable, from the DOM alone, between "reader is caught up"
  // and "the follow machinery is dead" - which is exactly why this asserts
  // the pill DOES appear here, not that it stays absent.
  const root = scrollRootOf(container);
  stubScrolledAway(root);
  fireEvent.scroll(root);

  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [
          {
            id: "turn_2",
            status: "completed",
            itemsView: "full",
          },
        ],
        items: [{ id: "item_2", turnId: "turn_2", type: "userMessage", text: "second", status: "completed" }].map(
          (it) => ({ ...it, turnId: it.turnId ?? "turn_2" }),
        ),
      },
    } as AnyNotification);
  });

  const pill = await screen.findByTestId("new-content-pill");
  expect(pill.textContent).toContain("1");
});

// The pending-questions widget is a scrollable part of the transcript, not a
// footer-anchored composer surface: while a batch is pending it renders as
// the LAST row of the transcript's virtual list, so scrolling back to read
// context scrolls it away with the content. The composer keeps its own
// half of the contract (hiding its input row while a question is pending),
// proven in Composer.test.tsx; here the composer is the stubbed slot, which
// is exactly what lets this test pin "the dock is NOT the composer's child".
test("a pending ask_user batch renders as the transcript's last row, not inside the composer", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => versionedReadResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.queryByText(/loading/i)).toBeNull());

  // Same notification sequence AskDock.test.tsx's hydrateWithOneAsk drives:
  // a completed, unanswered ask_user call after the last user message is a
  // live pending question (deriveAskQuestions).
  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    });
    const item = {
      type: "commandExecution",
      id: "item_1",
      turnId: "turn_1",
      toolName: "ask_user",
      callId: "call_1",
      argumentsJson: JSON.stringify({
        questions: [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }],
      }),
    };
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [{ ...{ ...item, status: "inProgress" }, turnId: "turn_1" }],
      },
    });
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [{ ...{ ...item, status: "completed" }, turnId: "turn_1" }],
      },
    });
    fake.emitNotification(askPendingStatusChanged("ref_a"));
  });

  let dock: HTMLElement | null = null;
  await waitFor(() => {
    dock = document.querySelector("[data-ask-response-dock]");
    expect(dock).not.toBeNull();
  });

  // Inside the transcript's virtual list, as its LAST row...
  const list = screen.getByTestId("transcript-virtual-list");
  expect(list.contains(dock)).toBe(true);
  const rows = screen.getAllByTestId("transcript-row");
  expect(rows.at(-1)?.contains(dock)).toBe(true);

  // ...while its one aria-live region stays OUTSIDE the list, so a
  // virtualized remount of the row never re-announces unchanged text.
  const announcements = screen.getByTestId("ask-dock-announcements");
  expect(list.contains(announcements)).toBe(false);

  // ...and not inside the composer slot.
  expect(screen.getByTestId("composer-slot").contains(dock)).toBe(false);
});

// The dock row is a real virtual row, so every scroll coordinator that
// targets "the last row" - initial end positioning, jump-to-bottom, pill
// jumps - must count it. useTranscriptScroll receives the row count from
// this pane; with a pending ask that count must include the synthetic
// ask-dock row, or those targets land one row short (roborev PR #854).
test("a pending ask counts the dock row in the scroll coordinator's rendered row count", async () => {
  const realUseTranscriptScroll = useTranscriptScrollModule.useTranscriptScroll;
  const capturedCounts: Array<number | undefined> = [];
  const spy = vi
    .spyOn(useTranscriptScrollModule, "useTranscriptScroll")
    .mockImplementation((options: Parameters<typeof realUseTranscriptScroll>[0]) => {
      capturedCounts.push(options.renderedRowCount);
      return realUseTranscriptScroll(options);
    });
  try {
    const fake = connectReadyFakeClient();
    fake.on("thread/read", () => versionedReadResponse("ref_a"));

    render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => expect(screen.queryByText(/loading/i)).toBeNull());

    act(() => {
      fake.emitNotification({
        method: "history/updated",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
        },
      });
      const item = {
        type: "commandExecution",
        id: "item_1",
        turnId: "turn_1",
        toolName: "ask_user",
        callId: "call_1",
        argumentsJson: JSON.stringify({
          questions: [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }],
        }),
      };
      fake.emitNotification({
        method: "history/updated",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          items: [{ ...{ ...item, status: "inProgress" }, turnId: "turn_1" }],
        },
      });
      fake.emitNotification({
        method: "history/updated",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          items: [{ ...{ ...item, status: "completed" }, turnId: "turn_1" }],
        },
      });
      fake.emitNotification(askPendingStatusChanged("ref_a"));
    });

    // The dock row is on screen (placement contract), and the last options
    // the coordinator saw count it: one turn row + the synthetic dock row.
    await waitFor(() => expect(document.querySelector("[data-ask-response-dock]")).not.toBeNull());
    expect(capturedCounts.at(-1)).toBe(2);
  } finally {
    spy.mockRestore();
  }
});

// The steering-ghost spec's live-edge row: ONE trailing virtual row hosts
// both bottom-of-transcript tenants - the AskDock and the held-steer ghost
// stack - because a second synthetic row would double-count against every
// end-targeted scroll path. The tests below pin that row's composition, the
// derived count the scroll coordinator needs (a held steer without an ask
// used to land those paths one row short), the heldEpoch arrival signal the
// pill consumes, and the live-surface gate that keeps a notLoaded stub from
// showing a ghost.

// Most live-edge tests hydrate a thread carrying one real turn - the same
// readResponse-override fixture shape readOnlyEntityThread uses above. Fresh
// object per call, never shared across tests. The dormant-session regression
// below deliberately uses only the synthetic prelude instead.
function liveSurfaceThread(): Partial<Thread> {
  return {
    evener: {
      ref: "ref_a",
      mutationStateAuthoritative: true,
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
    },
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [{ id: "item_1", turnId: "turn_1", type: "agentMessage", text: "earlier reply", status: "completed" }],
      },
    ],
  };
}

test("a dormant live session keeps an idle drain in the QueueStrip", async () => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(stubSessionSlots);
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_system",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_system_prompt",
              turnId: "turn_system",
              type: "systemMessage",
              text: "System prompt",
            },
          ],
        },
      ],
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["parked"], preview: ["parked"] },
      },
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await act(async () => {
    await mutationStorage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/drainAsSteer",
      payload: { ref: "ref_a", input: [] },
      attachments: [],
      optimisticDisplay: { method: "turn/drainAsSteer", input: [] },
    });
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();

  await waitFor(() => expect(screen.getByTestId("queue-strip").dataset.queuedCount).toBe("2"));
  expect(screen.queryByTestId("held-steer-stack")).toBeNull();
  expect(document.querySelector('[data-row-id="live-edge"]')).toBeNull();
  expect(screen.getByTestId("held-steer-announcements")).toBeTruthy();
});

// Spec §1's live gate (roborev #2140): held ghosts never render on
// read-only surfaces - the same family the shared-notes surface keys on
// (humanNoteDrafts.ts). A hold parked on a restart-required session waits
// with the queue, not as a ghost promising delivery.
test("a restart-required session renders no held ghost for a seeded hold", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      status: { type: "restartRequired" },
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["parked"], preview: ["parked"] },
      },
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await act(async () => {
    await mutationStorage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/drainAsSteer",
      payload: { ref: "ref_a", input: [] },
      attachments: [],
      optimisticDisplay: { method: "turn/drainAsSteer", input: [] },
    });
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  await act(async () => {});

  expect(screen.queryByTestId("held-steer-stack")).toBeNull();
  expect(screen.queryByTestId("held-steer-announcements")).toBeNull();
  expect(document.querySelector('[data-row-id="live-edge"]')).toBeNull();
});

// Stop cancels a locally owned, never-attempted row. It is not a held ghost
// and must not produce a held outcome announcement.
test("Stop keeps a dormant session's unattempted row in local queue ownership", async () => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(stubSessionSlots);
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_system",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_system_prompt",
              turnId: "turn_system",
              type: "systemMessage",
              text: "System prompt",
            },
          ],
        },
      ],
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["parked"], preview: ["parked"] },
      },
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("queue-strip")).toBeTruthy());
  await act(async () => {
    await mutationStorage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/drainAsSteer",
      payload: { ref: "ref_a", input: [] },
      attachments: [],
      optimisticDisplay: { method: "turn/drainAsSteer", input: [] },
    });
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(screen.getByTestId("queue-strip").dataset.queuedCount).toBe("2"));

  // Stop cancels attempted work, but this never-attempted row stays locally
  // owned by QueueStrip.
  await act(async () => {
    await mutationStorage.cancelUnattempted("ref_a");
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();

  // Stop keeps the unattempted row under local queue ownership. It was never
  // a held ghost, so there is no delivery/failure announcement to preserve.
  await waitFor(() => expect(screen.getByTestId("queue-strip").dataset.queuedCount).toBe("2"));
  expect(screen.getByTestId("held-steer-announcements").dataset.outcomes).toBeUndefined();
  expect(screen.queryByTestId("held-steer-stack")).toBeNull();
});

test("a dormant session announces held delivery after transcript reflection", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
      },
      turns: [
        {
          id: "turn_system",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_system_prompt", turnId: "turn_system", type: "systemMessage", text: "System prompt" }],
        },
      ],
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("composer-slot")).toBeTruthy());
  const id = await act(async () => seedHeld("steer", "focus on the parser", { ref: "ref_a", attempted: true }));
  await waitFor(() => expect(screen.getByTestId("held-steer-stack")).toBeTruthy());

  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
      },
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [
            { id: "item_1", turnId: "turn_1", type: "userMessage", text: "focus on the parser", clientMutationId: id },
          ],
        },
      ],
    }),
  );
  await act(async () => {
    await threadsStore.getState().refreshThread("ref_a");
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();

  await waitFor(() => expect(screen.getByTestId("held-steer-announcements").dataset.outcomes).toBe("delivered"));
  expect(screen.queryByTestId("held-steer-stack")).toBeNull();
  // This delivery adds a real user turn; it does not exercise disappearance
  // of the entire transcript subtree after the final ghost leaves.
});

test("a dormant session keeps the held outcome region through final rejected ghost departure", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
      },
      turns: [
        {
          id: "turn_system",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_system_prompt", turnId: "turn_system", type: "systemMessage", text: "System prompt" }],
        },
      ],
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("empty-state").getAttribute("data-reason")).toBe("no-turns"));
  expect(screen.queryByTestId("transcript-virtual-list")).toBeNull();
  const announcements = screen.getByTestId("held-steer-announcements");

  const id = await act(async () => seedHeld("steer", "focus on the parser", { ref: "ref_a", attempted: true }));
  await waitFor(() => expect(screen.getByTestId("held-steer-stack")).toBeTruthy());
  expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy();
  expect(screen.queryByTestId("empty-state")).toBeNull();
  expect(screen.getByTestId("held-steer-announcements")).toBe(announcements);

  await act(async () => {
    await mutationStorage.transferToRecovery(id, "rejected");
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();

  await waitFor(() => expect(screen.queryByTestId("held-steer-stack")).toBeNull());
  expect(screen.queryByTestId("transcript-virtual-list")).toBeNull();
  expect(screen.getByTestId("empty-state").getAttribute("data-reason")).toBe("no-turns");
  expect(screen.getByTestId("held-steer-announcements")).toBe(announcements);
  expect(announcements.dataset.outcomes).toBe("rejected");
});

// The live gate's two fence marks (roborev #2140 round 4): a session whose
// status still reads active/idle can be read-only - a snapshot marked
// resumeRequired, or a restart-blocking recovery obligation. No ghost, no
// announcements, no live-edge row while either fence stands.
test("a resume-required live session renders no held ghost", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["parked"], preview: ["parked"] },
        resumeRequired: true,
      },
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("composer-slot")).toBeTruthy());
  await act(async () => {
    await mutationStorage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/drainAsSteer",
      payload: { ref: "ref_a", input: [] },
      attachments: [],
      optimisticDisplay: { method: "turn/drainAsSteer", input: [] },
    });
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  await act(async () => {});

  expect(screen.queryByTestId("held-steer-stack")).toBeNull();
  expect(screen.queryByTestId("held-steer-announcements")).toBeNull();
  expect(document.querySelector('[data-row-id="live-edge"]')).toBeNull();
});

test("a recovery-fenced live session renders no held ghost", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["parked"], preview: ["parked"] },
      },
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("composer-slot")).toBeTruthy());
  // The recovery fence's obligation (the same mark liveControls' press-time
  // rule consults) - armed here the way CommandPalette.test arms it.
  act(() => {
    threadsStore.setState((state) => ({
      restartBlockingObligations: new Map(state.restartBlockingObligations).set("ref_a", Symbol()),
    }));
  });
  await act(async () => {
    await mutationStorage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/drainAsSteer",
      payload: { ref: "ref_a", input: [] },
      attachments: [],
      optimisticDisplay: { method: "turn/drainAsSteer", input: [] },
    });
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  await act(async () => {});

  expect(screen.queryByTestId("held-steer-stack")).toBeNull();
  expect(screen.queryByTestId("held-steer-announcements")).toBeNull();
  expect(document.querySelector('[data-row-id="live-edge"]')).toBeNull();
});

test("a held steer renders as the live-edge trailing row, under the AskDock when both exist", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => versionedReadResponse("ref_a", liveSurfaceThread()));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await act(async () => {
    await seedHeld("steer", "focus on the parser", { ref: "ref_a", attempted: true });
  });
  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    expect(row!.querySelector("[data-testid='held-steer-stack']")).not.toBeNull();
  });

  // With the ask dock pending too, both live in the ONE trailing row, dock
  // first. Same ask_user drive the ask-dock trailing-row test above uses (a
  // completed, unanswered ask_user call is a live pending question -
  // deriveAskQuestions), on its own turn so it never rewrites the hydrated one.
  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_2", status: "inProgress", itemsView: "" }],
      },
    });
    const item = {
      type: "commandExecution",
      id: "item_2",
      turnId: "turn_2",
      toolName: "ask_user",
      callId: "call_2",
      argumentsJson: JSON.stringify({
        questions: [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }],
      }),
    };
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [{ ...{ ...item, status: "inProgress" }, turnId: "turn_2" }],
      },
    });
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [{ ...{ ...item, status: "completed" }, turnId: "turn_2" }],
      },
    });
    fake.emitNotification(askPendingStatusChanged("ref_a"));
  });
  await waitFor(() => {
    const stack = document.querySelector("[data-testid='held-steer-stack']");
    expect(stack).not.toBeNull();
    const dock = document.querySelector("[data-ask-response-dock]");
    expect(dock).not.toBeNull();
    // DOM order: the dock precedes the stack inside the same row.
    expect(dock!.compareDocumentPosition(stack!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});

test("no trailing row renders when neither an ask nor held steering exists", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy());
  // Scoped to the trailing row's id: ordinary turn rows carry their own
  // data-row-id values and must keep rendering.
  expect(document.querySelector('[data-row-id="live-edge"]')).toBeNull();
});

test("a held steer without an ask still counts in renderedRowCount (the one-row-short regression)", async () => {
  const realUseTranscriptScroll = useTranscriptScrollModule.useTranscriptScroll;
  const captured: Array<{ renderedRowCount?: number; heldEpoch?: number }> = [];
  const spy = vi
    .spyOn(useTranscriptScrollModule, "useTranscriptScroll")
    .mockImplementation((options: Parameters<typeof realUseTranscriptScroll>[0]) => {
      captured.push({ renderedRowCount: options.renderedRowCount, heldEpoch: options.heldEpoch });
      return realUseTranscriptScroll(options);
    });
  try {
    const fake = connectReadyFakeClient();
    fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));

    render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => {
      expect(captured.length).toBeGreaterThan(0);
      expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy();
    });
    const beforeSeed = captured.at(-1)?.renderedRowCount;
    await act(async () => {
      await seedHeld("steer", "focus on the parser", { ref: "ref_a", attempted: true });
    });
    await waitFor(() => expect(screen.getByTestId("held-steer-stack")).toBeTruthy());
    const afterSeed = captured.at(-1)?.renderedRowCount;
    expect(afterSeed).toBe((beforeSeed ?? 0) + 1);
  } finally {
    spy.mockRestore();
  }
});

test("heldEpoch bumps on arrival only - never on removal", async () => {
  // Same spy shape as above, capturing options.heldEpoch instead: this is
  // useHeldSteerEpoch's one end-to-end pin - the epoch Session feeds the
  // scroll coordinator, observed through the options the coordinator
  // actually received.
  const realUseTranscriptScroll = useTranscriptScrollModule.useTranscriptScroll;
  const epochs: number[] = [];
  const spy = vi
    .spyOn(useTranscriptScrollModule, "useTranscriptScroll")
    .mockImplementation((options: Parameters<typeof realUseTranscriptScroll>[0]) => {
      epochs.push(options.heldEpoch ?? 0);
      return realUseTranscriptScroll(options);
    });
  try {
    const fake = connectReadyFakeClient();
    fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));
    // The hydrated session dispatches the seeded steer at once, and the steer
    // stays held while the daemon has not answered: the test holds that
    // answer, then gives it to make the departure.
    const steerSent = deferred<() => void>();
    fake.on("turn/steer", (params) => {
      return new Promise((resolve) => {
        steerSent.resolve(() =>
          resolve({
            receipt: appliedReceipt(params.clientMutationId, "thr_ref_a"),
          }),
        );
      });
    });

    render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await act(async () => {
      await seedHeld("steer", "focus on the parser", { ref: "ref_a", attempted: true });
    });
    await waitFor(() => expect(screen.getByTestId("held-steer-stack")).toBeTruthy());
    expect(Math.max(...epochs)).toBe(1); // arrival bumped it exactly once
    // A departure: the daemon acknowledges the steer, and settling that
    // receipt removes the row.
    const answerSteer = await act(() => steerSent.promise);
    await act(async () => {
      answerSteer();
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(screen.queryByTestId("held-steer-stack")).toBeNull());
    expect(Math.max(...epochs)).toBe(1); // removal never bumps the epoch
    // ...and never RESETS it either: the last observed value is still the
    // arrival's 1. Math.max alone would let a reset-to-0 on removal slip
    // through (0 is no new maximum), so the final observation is pinned
    // directly.
    expect(epochs.at(-1)).toBe(1);
  } finally {
    spy.mockRestore();
  }
});

test("held steering renders only on a live surface: a notLoaded session shows no ghost", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { ...liveSurfaceThread(), status: { type: "notLoaded" } }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  // Wait for hydration first: with the model still pending there is no row
  // for a different reason, and this test must fail on the status gate alone.
  await waitFor(() => expect(screen.getByText("earlier reply")).toBeTruthy());
  await act(async () => {
    await seedHeld("steer", "focus on the parser", { ref: "ref_a", attempted: true });
  });
  await flushPendingTurnsProjectionForTests();
  expect(document.querySelector("[data-row-id='live-edge']")).toBeNull();
});

// The held-steer announcements region follows the ask dock's one mounting rule
// (the pending-ask test above pins the same for its own region): the ghost
// stack itself is a virtualized trailing row - a scroll-away unmounts it - so
// its ONE aria-live region must live OUTSIDE the virtual list, or every
// scroll remount would re-announce unchanged text (the AskDockAnnouncements
// pattern; steering-ghost spec §2).
test("the held-steer announcements region lives outside the virtual list", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await act(async () => {
    await seedHeld("steer", "focus on the parser", { ref: "ref_a", attempted: true });
  });
  await waitFor(() => expect(screen.getByTestId("held-steer-stack")).toBeTruthy());

  // The ghost stack itself IS inside the virtual list - it is the live-edge
  // trailing row, scrolling away with the content...
  const list = screen.getByTestId("transcript-virtual-list");
  expect(list.contains(screen.getByTestId("held-steer-stack"))).toBe(true);

  // ...while its one aria-live region stays OUTSIDE the list, so a
  // virtualized remount of the row never re-announces unchanged text.
  const announcements = screen.getByTestId("held-steer-announcements");
  expect(list.contains(announcements)).toBe(false);
});
