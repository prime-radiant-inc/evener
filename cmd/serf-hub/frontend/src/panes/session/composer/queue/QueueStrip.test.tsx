import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { ConnectionState } from "../../../../protocol/client";
import { WireError } from "../../../../protocol/errors";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import { Toast } from "../../../../widgets";
import { resetPendingTurnsStoreForTests, submitWithPendingTracking } from "./pendingTurnsStore";
import { QueueStrip } from "./QueueStrip";

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "active" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref, capabilities: CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
    turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

// This project has no jest-dom matcher setup (vite.config.ts's own
// `test.setupFiles: []`) - every other test file in the tree checks a
// button's disabled state via the plain DOM property directly (e.g.
// widgets/button/button.test.tsx, sandboxEscalation.test.tsx), not a
// `toBeDisabled()` matcher; this helper matches that established
// convention.
function isDisabled(el: HTMLElement): boolean {
  return (el as HTMLButtonElement).disabled;
}

function connectFakeClient(state: ConnectionState = "ready"): FakeClient {
  const fake = new FakeClient(state);
  connectionStore.getState().connect(fake);
  return fake;
}

async function hydrate(fake: FakeClient, ref: string, overrides: Partial<Thread> = {}): Promise<void> {
  fake.on("thread/read", () => readResponse(ref, overrides));
  await threadsStore.getState().ensureThread(ref);
}

function defaultProps(overrides: Partial<Parameters<typeof QueueStrip>[0]> = {}) {
  return {
    ref: "ref_a",
    getComposerText: () => ({ text: "composer text", attachments: undefined, hasPending: false }),
    onRestoreToComposer: vi.fn(),
    onDrainSuccess: vi.fn(),
    busy: false,
    onDrainBusyChange: vi.fn(),
    ...overrides,
  };
}

function renderStrip(props: ReturnType<typeof defaultProps>) {
  return render(
    <>
      <QueueStrip {...props} />
      <Toast />
    </>,
  );
}

// DrainBusyHarness owns busy/onDrainBusyChange as REAL controlled state
// (mirroring Composer.tsx's own busyAction/setBusyAction round-trip) - a
// static `busy: false` from defaultProps() (every other test's own default)
// can't observe QueueStrip's own self-disabling behavior, since nothing
// would ever flip it back to true when handleDrain calls onDrainBusyChange.
function DrainBusyHarness(overrides: Partial<Parameters<typeof QueueStrip>[0]> = {}) {
  const [busy, setBusy] = useState(false);
  return (
    <>
      <QueueStrip {...defaultProps({ ...overrides, busy, onDrainBusyChange: setBusy })} />
      <Toast />
    </>
  );
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("visibility", () => {
  // Queries for the "Queued messages" heading specifically, not a bare
  // `section` selector - <Toast/> (rendered alongside the strip in every
  // test via renderStrip) also mounts its own <section>, which a generic
  // selector would false-positive against regardless of QueueStrip's own
  // visibility.
  test("renders nothing before the thread has hydrated", () => {
    renderStrip(defaultProps());
    expect(screen.queryByText(/queued messages/i)).toBeNull();
  });

  test("renders nothing when the queue is empty and no pending entries exist", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", { serf: { ref: "ref_a", capabilities: CAPABILITIES, queue: { depth: 0 } } });
    renderStrip(defaultProps());
    expect(screen.queryByText(/queued messages/i)).toBeNull();
  });

  test("renders the strip once the queue has entries", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] },
      },
    });
    renderStrip(defaultProps());
    expect(await screen.findByText(/queued messages/i)).toBeTruthy();
  });
});

describe("row rendering", () => {
  async function hydrateWithTwoRows(fake: FakeClient) {
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: {
          depth: 2,
          ids: ["q1", "q2"],
          texts: ["first queued message", "second queued message"],
          preview: ["first queued message", "second queued message"],
        },
      },
    });
  }

  test("renders one row per queue entry, with its preview text", async () => {
    const fake = connectFakeClient();
    await hydrateWithTwoRows(fake);
    renderStrip(defaultProps());

    const rows = await screen.findAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]!).getByText("first queued message")).toBeTruthy();
    expect(within(rows[1]!).getByText("second queued message")).toBeTruthy();
  });

  test("truncates a preview row over 140 chars with a trailing ellipsis", async () => {
    const fake = connectFakeClient();
    const long = "x".repeat(150);
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: [long], preview: [long] },
      },
    });
    renderStrip(defaultProps());

    const rows = await screen.findAllByRole("listitem");
    expect(within(rows[0]!).getByText(`${"x".repeat(140)}…`)).toBeTruthy();
  });

  test("each row exposes steer-now, edit, and remove actions", async () => {
    const fake = connectFakeClient();
    await hydrateWithTwoRows(fake);
    renderStrip(defaultProps());

    const rows = await screen.findAllByRole("listitem");
    for (const row of rows) {
      expect(within(row).getByRole("button", { name: /steer now/i })).toBeTruthy();
      expect(within(row).getByRole("button", { name: /edit/i })).toBeTruthy();
      expect(within(row).getByRole("button", { name: /remove from queue/i })).toBeTruthy();
    }
  });
});

describe("promote", () => {
  test("clicking steer-now calls promoteQueuedAsSteer with the row's index and entry id", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] },
      },
    });
    fake.on("turn/promoteQueuedAsSteer", () => ({}));
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /steer now/i }));
    });

    const call = fake.calls.find((c) => c.method === "turn/promoteQueuedAsSteer");
    expect(call?.params).toEqual({ ref: "ref_a", index: 0, expectedEntryId: "q1" });
  });

  test("a failed promote shows an error toast and leaves the row in place", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] },
      },
    });
    fake.on("turn/promoteQueuedAsSteer", () => {
      throw new Error("queue shifted");
    });
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /steer now/i }));
    });

    await screen.findByText(/couldn't steer with this message now.*queue shifted/i);
    expect(await screen.findAllByRole("listitem")).toHaveLength(1);
  });
});

describe("cancel", () => {
  test("clicking remove calls cancelQueued with the row's index and entry id", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] },
      },
    });
    fake.on("turn/cancelQueued", () => ({ removedText: "hello" }));
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /remove from queue/i }));
    });

    const call = fake.calls.find((c) => c.method === "turn/cancelQueued");
    expect(call?.params).toEqual({ ref: "ref_a", index: 0, expectedEntryId: "q1" });
  });

  test("a successful cancel that removed images shows a warning toast about re-attaching", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] },
      },
    });
    fake.on("turn/cancelQueued", () => ({ removedText: "hello", removedImages: 2 }));
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /remove from queue/i }));
    });

    await screen.findByText(/2 image attachments weren't restored/i);
  });

  test("a failed cancel shows an error toast and the row stays", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] },
      },
    });
    fake.on("turn/cancelQueued", () => {
      throw new Error("already consumed");
    });
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /remove from queue/i }));
    });

    await screen.findByText(/couldn't remove.*already consumed/i);
    expect(await screen.findAllByRole("listitem")).toHaveLength(1);
  });
});

describe("edit", () => {
  test("restores the FULL text to the composer BEFORE calling cancelQueued (loser-safe order)", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["the full untruncated message"], preview: ["the full…"] },
      },
    });
    const calls: string[] = [];
    fake.on("turn/cancelQueued", () => {
      calls.push("cancelQueued");
      return { removedText: "the full untruncated message" };
    });
    const onRestoreToComposer = vi.fn(() => calls.push("restore"));
    renderStrip(defaultProps({ onRestoreToComposer }));

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /edit/i }));
    });

    expect(onRestoreToComposer).toHaveBeenCalledWith("the full untruncated message");
    expect(calls).toEqual(["restore", "cancelQueued"]);
  });

  test("if the underlying cancel fails, the restored text stays (already applied) and the toast says so", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] },
      },
    });
    fake.on("turn/cancelQueued", () => {
      throw new Error("already consumed");
    });
    const onRestoreToComposer = vi.fn();
    renderStrip(defaultProps({ onRestoreToComposer }));

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /edit/i }));
    });

    expect(onRestoreToComposer).toHaveBeenCalledWith("hello");
    await screen.findByText(/moved to the composer.*couldn't remove.*already consumed/i);
  });

  test("edit is disabled for an image-only queued entry (blank text)", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: [""], preview: ["[image]"] },
      },
    });
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(isDisabled(within(row).getByRole("button", { name: /edit/i }))).toBe(true);
    expect(isDisabled(within(row).getByRole("button", { name: /remove from queue/i }))).toBe(false);
  });

  test("edit is disabled entirely when the daemon reports no texts array at all", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], preview: ["hello"] },
      },
    });
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(isDisabled(within(row).getByRole("button", { name: /edit/i }))).toBe(true);
    expect(isDisabled(within(row).getByRole("button", { name: /steer now/i }))).toBe(false);
    expect(isDisabled(within(row).getByRole("button", { name: /remove from queue/i }))).toBe(false);
  });
});

// Rows are never index-cached: they are recomputed fresh from model.queue's
// own arrays on every render, so a surviving row automatically re-keys to
// its new position once an earlier row is consumed - a contract row named
// explicitly for BOTH promote (test-queue-promote.js) and cancel
// (test-queue-edit-cancel.js): "surviving rows re-key their index," and
// "after a re-render, promoting a row sends that row's CURRENT entry_id,
// never a stale id carried over from an earlier snapshot."
describe("re-rendering after the queue shifts", () => {
  test("after the daemon confirms the head entry is consumed, the surviving row promotes with its NEW index", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: {
          depth: 2,
          ids: ["q1", "q2"],
          texts: ["first queued message", "second queued message"],
          preview: ["first queued message", "second queued message"],
        },
      },
    });
    fake.on("turn/promoteQueuedAsSteer", () => ({}));
    renderStrip(defaultProps());

    // The daemon confirms the FIRST entry (q1) was consumed elsewhere (e.g.
    // popped into a turn) - the surviving entry (originally at index 1)
    // shifts down to index 0, still carrying its OWN entryId (q2).
    act(() => {
      fake.emitNotification({
        method: "thread/queueChanged",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          queue: { depth: 1, ids: ["q2"], texts: ["second queued message"], preview: ["second queued message"] },
        },
      });
    });

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(within(row).getByText("second queued message")).toBeTruthy();
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /steer now/i }));
    });

    const call = fake.calls.find((c) => c.method === "turn/promoteQueuedAsSteer");
    expect(call?.params).toEqual({ ref: "ref_a", index: 0, expectedEntryId: "q2" });
  });

  test("after the daemon confirms the head entry is consumed, the surviving row cancels with its NEW index", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: {
          depth: 2,
          ids: ["q1", "q2"],
          texts: ["first queued message", "second queued message"],
          preview: ["first queued message", "second queued message"],
        },
      },
    });
    fake.on("turn/cancelQueued", () => ({ removedText: "second queued message" }));
    renderStrip(defaultProps());

    act(() => {
      fake.emitNotification({
        method: "thread/queueChanged",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          queue: { depth: 1, ids: ["q2"], texts: ["second queued message"], preview: ["second queued message"] },
        },
      });
    });

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /remove from queue/i }));
    });

    const call = fake.calls.find((c) => c.method === "turn/cancelQueued");
    expect(call?.params).toEqual({ ref: "ref_a", index: 0, expectedEntryId: "q2" });
  });
});

describe("degraded daemon: no entry ids", () => {
  test("every row action is disabled when the daemon reports no ids array at all", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, texts: ["hello"], preview: ["hello"] },
      },
    });
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(isDisabled(within(row).getByRole("button", { name: /steer now/i }))).toBe(true);
    expect(isDisabled(within(row).getByRole("button", { name: /edit/i }))).toBe(true);
    expect(isDisabled(within(row).getByRole("button", { name: /remove from queue/i }))).toBe(true);
  });
});

describe("in-flight row locking", () => {
  test("while a cancel is in flight, that row's own steer-now/edit/remove are all disabled", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] },
      },
    });
    let resolveCancel: (() => void) | undefined;
    fake.on(
      "turn/cancelQueued",
      () =>
        new Promise((resolve) => {
          resolveCancel = () => resolve({ removedText: "hello" });
        }),
    );
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    fireEvent.click(within(row).getByRole("button", { name: /remove from queue/i }));

    await vi.waitFor(() => {
      expect(isDisabled(within(row).getByRole("button", { name: /remove from queue/i }))).toBe(true);
    });
    expect(isDisabled(within(row).getByRole("button", { name: /steer now/i }))).toBe(true);
    expect(isDisabled(within(row).getByRole("button", { name: /edit/i }))).toBe(true);

    await act(async () => {
      resolveCancel?.();
    });
  });

  test("an in-flight action on one row does not disable a different row", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: {
          depth: 2,
          ids: ["q1", "q2"],
          texts: ["first queued message", "second queued message"],
          preview: ["first queued message", "second queued message"],
        },
      },
    });
    fake.on("turn/cancelQueued", () => new Promise(() => {})); // never resolves within this test
    renderStrip(defaultProps());

    const rows = await screen.findAllByRole("listitem");
    fireEvent.click(within(rows[0]!).getByRole("button", { name: /remove from queue/i }));

    await vi.waitFor(() => {
      expect(isDisabled(within(rows[0]!).getByRole("button", { name: /remove from queue/i }))).toBe(true);
    });
    expect(isDisabled(within(rows[1]!).getByRole("button", { name: /remove from queue/i }))).toBe(false);
  });
});

describe("optimistic pending queue rows", () => {
  test("a pending queue-method entry from another submission renders as an extra, action-less row", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: { ref: "ref_a", capabilities: CAPABILITIES, queue: { depth: 0 } },
    });
    fake.on("turn/queue", () => ({}));
    renderStrip(defaultProps());

    await act(async () => {
      await submitWithPendingTracking(
        { ref: "ref_a", method: "queue", text: "not yet confirmed", onFailure: () => {} },
        () => threadsStore.getState().queue("ref_a", "not yet confirmed"),
      );
    });

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(within(row).getByText("not yet confirmed")).toBeTruthy();
    expect(within(row).queryByRole("button")).toBeNull();
  });
});

describe("drain-as-steer affordance", () => {
  test("the drain button is absent when there is nothing queued", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", { serf: { ref: "ref_a", capabilities: CAPABILITIES, queue: { depth: 0 } } });
    renderStrip(defaultProps());
    expect(screen.queryByRole("button", { name: "Steer queue now" })).toBeNull();
  });

  test("clicking the drain button drains the composer's current text into the queue as steering", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
      },
    });
    fake.on("turn/drainAsSteer", () => ({}));
    const onDrainSuccess = vi.fn();
    renderStrip(
      defaultProps({ getComposerText: () => ({ text: "my current draft", hasPending: false }), onDrainSuccess }),
    );

    await act(async () => {
      fireEvent.click(await screen.findByRole("button", { name: "Steer queue now" }));
    });

    const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
    expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "my current draft" }] });
    expect(onDrainSuccess).toHaveBeenCalledTimes(1);
  });

  test("a queuedDrainPartial failure shows a distinct 'queued, but drain failed' message", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
      },
    });
    fake.on("turn/drainAsSteer", () => {
      // Same wire code as Conflict() but a different serfErrorInfo (parity
      // §A) - the discriminator threads.ts's mapConflict uses is the
      // serfErrorInfo string, not the numeric code alone (see
      // stores/threads.test.ts's own identical construction).
      throw new WireError("queue drained partially", -32013, { serfErrorInfo: "queuedDrainPartial" });
    });
    renderStrip(defaultProps());

    await act(async () => {
      fireEvent.click(await screen.findByRole("button", { name: "Steer queue now" }));
    });

    await screen.findByText(/queued.*drain failed/i);
  });

  test("a generic drain failure shows the plain 'drain failed' message", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
      },
    });
    fake.on("turn/drainAsSteer", () => {
      throw new Error("network unreachable");
    });
    renderStrip(defaultProps());

    await act(async () => {
      fireEvent.click(await screen.findByRole("button", { name: "Steer queue now" }));
    });

    await screen.findByText(/drain failed.*network unreachable/i);
  });

  // Mirrors Composer.tsx's own submit-time guard (handleFormSubmit/
  // handleSteerClick block on attachments.hasPending with the identical
  // toast) - QueueStrip's "Steer queue now" button had no equivalent check
  // (w5-integration-wiring-report.md Concern #3), so a drain triggered
  // mid-encode would silently omit the not-yet-encoded image from the
  // drained payload rather than refusing the whole request like every
  // other submit path does.
  test("a mid-encode attachment (hasPending) blocks the drain with a toast, never calling drainAsSteer", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
      },
    });
    fake.on("turn/drainAsSteer", () => ({}));
    renderStrip(defaultProps({ getComposerText: () => ({ text: "my current draft", hasPending: true }) }));

    await act(async () => {
      fireEvent.click(await screen.findByRole("button", { name: "Steer queue now" }));
    });

    await screen.findByText(/image attachment is still processing/i);
    expect(fake.calls.filter((c) => c.method === "turn/drainAsSteer")).toHaveLength(0);
  });

  test("the drain button disables itself while its own request is in flight", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
      },
    });
    let resolveDrain: (() => void) | undefined;
    fake.on(
      "turn/drainAsSteer",
      () =>
        new Promise((resolve) => {
          resolveDrain = () => resolve({});
        }),
    );
    render(<DrainBusyHarness />);

    const drainButton = await screen.findByRole("button", { name: "Steer queue now" });
    fireEvent.click(drainButton);

    await vi.waitFor(() => {
      expect(isDisabled(screen.getByRole("button", { name: "Steer queue now" }))).toBe(true);
    });

    await act(async () => {
      resolveDrain?.();
    });
  });

  test("the shared busy prop (a different in-flight action elsewhere) also disables the drain button", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      serf: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
      },
    });
    fake.on("turn/drainAsSteer", () => ({}));
    renderStrip(defaultProps({ busy: true }));

    const drainButton = await screen.findByRole("button", { name: "Steer queue now" });
    expect(isDisabled(drainButton)).toBe(true);

    fireEvent.click(drainButton);
    expect(fake.calls.filter((c) => c.method === "turn/drainAsSteer")).toHaveLength(0);
  });
});
