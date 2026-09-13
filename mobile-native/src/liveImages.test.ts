import { expect, it } from "vitest";
import type {
  AnyNotification,
  Thread,
  ThreadItem,
} from "../../appwire-client/typescript/types.gen";
import {
  type ConversationClientLike,
  createConversationService,
} from "../../mobile/src/services/conversation";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";

async function setup(initialItems: ThreadItem[] = []) {
  const thread: Thread = {
    id: "thread",
    sessionId: "thread",
    preview: "",
    ephemeral: false,
    modelProvider: "fake",
    createdAt: 0,
    updatedAt: 0,
    cwd: "/fixture",
    cliVersion: "test",
    source: "evener",
    status: { type: "active" },
    turns: [
      {
        id: "turn",
        status: "inProgress",
        itemsView: "default",
        items: initialItems,
      },
    ],
    evener: {
      ref: "local:thread",
      queue: { revision: 0 },
      capabilities: {
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
        rename: true,
      },
    },
  };
  let reads = 0;
  let holdRead: (() => Promise<void>) | undefined;
  const service = createConversationService({
    request: async (method: string) => {
      if (method === "thread/turns/list")
        return { data: thread.turns, nextCursor: null };
      if (method !== "thread/read") throw new Error(`Unexpected ${method}`);
      reads++;
      await holdRead?.();
      return { thread, olderCursor: "older" };
    },
    onNotification: () => () => {},
  } as ConversationClientLike);
  const store = createConversationStore();
  const sink = createActivityStore().getState();
  await store.getState().openProjected(service, sink, "local:thread");
  return {
    store,
    service,
    sink,
    holdNextRead: () => {
      let release!: () => void;
      let start!: () => void;
      const started = new Promise<void>((resolve) => {
        start = resolve;
      });
      const pending = new Promise<void>((resolve) => {
        release = resolve;
      });
      holdRead = () => {
        start();
        return pending;
      };
      return { started, release };
    },
    reads: () => reads,
    publish: (item: ThreadItem) =>
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread",
          ref: "local:thread",
          turnId: "turn",
          item,
        },
      } as AnyNotification),
  };
}

it("shows and replaces live input images without waiting for the turn to stop", async () => {
  const { store, publish, reads } = await setup();
  const item: ThreadItem = {
    type: "userMessage",
    id: "user",
    status: "completed",
    text: "look",
    images: [
      {
        type: "image",
        mediaType: "image/png",
        data: "AQID",
        name: "first.png",
      },
    ],
  };
  publish(item);
  expect(store.getState().conversation?.items).toEqual([
    { kind: "user", id: "user", text: "look" },
    {
      kind: "attachments",
      id: "user:attachments",
      items: [
        {
          id: "user:0",
          src: "data:image/png;base64,AQID",
          name: "first.png",
          mediaType: "image/png",
        },
      ],
    },
  ]);
  publish({
    ...item,
    images: [
      {
        type: "image",
        mediaType: "image/png",
        data: "BAUG",
        name: "second.png",
      },
    ],
  });
  const attachments = store
    .getState()
    .conversation?.items.find((i) => i.kind === "attachments");
  expect(attachments).toMatchObject({
    items: [{ src: "data:image/png;base64,BAUG", name: "second.png" }],
  });
  publish({ ...item, images: [] });
  expect(store.getState().conversation?.items).toEqual([
    { kind: "user", id: "user", text: "look" },
  ]);
  expect(reads()).toBe(1);
});

it("preserves tool-output image references in live item events", async () => {
  const { store, publish, reads } = await setup();
  publish({
    type: "commandExecution",
    id: "tool",
    status: "completed",
    toolName: "screenshot",
    outputImages: [
      {
        source: "fixture",
        url: "/s/thread/images/fixture",
        name: "capture.png",
        mediaType: "image/png",
      },
    ],
  });
  expect(store.getState().conversation?.items.map((i) => i.kind)).toEqual([
    "activity",
    "attachments",
  ]);
  expect(store.getState().conversation?.items[1]).toMatchObject({
    id: "tool:attachments",
    items: [{ src: "/s/thread/images/fixture", name: "capture.png" }],
  });
  expect(reads()).toBe(1);
});

for (const replacement of [
  [],
  [
    {
      type: "image" as const,
      mediaType: "image/png",
      data: "BAUG",
      name: "new.png",
    },
  ],
]) {
  it(`keeps a live image ${replacement.length ? "replacement" : "removal"} when an older snapshot arrives`, async () => {
    const item: ThreadItem = {
      type: "userMessage",
      id: "user",
      status: "completed",
      text: "look",
      images: [{ type: "image", mediaType: "image/png", data: "AQID" }],
    };
    const { store, service, sink, publish, holdNextRead } = await setup([item]);
    const held = holdNextRead();
    const refresh = store.getState().rehydrate(service, sink);
    await held.started;
    publish({ ...item, images: replacement });
    held.release();
    await refresh;
    const rows = store
      .getState()
      .conversation?.items.filter((item) => item.kind === "attachments");
    expect(rows).toHaveLength(replacement.length);
    if (replacement.length)
      expect(rows?.[0]).toMatchObject({
        items: [{ src: "data:image/png;base64,BAUG" }],
      });
  });
}

it("does not restore obsolete images from an overlapping older page", async () => {
  const item: ThreadItem = {
    type: "userMessage",
    id: "user",
    status: "completed",
    text: "look",
    images: [{ type: "image", mediaType: "image/png", data: "AQID" }],
  };
  const { store, service, publish } = await setup([item]);
  publish({ ...item, images: [] });
  expect((await store.getState().loadOlder(service)).status).toBe("loaded");
  expect(store.getState().conversation?.items.map((item) => item.kind)).toEqual(
    ["user"],
  );
});

it("keeps a newly added image beside its source after an overlapping snapshot", async () => {
  const item: ThreadItem = {
    type: "userMessage",
    id: "user",
    status: "completed",
    text: "look",
  };
  const { store, service, sink, publish, holdNextRead } = await setup([
    item,
    {
      type: "agentMessage",
      id: "assistant",
      status: "completed",
      text: "response",
    },
  ]);
  const held = holdNextRead();
  const refresh = store.getState().rehydrate(service, sink);
  await held.started;
  publish({
    ...item,
    images: [{ type: "image", mediaType: "image/png", data: "AQID" }],
  });
  held.release();
  await refresh;
  expect(store.getState().conversation?.items.map((item) => item.id)).toEqual([
    "user",
    "user:attachments",
    "assistant",
  ]);
});

it("accepts image removal in an authoritative snapshot after a live insertion", async () => {
  const item: ThreadItem = {
    type: "userMessage",
    id: "user",
    status: "completed",
    text: "look",
  };
  const { store, service, sink, publish } = await setup([item]);
  publish({
    ...item,
    images: [{ type: "image", mediaType: "image/png", data: "AQID" }],
  });
  expect(
    store
      .getState()
      .conversation?.items.some((row) => row.kind === "attachments"),
  ).toBe(true);
  await store.getState().rehydrate(service, sink);
  expect(store.getState().conversation?.items.map((row) => row.id)).toEqual([
    "user",
  ]);
});
