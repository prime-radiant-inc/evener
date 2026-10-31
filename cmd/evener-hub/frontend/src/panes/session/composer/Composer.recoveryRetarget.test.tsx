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
import { seedRejectedRecovery, submitButton, textarea } from "./composerTestUtils";
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

function readResponse(ref?: string): ThreadReadResponse {
  return {
    thread: { ...thread, evener: { ...thread.evener, ref: ref ?? "root" } },
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc", length: 0 },
  };
}

interface WiredFake {
  delivered: Array<Record<string, unknown>>;
  readReached: Promise<void>;
  releaseRead(): void;
}

function wireFake(fake: FakeClient, resumed: string, gateDestinationRead: boolean): WiredFake {
  const readReached = deferred<void>();
  let releaseRead: (() => void) | undefined;
  fake.on("thread/read", async ({ ref }) => {
    if (gateDestinationRead && ref === resumed) {
      readReached.resolve();
      await new Promise<void>((resolve) => {
        releaseRead = resolve;
      });
    }
    return readResponse(ref);
  });
  const delivered: Array<Record<string, unknown>> = [];
  fake.on("turn/start", (params) => {
    const request = params as unknown as { clientMutationId?: string } & Record<string, unknown>;
    delivered.push(request);
    return {
      turn: { id: "turn-recovery-resend", status: "inProgress", itemsView: "full", items: [] },
      receipt: {
        clientMutationId: request.clientMutationId ?? "",
        disposition: "applied",
        threadId: "thread-a",
        projectionState: "reflected",
      },
    };
  });
  return {
    delivered,
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
  const wired = wireFake(fake, "local:delivered", false);
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
  const wired = wireFake(fake, resumed, phase === "hydration");
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
  const wired = wireFake(fake, resumed, false);
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
