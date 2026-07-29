import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { MutationRecoveryKind } from "../../../../stores/mutationOutbox";
import { MutationOutboxIndexedDB } from "../../../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests } from "../../../../stores/threads";
import {
  refreshPendingTurnsProjection,
  resendRecoveryPendingTurn,
  resetPendingTurnsStoreForTests,
} from "../queue/pendingTurnsStore";
import { RecoveryTray } from "./RecoveryTray";

let storage: MutationOutboxIndexedDB;

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
});

afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
});

async function seedRecovery(
  recoveryKind: MutationRecoveryKind = "rejected",
  text = "recover this",
  withAttachment = false,
) {
  const input = [
    { type: "text" as const, text },
    ...(withAttachment ? [{ type: "image" as const, mediaType: "image/png", data: "AQID", name: "proof.png" }] : []),
  ];
  const outbox = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: {
      ref: "ref_a",
      input,
    },
    attachments: withAttachment
      ? [
          {
            presentationId: "presentation_1",
            name: "proof.png",
            mediaType: "image/png",
            blob: new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }),
          },
        ]
      : [],
    optimisticDisplay: {
      method: "turn/start",
      input,
    },
  });
  await storage.transferToRecovery(outbox.clientMutationId, recoveryKind);
  await refreshPendingTurnsProjection("ref_a");
  return outbox;
}

test("renders a rejected durable draft separately with its attachment still available", async () => {
  await seedRecovery("rejected", "recover this", true);

  render(<RecoveryTray sessionRef="ref_a" threadId="thread_a" />);

  expect(screen.getByRole("region", { name: "Recovery drafts" })).toBeTruthy();
  expect((screen.getByRole("textbox", { name: "Recovered message" }) as HTMLTextAreaElement).value).toBe(
    "recover this",
  );
  expect(screen.getByText("proof.png")).toBeTruthy();
});

test("edits the recovery record durably without changing the main composer", async () => {
  const outbox = await seedRecovery();
  render(<RecoveryTray sessionRef="ref_a" threadId="thread_a" />);

  fireEvent.change(screen.getByRole("textbox", { name: "Recovered message" }), {
    target: { value: "edited recovery" },
  });

  await waitFor(async () => {
    const recovered = await storage.getRecovery(outbox.clientMutationId);
    expect(recovered?.payload.input).toEqual([{ type: "text", text: "edited recovery" }]);
  });
});

test("simultaneous recovery sends have one durable winner", async () => {
  const outbox = await seedRecovery();

  const results = await Promise.all([
    resendRecoveryPendingTurn(outbox.clientMutationId, "ref_a", "thread_a"),
    resendRecoveryPendingTurn(outbox.clientMutationId, "ref_a", "thread_a"),
  ]);

  expect(results.filter(Boolean)).toHaveLength(1);
  expect(await storage.getRecovery(outbox.clientMutationId)).toBeUndefined();
  const resent = await storage.listOutbox("ref_a");
  expect(resent).toHaveLength(1);
  expect(resent[0]?.clientMutationId).not.toBe(outbox.clientMutationId);
});

test("blocked-unknown entries expose only Retry, Copy, and Export actions", async () => {
  const user = userEvent.setup();
  const outbox = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "outcome unknown" }] },
    attachments: [],
    optimisticDisplay: {
      method: "turn/start",
      input: [{ type: "text", text: "outcome unknown" }],
    },
  });
  await storage.markUnknown(outbox.clientMutationId, "blockedUnknown");
  await refreshPendingTurnsProjection("ref_a");

  render(<RecoveryTray sessionRef="ref_a" threadId="thread_a" />);

  expect(screen.getByText("Delivery outcome unknown")).toBeTruthy();
  expect(screen.getAllByRole("button").map((button) => button.textContent)).toEqual(["Retry", "Copy", "Export"]);
  expect(screen.queryByRole("button", { name: /abandon|unblock|send/i })).toBeNull();

  await user.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(async () => {
    expect((await storage.getOutbox(outbox.clientMutationId))?.state).toBe("submitting");
  });
});
