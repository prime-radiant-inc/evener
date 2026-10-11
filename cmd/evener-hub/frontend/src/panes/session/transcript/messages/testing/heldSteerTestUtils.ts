// The shared held-steer test harness (steering-ghost spec §2/§5): the
// fixtures HeldSteerStack.test.tsx and HeldSteerAnnouncements.test.tsx both
// seed through - a wire-shaped thread, a connected FakeClient, hydrate for
// status/reflection, and seedHeld, which writes a real durable outbox row
// through the same storage every submission path uses and settles the shared
// projection so a render observes the seeded entry, not a racing refresh.
// Enqueueing alone never reconciles the entry (pendingTurnsStore's own
// contract), so the row stays in its seeded submitting state. seedHeld
// returns the clientMutationId so tests can key wire fixtures and departure
// writes to it; the ref option seeds a ref other than ref_a (the ref-change
// baseline test needs ref_b's hold to predate its first observation), every
// other caller takes the default.
import type { ConnectionState, Thread, ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { connectionStore } from "../../../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../../../stores/mutationOutboxIndexedDB";
import type { InputAttachment } from "../../../../../stores/threads";
import { threadsStore } from "../../../../../stores/threads";
import type { PendingMethod } from "../../../composer/queue/pendingReconcile";
import { refreshPendingTurnsProjection } from "../../../composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../../../composer/queue/testing/flushPendingTurnsProjection";

export const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

export function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
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
    source: "evener",
    evener: {
      ref,
      mutationStateAuthoritative: true,
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
    turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
    ...overrides,
  };
}

export function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

export function connectFakeClient(state: ConnectionState = "ready"): FakeClient {
  const fake = new FakeClient(state);
  connectionStore.getState().connect(fake);
  return fake;
}

export async function hydrate(fake: FakeClient, ref: string, overrides: Partial<Thread> = {}): Promise<void> {
  fake.on("thread/read", () => readResponse(ref, overrides));
  await threadsStore.getState().ensureThread(ref);
}

export async function seedHeld(
  method: PendingMethod,
  text: string,
  opts: { ref?: string; skillNames?: string[]; attachments?: InputAttachment[] } = {},
): Promise<string> {
  const ref = opts.ref ?? "ref_a";
  const wireMethod = {
    send: "turn/start",
    steer: "turn/steer",
    queue: "turn/queue",
    drain: "turn/drainAsSteer",
    promote: "turn/promoteQueuedAsSteer",
  }[method];
  const skillNames = opts.skillNames ?? [];
  const attachments = opts.attachments ?? [];
  const input = [
    ...(text ? [{ type: "text", text }] : []),
    ...attachments.map((attachment) => ({
      type: "image",
      mediaType: attachment.mediaType,
      data: attachment.data,
      name: attachment.name,
    })),
    ...skillNames.map((name) => ({ type: "skill", name })),
  ];
  const storage = new MutationOutboxIndexedDB();
  const outbox = await storage.enqueueIntent({
    targetRef: ref,
    method: wireMethod,
    payload: { ref, input },
    attachments: attachments.map((attachment, index) => ({
      presentationId: `presentation_${index}`,
      marker: attachment.marker,
      name: attachment.name ?? "attachment",
      mediaType: attachment.mediaType,
      blob: new Blob(),
    })),
    optimisticDisplay: { method: wireMethod, input },
  });
  storage.close();
  await refreshPendingTurnsProjection(ref);
  await flushPendingTurnsProjectionForTests();
  return outbox.clientMutationId;
}
