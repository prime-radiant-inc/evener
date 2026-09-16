import { createTasksPanelStore } from "@evener/appwire-client";
import { expect, it } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { tasksReadThroughCurrentClient } from "./tasksRead";

const row = (id: number) => ({ id, status: "open", type: "task", description: `task-${id}`, prompt: "p" });

function clientAnswering(read: () => Promise<unknown>): ConversationClientLike {
  return {
    request: async () => ({ data: await read() }),
    onNotification: () => () => {},
  } as ConversationClientLike;
}

it("retains loaded rows when a replacement connection cannot refresh them", async () => {
  let client = clientAnswering(async () => [row(1)]);
  const store = createTasksPanelStore(tasksReadThroughCurrentClient(() => client));
  await store.refresh("local:test", () => true);
  expect(store.getState().entries.get("local:test")?.rows).toEqual([row(1)]);

  client = clientAnswering(async () => {
    throw new Error("Reconnect failed to load tasks");
  });
  await store.refresh("local:test", () => true);
  const entry = store.getState().entries.get("local:test");
  expect(entry?.rows).toEqual([row(1)]);
  expect(entry?.failure?.sentence).toContain("Reconnect failed to load tasks");
  expect(entry?.loading).toBe(false);
});
