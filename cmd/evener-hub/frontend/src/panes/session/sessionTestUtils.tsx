import type { Thread, ThreadReadResponse } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act } from "@testing-library/react";
import type { vi } from "vitest";
import { navigationStore } from "../../stores/navigation/store";
import { threadsStore } from "../../stores/threads";
import { ALL_CAPABILITIES as CAPABILITIES } from "../../threadTestUtils";
import { requireClass } from "../../widgets/internal/requireClass";
import virtualListStyles from "../../widgets/virtuallist/virtuallist.module.css";
import "./index";
import "./testing/editorGeometry";

export function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

export function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

// Like readResponse, but stamped with the same live-history identity
// ("1"/epoch 1/"inc-1") this file's history/updated fixtures carry, so a
// thread/read a test pairs with a live history/updated notification hydrates
// straight into the versioned-history path instead of getting invalidated by
// a boot-generation mismatch (EMPTY_HISTORY's "" vs the frame's "1") the
// instant the first live frame lands.
export function versionedReadResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return {
    thread: testThread(ref, overrides),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 0 },
  };
}

// The pane's clock tests fake the now-tick's interval and Date, and nothing
// else. Faking every timer would also fake setImmediate and
// requestAnimationFrame, which fake-indexeddb and the transcript's virtualizer
// schedule on, and advanceTimersByTimeAsync yields one real macrotask per fake
// timer it fires: a 21s advance would take about 750 real turns (the storage
// steps of ten discovery scans and a scroll-reconcile frame loop), enough for a
// starved host to push the test past its timeout. With only these faked, the
// same advance fires about seventeen timers. setTimeout stays real as well:
// the storage's 10s transaction deadlines and the projection flush's 4s stall
// tripwire run on it, and a long fake advance would fire them while
// fake-indexeddb's work still runs in real time. The discovery scans the faked
// interval still starts run on real IndexedDB, so a test settles them with the
// projection flush after it advances, before the file's afterEach resets the
// pending-turns store they publish into.
export const FAKE_CLOCK_ONLY: Parameters<typeof vi.useFakeTimers>[0] = {
  toFake: ["setInterval", "clearInterval", "Date"],
};

export function setNavigationTitle(
  ref: string,
  title: string,
  topLevel = true,
  fields: Record<string, unknown> = {},
): void {
  const key = { kind: "location", ref } as const;
  const data = {
    generation_id: "generation_test",
    revision: 1,
    ref,
    top_level_ref: topLevel ? ref : "local:continuation",
    top_level: topLevel,
    session: {
      ref,
      host_id: "local",
      session_id: ref,
      title,
      project: "test-project",
      state: "idle",
      kind: topLevel ? "session" : "fork",
      live: true,
      children: [],
      ...fields,
    },
  };
  navigationStore.setState({
    mode: "v3",
    clientGenerationID: "generation_test",
    resources: new Map([
      [
        keyID(key),
        {
          key,
          data,
          loadedRevision: 1,
          targetRevision: null,
          forceToken: 0,
          etag: "etag",
          loading: false,
          stale: false,
          error: null,
          generationID: "generation_test",
        },
      ],
    ]),
  });
}

// R09: the resume the standalone Resume button used to run now runs from the
// store, driven by a send on the Send-resumes face. Tests that used to click
// the button drive the same sequence with this send. The parked row is a real
// outbox row the tests that inspect storage account for.
export async function resumeViaSend(ref: string, text = "resume and continue"): Promise<void> {
  await act(async () => {
    await threadsStore.getState().send(ref, text);
  });
}

// --- seen divider (kata g2ez) --------------------------------------------

export function turnFixture(id: string, text: string) {
  return {
    id,
    status: "completed" as const,
    itemsView: "full" as const,
    items: [{ id: `${id}-item`, turnId: id, type: "userMessage", text, status: "completed" as const }],
  };
}

// --- transcript/flow integration (wave 4 T4) -----------------------------
//
// useTranscriptScroll.test.ts proves the scroll-decision LOGIC exhaustively
// against a fully fake VirtualListHandle; none of that proves Session.tsx
// actually wires virtualListRef into the REAL VirtualList correctly (a
// wrong prop name, a ref that never reaches the widget, etc. would slip
// past every test in that file, and past every OTHER test in this file,
// which never touch scroll state at all). These two tests close that gap
// against the real component tree, using the same real-DOM property-stub
// technique virtuallist.test.tsx's own scrollToIndex test already
// establishes as this project's way to fake geometry jsdom won't compute.
export const ROOT_CLASS = requireClass(virtualListStyles.root, "virtuallist.module.css", "root");

export function scrollRootOf(container: HTMLElement): HTMLElement {
  return container.querySelector(`.${ROOT_CLASS}`) as HTMLElement;
}

export function stubScrolledAway(el: HTMLElement) {
  // scrollTop is writable (unlike scrollHeight/clientHeight): jumpToBottom
  // pins the true bottom by assigning it directly, and tests observe that.
  Object.defineProperty(el, "scrollTop", { configurable: true, writable: true, value: 0 });
  Object.defineProperty(el, "scrollHeight", { configurable: true, value: 5000 });
  Object.defineProperty(el, "clientHeight", { configurable: true, value: 500 });
}

// The hub pushes evener/thread/resync when its roster discovers the old daemon
// gone; the store re-hydrates on it, so the pane leaves the degraded state on
// its own with no button press. emitDaemonGoneResync is the one emission every
// daemon-exit test here drives; the read-held variant below keeps its own act
// because it holds the reconciliation read open inside it.
export const emitDaemonGoneResync = (client: FakeClient) =>
  act(async () => {
    client.emitNotification({ method: "evener/thread/resync", params: { ref: "ref_a", threadId: "thr_ref_a" } });
  });
