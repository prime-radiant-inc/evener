// The recovery Send intent across a peer retarget (issue #5182's acceptance
// matrix): the pane still displays the superseded ref while the row's durable
// destination has moved, so the Send must (1) target the destination, (2)
// fence a Stop on EITHER ref from the press's own synchronous prefix, during
// both of the send's waits - its persistence and the destination hydration -
// and (3) hydrate an unread destination under a foreground claim that is
// released exactly when it was acquired.

import type { Thread, ThreadReadResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../../shell/clientContext";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { connectionStore, resetConnectionStoreForTests } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../stores/threads";
import { createTestComposerSource } from "../testing/composerSource";
import { replaceEditorText } from "../testing/editor";
import { Composer } from "./Composer";
import { notAcceptedError, seedRejectedRecovery, submitButton, textarea } from "./composerTestUtils";
import { resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./queue/testing/flushPendingTurnsProjection";

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  globalThis.indexedDB = new IDBFactory();
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  vi.restoreAllMocks();
});

const thread: Thread = {
  id: "thread-a",
  sessionId: "session-a",
  preview: "",
  ephemeral: false,
  modelProvider: "anthropic/claude-sonnet-4-5",
  createdAt: 1,
  updatedAt: 1,
  status: { type: "idle" },
  cwd: "/tmp/project",
  cliVersion: "1",
  source: "evener",
  turns: [],
  evener: {
    ref: "root",
    mutationStateAuthoritative: true,
    queue: { revision: 0 },
    capabilities: {
      compact: true,
      clear: true,
      forkFromTurn: true,
      shutdown: true,
      changeModel: true,
      changeVisionModel: true,
      goal: true,
      sharedNotes: true,
      rename: true,
      send: true,
      queue: true,
      steer: true,
      interrupt: true,
      skillInput: true,
      commandInput: true,
    },
  },
};

function readResponse(ref?: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return {
    thread: { ...thread, ...overrides, evener: { ...thread.evener, ref: ref ?? "root" } },
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc", length: 0 },
  };
}

interface WiredFake {
  delivered: Array<Record<string, unknown>>;
  methods: string[];
  readReached: Promise<void>;
  releaseRead(): void;
}

interface WireOptions {
  gateDestinationRead?: boolean;
  activeDestination?: boolean;
  refuseDestinationStart?: boolean;
}

function wireFake(fake: FakeClient, resumed: string, options: WireOptions = {}): WiredFake {
  const readReached = deferred<void>();
  let releaseRead: (() => void) | undefined;
  fake.on("thread/read", async ({ ref }) => {
    if (options.gateDestinationRead && ref === resumed) {
      readReached.resolve();
      await new Promise<void>((resolve) => {
        releaseRead = resolve;
      });
    }
    return readResponse(ref, options.activeDestination && ref === resumed ? { status: { type: "active" } } : {});
  });
  const delivered: Array<Record<string, unknown>> = [];
  const methods: string[] = [];
  const receiveTurn = (params: unknown, method: string) => {
    const request = params as unknown as { clientMutationId?: string } & Record<string, unknown>;
    delivered.push(request);
    methods.push(method);
    if (options.refuseDestinationStart && method === "turn/start" && request.ref === resumed) {
      throw notAcceptedError(String(request.clientMutationId ?? ""));
    }
    return {
      turn: { id: "turn-recovery-resend", status: "inProgress", itemsView: "full", items: [] },
      receipt: {
        clientMutationId: request.clientMutationId ?? "",
        disposition: "applied",
        threadId: "thread-a",
        projectionState: "reflected",
      },
    };
  };
  fake.on("turn/start", (params) => {
    return receiveTurn(params, "turn/start");
  });
  fake.on("turn/queue", (params) => receiveTurn(params, "turn/queue"));
  return {
    delivered,
    methods,
    readReached: readReached.promise,
    releaseRead: () => releaseRead?.(),
  };
}

// The staging every case in this file shares: a rejected recovery row this
// pane's seat owns restores at mount, a peer retargets it to `resumed`, and
// the source reconciles the destination through its next successful autosave -
// the issue's "autosave follows a peer retarget" shape. After this returns,
// the composer still displays "root" while the send must target `resumed`.
async function stageRetargetedSend(
  storage: MutationOutboxIndexedDB,
  fake: FakeClient,
  resumed: string,
): Promise<string> {
  setMutationStorageForTests(storage);
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread("root");
  const recovered = await seedRejectedRecovery(storage, "root", "old composition");
  const source = createTestComposerSource("root");
  render(
    <ClientProvider client={fake}>
      <Composer ref="root" source={source} focused={false} />
    </ClientProvider>,
  );
  await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId));
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  const peer = new MutationOutboxIndexedDB();
  await peer.retargetOutbox("root", resumed, { threadId: "thread-new", instanceId: "instance-new" });
  await act(async () => {
    replaceEditorText(textarea(), "reconciled composition");
  });
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  expect(source.recoveryTargetRef).toBe(resumed);
  peer.close();
  return recovered.clientMutationId;
}

test("a retargeted recovery Send targets the destination and releases its claim", async () => {
  vi.spyOn(console, "error").mockImplementation(() => {});
  const storage = new MutationOutboxIndexedDB();
  const fake = new FakeClient("ready");
  const wired = wireFake(fake, "local:delivered");
  const recoveryId = await stageRetargetedSend(storage, fake, "local:delivered");
  await act(async () => {
    fireEvent.click(submitButton());
  });
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(wired.delivered).toHaveLength(1));
  expect(wired.delivered[0]).toMatchObject({ ref: "local:delivered" });
  expect(typeof wired.delivered[0]?.expectedInstanceId).toBe("string");
  // The resend consumed the retargeted recovery row.
  expect(await storage.getRecovery(recoveryId)).toBeUndefined();
  // The destination claim was temporary: with the send done and no other
  // reader holding the resumed ref, its model retires.
  cleanup();
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(threadsStore.getState().threads.has("local:delivered")).toBe(false));
});

type StopOrigin = "displayed" | "destination" | "both";
type StopPhase = "persistence" | "hydration";

class HeldWriteStorage extends MutationOutboxIndexedDB {
  readonly reached = deferred<void>();
  private release = deferred<void>();
  private holdNextUpdate = false;

  armHold(): void {
    this.holdNextUpdate = true;
  }

  finishHold(): void {
    this.release.resolve();
  }

  override async updateRecoveryInput(
    ...args: Parameters<MutationOutboxIndexedDB["updateRecoveryInput"]>
  ): ReturnType<MutationOutboxIndexedDB["updateRecoveryInput"]> {
    const result = await super.updateRecoveryInput(...args);
    if (this.holdNextUpdate) {
      this.holdNextUpdate = false;
      this.reached.resolve();
      await this.release.promise;
    }
    return result;
  }
}

async function runStopMatrixCase(stop: StopOrigin, phase: StopPhase): Promise<void> {
  vi.spyOn(console, "error").mockImplementation(() => {});
  const storage = new HeldWriteStorage();
  const resumed = `local:matrix-${phase}-${stop}`;
  const fake = new FakeClient("ready");
  const wired = wireFake(fake, resumed, phase === "hydration" ? { gateDestinationRead: true } : {});
  await stageRetargetedSend(storage, fake, resumed);
  const peer = new MutationOutboxIndexedDB();
  try {
    if (phase === "persistence") storage.armHold();
    await act(async () => {
      fireEvent.click(submitButton());
    });
    if (phase === "persistence") await storage.reached.promise;
    else await wired.readReached;
    // The Stop lands while the Send is parked on its own persistence or on
    // the destination hydration - both after the press, so both must fence.
    if (stop === "displayed" || stop === "both") await peer.cancelUnattempted("root");
    if (stop === "destination" || stop === "both") await peer.cancelUnattempted(resumed);
    if (phase === "persistence") storage.finishHold();
    else wired.releaseRead();
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(wired.delivered).toHaveLength(0));
    // The resent row is born canceled on the destination: a Stop on either
    // owner cancels the Send rather than letting it dispatch after the Stop.
    const rows = await storage.listOutbox(resumed);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ state: "canceled", targetRef: resumed });
  } finally {
    storage.finishHold();
    wired.releaseRead();
    peer.close();
  }
}

test.each([
  ["displayed", "persistence"],
  ["destination", "persistence"],
  ["both", "persistence"],
  ["displayed", "hydration"],
  ["destination", "hydration"],
  ["both", "hydration"],
] as const)("a stop on the %s ref during %s cancels the retargeted send", async (stop, phase) => {
  await runStopMatrixCase(stop, phase);
});

class HeldTargetReadStorage extends MutationOutboxIndexedDB {
  readonly reached = deferred<void>();
  private release = deferred<void>();
  private hold = false;

  armHold(): void {
    this.hold = true;
  }

  finishHold(): void {
    this.release.resolve();
  }

  override async getRecoveryWithStopEpoch(
    ...args: Parameters<MutationOutboxIndexedDB["getRecoveryWithStopEpoch"]>
  ): ReturnType<MutationOutboxIndexedDB["getRecoveryWithStopEpoch"]> {
    const result = await super.getRecoveryWithStopEpoch(...args);
    if (this.hold) {
      this.hold = false;
      this.reached.resolve();
      await this.release.promise;
    }
    return result;
  }
}

test("a stop while the press-time destination read is delayed cancels the send", async () => {
  vi.spyOn(console, "error").mockImplementation(() => {});
  const storage = new HeldTargetReadStorage();
  const resumed = "local:stop-read";
  const fake = new FakeClient("ready");
  const wired = wireFake(fake, resumed);
  await stageRetargetedSend(storage, fake, resumed);
  const peer = new MutationOutboxIndexedDB();
  try {
    storage.armHold();
    await act(async () => {
      fireEvent.click(submitButton());
    });
    // The press's own destination snapshot has not ANSWERED yet; a Stop that
    // lands in that window still belongs after the press's capture request,
    // so it must cancel the Send rather than become its baseline.
    await storage.reached.promise;
    await peer.cancelUnattempted(resumed);
    storage.finishHold();
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(wired.delivered).toHaveLength(0));
    const rows = await storage.listOutbox(resumed);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ state: "canceled", targetRef: resumed });
  } finally {
    storage.finishHold();
    peer.close();
  }
});

test("a stop on the displayed ref after an offline resend cancels it before reconnect", async () => {
  vi.spyOn(console, "error").mockImplementation(() => {});
  const storage = new MutationOutboxIndexedDB();
  const resumed = "local:offline-stop";
  const fake = new FakeClient("ready");
  const wired = wireFake(fake, resumed);
  await stageRetargetedSend(storage, fake, resumed);
  try {
    // The destination is already tracked before the send - a reader on the
    // resumed session has it open - so the claim takes nothing and the resend
    // commits straight to the outbox.
    await threadsStore.getState().ensureThread(resumed);
    // The tab drops offline before the Send: the row commits durably and its
    // dispatch parks on the reconnect.
    fake.emitStateChange("reconnecting");
    await act(async () => {
      fireEvent.click(submitButton());
    });
    await waitFor(async () => expect(await storage.listOutbox(resumed)).toHaveLength(1));
    // A Stop on the still-displayed session lands while dispatch is paused.
    // The row targets the destination, so this Stop can only reach it through
    // the displayed-ref cancellation ownership the resend carried.
    await storage.enqueueInterruptAndCancel({
      targetRef: "root",
      method: "turn/interrupt",
      payload: { ref: "root" },
      attachments: [],
      optimisticDisplay: { method: "turn/interrupt", input: [] },
    });
    // Reconnect: dispatch resumes, and the Stop must have won.
    fake.emitReady();
    await flushPendingTurnsProjectionForTests();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 150));
    });
    expect(wired.delivered).toHaveLength(0);
    const rows = await storage.listOutbox(resumed);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ state: "canceled", targetRef: resumed });
  } finally {
    fake.emitReady();
  }
});

test("a retargeted Send whose destination is running routes as a queue", async () => {
  vi.spyOn(console, "error").mockImplementation(() => {});
  const storage = new MutationOutboxIndexedDB();
  const resumed = "local:active-destination";
  const fake = new FakeClient("ready");
  const wired = wireFake(fake, resumed, {
    activeDestination: true,
    // The hub's AcceptClientMutationStart refusal: a fresh turn/start into a
    // session that is already running a user-started turn.
    refuseDestinationStart: true,
  });
  const recoveryId = await stageRetargetedSend(storage, fake, resumed);
  // A reader on the resumed session has it open, and its turn is running.
  await threadsStore.getState().ensureThread(resumed);
  expect(threadsStore.getState().threads.get(resumed)?.status.type).toBe("active");
  await act(async () => {
    fireEvent.click(submitButton());
  });
  await flushPendingTurnsProjectionForTests();
  // The displayed session offered Send, but the destination decides the
  // route: the send queues behind the destination's running turn instead of
  // bouncing as a rejected recovery under a session the user is not looking
  // at.
  await waitFor(() => expect(wired.methods).toContain("turn/queue"));
  expect(wired.methods).not.toContain("turn/start");
  const deliveredQueue = wired.delivered[wired.methods.indexOf("turn/queue")];
  expect(deliveredQueue).toMatchObject({ ref: resumed });
  await waitFor(async () => expect(await storage.listRecovery(resumed)).toHaveLength(0));
  expect(await storage.getRecovery(recoveryId)).toBeUndefined();
});
