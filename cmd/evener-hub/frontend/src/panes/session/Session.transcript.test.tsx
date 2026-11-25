import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { AnyNotification, Thread, TurnStartResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { act, cleanup, fireEvent, type RenderResult, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { appliedReceipt } from "../../mutationReceiptTestUtils";
import { resetActivitySidebarStoreForTests } from "../../shell/activitybar/activitySidebarStore";
import { ClientProvider } from "../../shell/clientContext";
import { resetMobileViewportForTests } from "../../shell/useIsMobile";
import { resetWorkspaceStoreForTests } from "../../shell/workspace";
import { resetConnectionStoreForTests } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../stores/threads";
import { ALL_CAPABILITIES as CAPABILITIES } from "../../threadTestUtils";
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
import {
  readResponse,
  scrollRootOf,
  stubScrolledAway,
  stubSessionSlots,
  versionedReadResponse,
} from "./sessionTestUtils";
import * as useTranscriptScrollModule from "./transcript/flow/useTranscriptScroll";

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
    // the coordinator saw count it: one turn row + the synthetic dock row +
    // the end inset row (the floating-composer reserve, always last).
    await waitFor(() => expect(document.querySelector("[data-ask-response-dock]")).not.toBeNull());
    expect(capturedCounts.at(-1)).toBe(3);
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

  // The one-rendering rule: both undelivered messages render as transcript
  // rows in the live-edge region, and the retired dock renders nowhere.
  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    expect(row!.querySelectorAll('[data-testid="user-message-item"]')).toHaveLength(2);
  });
  expect(screen.queryByTestId("queue-strip")).toBeNull();
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

  // The durable families reach off-status surfaces (the retired footer
  // strip's own reach): the seeded queue row renders. But the seeded drain
  // hold must produce no ghost row - a pending-entry row, the family the
  // live gate hides on read-only surfaces - only its durable waiting row.
  const stack = screen.queryByTestId("pending-row-stack");
  expect(stack).not.toBeNull();
  expect(stack!.textContent).toContain("parked");
  expect(stack!.querySelectorAll("li[data-kind]")).toHaveLength(2);
  expect(stack!.querySelectorAll("li:not([data-kind])")).toHaveLength(0);
  expect(screen.queryByTestId("held-steer-announcements")).toBeNull();
});

// A Send that queued while the session needs recovery is a settled fact
// about the user's undelivered work: its waiting row (this client's own
// unattempted editable row) renders through the read-only state with its
// Edit and Remove controls, the retired footer strip's reach. Hiding it
// behind the live gate would leave the message reachable from nowhere if
// the resume stalls.
test("a resume-required session keeps its unattempted queued send's waiting row visible", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 0 },
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
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "queued while waiting to resume" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "queued while waiting to resume" }] },
    });
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  await act(async () => {});

  const stack = screen.queryByTestId("pending-row-stack");
  expect(stack).not.toBeNull();
  expect(stack!.textContent).toContain("queued while waiting to resume");
  expect(within(stack!).getByRole("button", { name: "Edit message" })).toBeTruthy();
  expect(within(stack!).getByRole("button", { name: "Remove from queue" })).toBeTruthy();
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
  // The daemon's queued row renders inline: exactly one transcript row for
  // the one undelivered message so far.
  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    expect(row!.querySelectorAll('[data-testid="user-message-item"]')).toHaveLength(1);
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
  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    expect(row!.querySelectorAll('[data-testid="user-message-item"]')).toHaveLength(2);
  });

  // Stop cancels attempted work, but this never-attempted row stays locally
  // owned by QueueStrip.
  await act(async () => {
    await mutationStorage.cancelUnattempted("ref_a");
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();

  // Stop keeps the unattempted row visible as its own transcript row - the
  // canceled family renders inline too. It was never a held ghost, so there
  // is no delivery/failure announcement to preserve.
  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    expect(row!.querySelectorAll('[data-testid="user-message-item"]')).toHaveLength(2);
  });
  expect(screen.getByTestId("held-steer-announcements").dataset.outcomes).toBeUndefined();
  expect(screen.queryByTestId("queue-strip")).toBeNull();
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
  await waitFor(() => expect(screen.getByTestId("pending-row-stack")).toBeTruthy());

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
  expect(screen.queryByTestId("pending-row-stack")).toBeNull();
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
  await waitFor(() => expect(screen.getByTestId("pending-row-stack")).toBeTruthy());
  expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy();
  expect(screen.queryByTestId("empty-state")).toBeNull();
  expect(screen.getByTestId("held-steer-announcements")).toBe(announcements);

  await act(async () => {
    await mutationStorage.transferToRecovery(id, "rejected");
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();

  // The one-rendering rule's departure shape: a rejected steer keeps a
  // visible refused row in the live-edge region (its recovery record now),
  // announced once. The transcript subtree stays mounted with it.
  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    const refused = row!.querySelector('[data-kind="refused"] [data-testid="user-message-item"]');
    expect(refused).not.toBeNull();
    expect(refused!.textContent).toContain("focus on the parser");
  });
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

  // Durable families reach off-status surfaces (the retired footer strip's
  // reach): the queue row and the hold's waiting row render, but the hold
  // produces no ghost - no pending-entry row, the family the live gate
  // hides.
  const stack = screen.queryByTestId("pending-row-stack");
  expect(stack).not.toBeNull();
  expect(stack!.textContent).toContain("parked");
  expect(stack!.querySelectorAll("li:not([data-kind])")).toHaveLength(0);
  expect(screen.queryByTestId("held-steer-announcements")).toBeNull();
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

  // Same rule as the resume-required case above: durable rows render, no
  // pending-entry ghost.
  const stack = screen.queryByTestId("pending-row-stack");
  expect(stack).not.toBeNull();
  expect(stack!.textContent).toContain("parked");
  expect(stack!.querySelectorAll("li:not([data-kind])")).toHaveLength(0);
  expect(screen.queryByTestId("held-steer-announcements")).toBeNull();
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
    expect(row!.querySelector("[data-testid='pending-row-stack']")).not.toBeNull();
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
    const stack = document.querySelector("[data-testid='pending-row-stack']");
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
    await waitFor(() => expect(screen.getByTestId("pending-row-stack")).toBeTruthy());
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
    await waitFor(() => expect(screen.getByTestId("pending-row-stack")).toBeTruthy());
    expect(Math.max(...epochs)).toBe(1); // arrival bumped it exactly once
    // A departure: the daemon acknowledges the steer, and settling that
    // receipt removes the row.
    const answerSteer = await act(() => steerSent.promise);
    await act(async () => {
      answerSteer();
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(screen.queryByTestId("pending-row-stack")).toBeNull());
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

// A queue row arriving from another client must bump the held epoch too
// (issue #6363): reconcilePendingEntries excludes identities the daemon's
// queue already reflects, so keying the epoch on this client's pending
// entries alone never fires for a foreign arrival and a scrolled-away
// reader gets no new-content pill. The epoch counts the visible pending
// MESSAGE set - the same rows the stack renders - so the arrival edge
// covers every family the pane can see.
test("heldEpoch bumps on a foreign queue arrival, not only this client's holds", async () => {
  // Same spy shape as the removal pin above: the epoch Session feeds the
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
    fake.on("thread/read", () => versionedReadResponse("ref_a", liveSurfaceThread()));
    render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("earlier reply")).toBeTruthy());
    const baseline = Math.max(...epochs);
    // A foreign arrival: another client (or the TUI) queues a message. No
    // pending entry of THIS client exists; the row arrives through the
    // daemon's queue alone.
    await act(async () => {
      fake.emitNotification({
        method: "thread/queueChanged",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          queue: {
            revision: 1,
            depth: 1,
            ids: ["q_foreign"],
            texts: ["queued elsewhere"],
            preview: ["queued elsewhere"],
          },
          consumedClientMutationIds: [],
        },
      });
    });
    // The row is visible in the pane's pending stack.
    await waitFor(() =>
      expect(screen.getByText("queued elsewhere", { selector: "[data-testid=user-bubble] *" })).toBeTruthy(),
    );
    expect(Math.max(...epochs)).toBe(baseline + 1); // the arrival bumped it
    // The row leaving (delivered or consumed elsewhere) never bumps again.
    await act(async () => {
      fake.emitNotification({
        method: "thread/queueChanged",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          queue: { revision: 2, depth: 0, ids: [] },
          consumedClientMutationIds: [],
        },
      });
    });
    await waitFor(() =>
      expect(screen.queryByText("queued elsewhere", { selector: "[data-testid=user-bubble] *" })).toBeNull(),
    );
    expect(Math.max(...epochs)).toBe(baseline + 1); // removal never bumps
  } finally {
    spy.mockRestore();
  }
});

// The per-pane recovery publications (#6364) exclude the union of every
// pane's live composition in the rendered stack. The epoch feeds on the
// same visible set (the invariant pinned in pendingMessageLiveEdge: one
// predicate for the mount gate, the stack's list, and the arrival edge, so
// the epoch and the rendered rows cannot disagree) - so pane B activating
// its OWN recovery must never read as an arrival in pane A: recovery-a
// stays excluded there, invisible to pane A's reader before and after
// pane B's activation. A pill that fired would name content the reader
// cannot see.
test("a sibling pane activating its own recovery never bumps this pane's arrival epoch", async () => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(stubSessionSlots);
  const realUseTranscriptScroll = useTranscriptScrollModule.useTranscriptScroll;
  // The scroll options' viewId is the retained read view's id when a view
  // exists, or the seat's fallback paneId before it does - both carry the
  // pane id (transcriptReadView.ts embeds it in the JSON id array). Bucket
  // every observation under the pane it belongs to.
  const epochsByPane = new Map<string, number[]>();
  const spy = vi
    .spyOn(useTranscriptScrollModule, "useTranscriptScroll")
    .mockImplementation((options: Parameters<typeof realUseTranscriptScroll>[0]) => {
      const paneOf = (viewId: string | undefined) => {
        const fallback = viewId && !viewId.startsWith("[") ? viewId : (JSON.parse(viewId ?? "null")?.[1] as string);
        return fallback ?? "unknown";
      };
      const bucket = epochsByPane.get(paneOf(options.viewId)) ?? [];
      bucket.push(options.heldEpoch ?? 0);
      epochsByPane.set(paneOf(options.viewId), bucket);
      return realUseTranscriptScroll(options);
    });
  try {
    const fake = connectReadyFakeClient();
    fake.on("thread/read", () => versionedReadResponse("ref_a", liveSurfaceThread()));
    const paneA = render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="pane_a" focused={true} />
      </ClientProvider>,
    );
    const paneB = render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="pane_b" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => {
      expect(within(paneA.container).getByText("earlier reply")).toBeTruthy();
      expect(within(paneB.container).getByText("earlier reply")).toBeTruthy();
    });
    // Two durable refused-recovery rows for the one ref - the two-composer
    // shape of #6364: each pane's composer will edit its own.
    await act(async () => {
      const storage = new MutationOutboxIndexedDB();
      const recoveryA = await storage.enqueueIntent({
        targetRef: "ref_a",
        threadId: "thr_ref_a",
        method: "turn/start",
        payload: { ref: "ref_a", input: [{ type: "text", text: "recovery from pane A" }] },
        attachments: [],
        optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "recovery from pane A" }] },
      });
      const recoveryB = await storage.enqueueIntent({
        targetRef: "ref_a",
        threadId: "thr_ref_a",
        method: "turn/start",
        payload: { ref: "ref_a", input: [{ type: "text", text: "recovery from pane B" }] },
        attachments: [],
        optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "recovery from pane B" }] },
      });
      await storage.transferToRecovery(recoveryA.clientMutationId, "rejected", "refused at the hub");
      await storage.transferToRecovery(recoveryB.clientMutationId, "rejected", "refused at the hub");
      storage.close();
      await refreshPendingTurnsProjection("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    // Both refused rows are visible in both panes' stacks.
    await waitFor(() => {
      expect(within(paneA.container).getAllByTestId("user-message-item").length).toBe(2);
      expect(within(paneB.container).getAllByTestId("user-message-item").length).toBe(2);
    });
    // Pane A edits its recovery; the row disappears from every pane's stack
    // (the union rule: any pane's live composition is excluded everywhere),
    // the same visible set the epoch counts.
    const user = userEvent.setup();
    const editInRow = (pane: RenderResult, text: string) => {
      const row = within(pane.container)
        .getAllByTestId("user-message-item")
        .find((item) => item.textContent?.includes(text));
      const refusedRow = row?.closest('li[data-kind="refused"]');
      if (!(refusedRow instanceof HTMLElement)) throw new Error(`no refused row for ${text}`);
      const button = within(refusedRow).getByRole("button", { name: "Edit message" });
      if (!button) throw new Error(`no Edit button in the ${text} row`);
      return button;
    };
    await user.click(editInRow(paneA, "recovery from pane A"));
    await waitFor(() => {
      expect(within(paneA.container).getAllByTestId("user-message-item").length).toBe(1);
    });
    const epochsA = epochsByPane.get("pane_a") ?? [];
    const baselineA = Math.max(...epochsA);
    // Pane B edits its own recovery. In pane A nothing visible changed:
    // recovery-a was already excluded, and the epoch counts the same set
    // the stack renders. The pre-fix joined-string aggregate matched
    // neither row (the epoch's filter saw BOTH rows as visible), so this
    // activation read as recovery-a reappearing - a spurious arrival.
    await user.click(editInRow(paneB, "recovery from pane B"));
    await waitFor(() => {
      expect(within(paneA.container).queryByText("recovery from pane B")).toBeNull();
    });
    const epochsALate = epochsByPane.get("pane_a") ?? [];
    expect(Math.max(...epochsALate)).toBe(baselineA); // no arrival in pane A
    // The rows' delivery (both edits end) never bumps either: removals are
    // not arrivals.
    expect(epochsALate.at(-1)).toBe(baselineA);
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
  await waitFor(() => expect(screen.getByTestId("pending-row-stack")).toBeTruthy());

  // The ghost stack itself IS inside the virtual list - it is the live-edge
  // trailing row, scrolling away with the content...
  const list = screen.getByTestId("transcript-virtual-list");
  expect(list.contains(screen.getByTestId("pending-row-stack"))).toBe(true);

  // ...while its one aria-live region stays OUTSIDE the list, so a
  // virtualized remount of the row never re-announces unchanged text.
  const announcements = screen.getByTestId("held-steer-announcements");
  expect(list.contains(announcements)).toBe(false);
});

// --- undelivered messages render as transcript rows --------------------------
//
// The one-rendering rule (Jesse's ruling, 2026-11-13): every user message
// that has not fully delivered to the session renders as ONE
// transcript-shaped row in the live-edge trailing region - same
// UserMessageView construction a delivered user message gets, actions on
// the row - never a dock below the transcript and never a dimmed chip
// beside the composer. The families below join the held-steer ghosts that
// already live there.

// A queued row from the daemon's authoritative queue renders as one
// transcript-shaped row inside the live-edge region, carrying its actions.
// The daemon queue is authoritative wire state, so this seeds through the
// hydrate, the same way liveSurfaceThread's queue-bearing variant does.
test("a queued message renders as a transcript row in the live-edge region, with its actions reachable", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      ...liveSurfaceThread(),
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: {
          revision: 1,
          depth: 1,
          ids: ["q1"],
          texts: ["parked behind the turn"],
          preview: ["parked behind the turn"],
        },
      },
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    // One transcript-shaped row: the same construction a delivered user
    // message gets, inside the trailing region.
    const message = row!.querySelector('[data-testid="user-message-item"]');
    expect(message).not.toBeNull();
    expect(message!.textContent).toContain("parked behind the turn");
    // The row's own actions travel with it into the transcript.
    expect(within(row as HTMLElement).getAllByRole("button").length).toBeGreaterThan(0);
  });
  // The dock is retired: no queue strip renders anywhere.
  expect(screen.queryByTestId("queue-strip")).toBeNull();
});

// An in-flight send (the "Sending..." family) renders as one transcript
// row too - the same one-rendering rule, not a chip beside the composer.
test("an in-flight send renders as a transcript row in the live-edge region, not a composer chip", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy());
  await act(async () => {
    await seedHeld("send", "hello there", { ref: "ref_a", attempted: true });
  });

  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    const message = row!.querySelector('[data-testid="user-message-item"]');
    expect(message).not.toBeNull();
    expect(message!.textContent).toContain("hello there");
  });
  // The chip dock is retired: no pending chips render anywhere.
  expect(screen.queryByTestId("pending-chips")).toBeNull();
});

// The failure families render in the same place: a Stop-canceled durable
// row is one transcript row with its Retry affordance, never a dock entry.
// Seeded through the same real durable write every Stop path makes
// (storage cancelUnattempted on an unattempted row - QueueStrip.test's
// seedCanceled shape).
test("a canceled-by-Stop message renders as a transcript row with its retry affordance", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy());
  await act(async () => {
    await mutationStorage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "stopped mid-flight" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "stopped mid-flight" }] },
    });
    await mutationStorage.cancelUnattempted("ref_a");
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();

  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    const message = row!.querySelector('[data-testid="user-message-item"]');
    expect(message).not.toBeNull();
    expect(message!.textContent).toContain("stopped mid-flight");
  });
  expect(screen.queryByTestId("queue-strip")).toBeNull();
});

// A delivery-uncertain (blocked) row renders as one transcript row as
// well - the failure label rides the row's provisional caption, not a
// dock slot.
test("a delivery-uncertain message renders as a transcript row in the live-edge region", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy());
  await act(async () => {
    const storage = new MutationOutboxIndexedDB();
    const outbox = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "uncertain delivery" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "uncertain delivery" }] },
    });
    await storage.markUnknown(outbox.clientMutationId, "blockedUnknown");
    storage.close();
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();

  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    const message = row!.querySelector('[data-testid="user-message-item"]');
    expect(message).not.toBeNull();
    expect(message!.textContent).toContain("uncertain delivery");
  });
  expect(screen.queryByTestId("queue-strip")).toBeNull();
});

// One rendering per message: the same pending id never produces two
// user-message rows, and no family renders outside the trailing region.
test("a pending send renders exactly one user message row and nothing outside the live-edge region", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy());
  await act(async () => {
    await seedHeld("send", "only one of me", { ref: "ref_a", attempted: true });
  });

  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    expect(row!.querySelectorAll('[data-testid="user-message-item"]')).toHaveLength(1);
  });
  // The transcript's delivered rows (none here) and the trailing region
  // together carry every user-message-item; nothing else may.
  expect(document.querySelectorAll('[data-testid="user-message-item"]').length).toBe(1);
});

// The trailing region is pinned BELOW the server's rows: the live-edge
// row is the last row of the virtual list, so a queued message always
// renders after everything that came from the server.
test("the live-edge region with a queued row renders below the server's transcript rows", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      ...liveSurfaceThread(),
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["queued below"], preview: ["queued below"] },
      },
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => {
    const liveEdge = document.querySelector('[data-row-id="live-edge"]');
    expect(liveEdge).not.toBeNull();
    const serverTurn = document.querySelector('[data-testid="turn-block"]');
    expect(serverTurn).not.toBeNull();
    // DOM order: the server's turn block precedes the live-edge region.
    expect(serverTurn!.compareDocumentPosition(liveEdge!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});

// The count the scroll coordinator reads (renderedRowCount) includes a
// queued row too, not only the held-steer and ask families - the same
// one-row-short regression the ghost test above pins for steers.
test("a queued message without an ask counts in renderedRowCount (the one-row-short regression)", async () => {
  const realUseTranscriptScroll = useTranscriptScrollModule.useTranscriptScroll;
  const captured: Array<{ renderedRowCount?: number }> = [];
  const spy = vi
    .spyOn(useTranscriptScrollModule, "useTranscriptScroll")
    .mockImplementation((options: Parameters<typeof realUseTranscriptScroll>[0]) => {
      captured.push({ renderedRowCount: options.renderedRowCount });
      return realUseTranscriptScroll(options);
    });
  try {
    const fake = connectReadyFakeClient();
    fake.on("thread/read", () =>
      readResponse("ref_a", {
        ...liveSurfaceThread(),
        evener: {
          ref: "ref_a",
          mutationStateAuthoritative: true,
          capabilities: CAPABILITIES,
          queue: { revision: 0 },
        },
      }),
    );

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
    // An optimistic queue entry - the same durable path a real queue press
    // takes - joins the trailing region.
    await act(async () => {
      await seedHeld("queue", "queued prose", { ref: "ref_a", attempted: true });
    });
    await waitFor(() => expect(document.querySelector('[data-row-id="live-edge"]')).not.toBeNull());
    const afterSeed = captured.at(-1)?.renderedRowCount;
    expect(afterSeed).toBe((beforeSeed ?? 0) + 1);
  } finally {
    spy.mockRestore();
  }
});

// The pill's arrival signal covers the queue/send families too: a queued
// row appearing while the reader is scrolled away fires the new-content
// pill, exactly as a held steer's heldEpoch does.
test("a queued row appearing while scrolled away fires the new-content pill", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));

  const { container } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy());
  const root = scrollRootOf(container);
  stubScrolledAway(root);
  fireEvent.scroll(root);

  await act(async () => {
    await seedHeld("queue", "arrived while away", { ref: "ref_a", attempted: true });
  });

  const pill = await screen.findByTestId("new-content-pill");
  expect(pill.textContent).toContain("1");
});

// A refused-recovery row (the durable record a rejected submission
// becomes) renders as one transcript row too - its Edit affordance rides
// the row, never a dock slot below the transcript.
test("a refused-recovery message renders as a transcript row in the live-edge region", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy());
  await act(async () => {
    const storage = new MutationOutboxIndexedDB();
    const outbox = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "refused at the hub" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "refused at the hub" }] },
    });
    await storage.transferToRecovery(outbox.clientMutationId, "rejected", "steer is not available");
    storage.close();
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();

  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    const message = row!.querySelector('[data-testid="user-message-item"]');
    expect(message).not.toBeNull();
    expect(message!.textContent).toContain("refused at the hub");
  });
  expect(screen.queryByTestId("queue-strip")).toBeNull();
});

// A refused-recovery row's Edit hands the record to the composer through
// the restore seam; the outbox-record branch of the same seam focuses the
// editor after its activation (the retired strip's own contract), but the
// recovery-record branch restored content without focusing, leaving the
// restored draft unreachable to the keyboard until the user clicked the
// composer. The press must end with the editor focused.
test("a refused-recovery row's Edit focuses the composer's editor", async () => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(stubSessionSlots);
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy());
  await act(async () => {
    const storage = new MutationOutboxIndexedDB();
    const outbox = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "refused at the hub" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "refused at the hub" }] },
    });
    await storage.transferToRecovery(outbox.clientMutationId, "rejected", "steer is not available");
    storage.close();
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();

  const row = await waitFor(() => {
    const liveEdge = document.querySelector('[data-row-id="live-edge"]');
    expect(liveEdge).not.toBeNull();
    return liveEdge as HTMLElement;
  });
  const user = userEvent.setup();
  await user.click(within(row).getByRole("button", { name: "Edit message" }));
  await waitFor(() => {
    const editor = document.querySelector('[role="textbox"][aria-label="Message"]');
    expect(editor).not.toBeNull();
    expect(document.activeElement).toBe(editor);
  });
});

// Retry's own busy state is transient (the press's in-flight window), unlike
// the blocked-target refusal, which is a durable verdict. A native disabled
// drops keyboard focus the moment the label flips to "Retrying…", so a
// keyboard-triggered retry that fails strands the reader's focus on <body>
// with no control to re-press. The busy state must refuse through
// aria-disabled - focusable, click swallowed by the widget's own refusal -
// while the blocked verdict keeps its native disabled (a blocked target's
// Retry can never act until the hub's answer changes).
test("a busy retry keeps its keyboard focus through the in-flight window", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      ...liveSurfaceThread(),
      status: { type: "active" },
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
      },
    }),
  );
  // Hold the retry's RPC answer open, so the busy window is observable: the
  // deferred is never resolved - the test observes the in-flight state, not
  // its outcome.
  const retryAnswered = deferred<void>();
  fake.on("turn/start", () => retryAnswered.promise as unknown as TurnStartResponse);

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy());
  await act(async () => {
    const storage = new MutationOutboxIndexedDB();
    const outbox = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "uncertain delivery" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "uncertain delivery" }] },
    });
    await storage.markUnknown(outbox.clientMutationId, "blockedUnknown");
    storage.close();
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();

  const retry = await waitFor(() => screen.getByRole("button", { name: "Retry" }));
  retry.focus();
  expect(document.activeElement).toBe(retry);
  const user = userEvent.setup();
  await user.keyboard("{Enter}");
  await waitFor(() => expect(screen.getByRole("button", { name: "Retrying…" })).toBeTruthy());
  const busy = screen.getByRole("button", { name: "Retrying…" });
  // The busy control refuses through aria-disabled, never the native
  // attribute: focus never left it.
  expect(busy.getAttribute("aria-disabled")).toBe("true");
  expect(busy.hasAttribute("disabled")).toBe(false);
  expect(document.activeElement).toBe(busy);
});

// Keep the pane mounted while the queue source, consuming command and
// recorded steering item hand the message to each other.
test.each(["local", "local-read", "remote", "remote-read", "remote-reconnect"])(
  "a queued row stays visible once through %s promotion and recording",
  async (origin) => {
    const fake = connectReadyFakeClient();
    const text = "promotion lifecycle sentinel";
    const local = origin === "local" || origin === "local-read";
    const sourceId = local ? await seedHeld("queue", text, { attempted: true }) : "remote_source";
    const history: Thread["turns"] = [
      {
        id: "earlier_turn",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "earlier_input",
            turnId: "earlier_turn",
            type: "userMessage",
            text: "earlier request",
            clientMutationId: "earlier_mutation",
            status: "completed",
          },
        ],
      },
    ];
    let snapshot = readResponse("ref_a", {
      turns: history,
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q1"], clientMutationIds: [sourceId], texts: [text], preview: [text] },
      },
    });
    fake.on("thread/read", () => snapshot);
    const promoted = deferred<string>();
    const answerPromotion = deferred<void>();
    fake.on("turn/promoteQueuedAsSteer", async (params: { clientMutationId: string; expectedEntryId: string }) => {
      expect(params.expectedEntryId).toBe("q1");
      promoted.resolve(params.clientMutationId);
      await answerPromotion.promise;
      return {
        receipt: appliedReceipt(params.clientMutationId, "thr_ref_a", {
          projectionState: "pending",
          queueEntryIds: ["q1"],
          consumedClientMutationIds: [sourceId],
        }),
      };
    });

    const { container, rerender } = render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );

    const messageRows = () =>
      screen.queryAllByTestId("user-message-item").filter((row) => row.textContent?.includes(text));
    await waitFor(() => expect(messageRows()).toHaveLength(1));
    const observations: number[] = [];
    const observer = new MutationObserver(() => observations.push(messageRows().length));
    observer.observe(container, { childList: true, subtree: true, characterData: true });
    onTestFinished(() => observer.disconnect());
    const originalRow = messageRows()[0];
    let promoteId = "remote_promote";
    if (local) {
      const user = userEvent.setup();
      await user.click(within(originalRow!).getByRole("button", { name: "Steer now" }));
      promoteId = await act(() => promoted.promise);
    }
    expect(promoteId).not.toBe(sourceId);
    await flushPendingTurnsProjectionForTests();
    expect(messageRows()).toHaveLength(1);
    expect(messageRows()[0]).toBe(originalRow);

    snapshot = readResponse("ref_a", {
      turns: history,
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: { revision: 2, depth: 0, ids: [] },
        pendingMutations: [
          {
            clientMutationId: promoteId,
            method: "turn/promoteQueuedAsSteer",
            input: [{ type: "text", text }],
            executionState: "claimed",
            projectionState: "pending",
            queueEntryIds: ["q1"],
          },
        ],
      },
    });
    await act(async () => {
      if (origin !== "remote-read" && origin !== "local-read") {
        fake.emitNotification({
          method: "thread/queueChanged",
          params: {
            threadId: "thr_ref_a",
            ref: "ref_a",
            queue: { revision: 2, depth: 0, ids: [] },
            consumedClientMutationIds: [sourceId],
          },
        });
      }
      answerPromotion.resolve();
      await threadsStore.getState().refreshThread("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    expect(messageRows()).toHaveLength(1);
    expect(messageRows()[0]).toBe(originalRow);

    if (origin === "remote-reconnect") {
      await act(async () => {
        const replacement = connectReadyFakeClient((client) => client.on("thread/read", () => snapshot));
        rerender(
          <ClientProvider client={replacement}>
            <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
          </ClientProvider>,
        );
        await threadsStore.getState().refreshThread("ref_a");
      });
      await flushPendingTurnsProjectionForTests();
      expect(messageRows()).toHaveLength(1);
      expect(messageRows()[0]).toBe(originalRow);
    }

    snapshot = readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: { revision: 2, depth: 0, ids: [] },
        pendingMutations: [],
      },
      turns: [
        ...history,
        {
          id: "turn_promoted",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_promoted",
              turnId: "turn_promoted",
              type: "steering",
              source: "user",
              text,
              clientMutationId: promoteId,
              conversationInputIds: [`client:${sourceId}`],
              status: "completed",
            },
          ],
        },
      ],
    });
    await act(async () => {
      await threadsStore.getState().refreshThread("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => {
      expect(messageRows()).toHaveLength(1);
      expect(messageRows()[0]?.getAttribute("data-provisional")).not.toBe("true");
    });
    expect(observations.length).toBeGreaterThan(0);
    expect(observations.every((count) => count === 1)).toBe(true);
  },
);

// The row's steer action carries the entry's own selections through the
// promote (the retired strip passed them; dropping them steers a
// selection-bearing message as bare text) and never promotes the daemon's
// generic "[N skills]" placeholder as the message.
test("a queued row's steer action preserves the entry's selections and never its placeholder preview", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      ...liveSurfaceThread(),
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: {
          revision: 1,
          depth: 2,
          ids: ["q1", "q2"],
          texts: ["promote me", ""],
          preview: ["promote me", "[1 skills]"],
          skillNames: [["review"], ["review"]],
        },
      },
    }),
  );
  const promoted = deferred<void>();
  fake.on("turn/promoteQueuedAsSteer", () => {
    promoted.resolve();
    return { receipt: appliedReceipt("m_promote", "thr_ref_a") };
  });

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  const row = await waitFor(() => {
    const liveEdge = document.querySelector('[data-row-id="live-edge"]');
    expect(liveEdge).not.toBeNull();
    return liveEdge as HTMLElement;
  });
  const user = userEvent.setup();
  const rows = within(row).getAllByRole("listitem");
  const first = rows.find((candidate) => candidate.textContent?.includes("promote me"));
  expect(first).toBeDefined();
  await user.click(within(first as HTMLElement).getByRole("button", { name: "Steer now" }));
  await act(async () => {
    await promoted.promise;
  });
  // The promote's optimistic display input keeps the entry's own skill
  // selections (the ghost row previews the message it will become) - the
  // store packs them from the display payload the row's press passes.
  const storage = new MutationOutboxIndexedDB();
  const outbox = await storage.listOutbox("ref_a");
  storage.close();
  const promoteRecord = outbox.find((record) => record.method === "turn/promoteQueuedAsSteer");
  const display = promoteRecord?.optimisticDisplay as { input: unknown[] } | undefined;
  expect(display?.input).toEqual(
    expect.arrayContaining([
      { type: "text", text: "promote me" },
      { type: "skill", name: "review" },
    ]),
  );
});
