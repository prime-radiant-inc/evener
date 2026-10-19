// @vitest-environment node

import { expect, test } from "vitest";
import type { AnyNotification } from "../../types.gen";
import { notificationMutationIdentities } from "./settledIdentities";

function history(items: { clientMutationId?: string }[]): AnyNotification {
  return {
    method: "history/updated",
    params: {
      threadId: "thread-1",
      ref: "ref-1",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
      items,
    },
  } as AnyNotification;
}

test("a history frame settles every recorded item's mutation id", () => {
  expect(
    notificationMutationIdentities(history([{ clientMutationId: "cmid-1" }, {}, { clientMutationId: "cmid-2" }])),
  ).toEqual(["cmid-1", "cmid-2"]);
});

test("a queue frame settles the ids still queued and the ones this push consumed", () => {
  const n = {
    method: "thread/queueChanged",
    params: {
      threadId: "thread-1",
      ref: "ref-1",
      queue: { revision: 4, clientMutationIds: ["cmid-queued"] },
      consumedClientMutationIds: ["cmid-drained"],
    },
  } as AnyNotification;
  expect(notificationMutationIdentities(n)).toEqual(["cmid-queued", "cmid-drained"]);
});

test("a malformed consumed list settles nothing, never guessed at", () => {
  const n = {
    method: "thread/queueChanged",
    params: { threadId: "thread-1", ref: "ref-1", queue: { revision: 4 }, consumedClientMutationIds: "cmid-1" },
  } as unknown as AnyNotification;
  expect(notificationMutationIdentities(n)).toEqual([]);
});

test("frames that carry no mutation identity settle nothing", () => {
  expect(
    notificationMutationIdentities({
      method: "thread/status/changed",
      params: { threadId: "thread-1", ref: "ref-1", status: { type: "idle" } },
    } as AnyNotification),
  ).toEqual([]);
});
