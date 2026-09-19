import { createTestConversationStore } from "../../mobile/src/state/conversationTestUtils";
import { expect, it } from "vitest";
import type {
  AnyNotification,
  Thread,
  ThreadItem,
} from "@evener/appwire-client";
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
        sharedNotes: false,
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
  const store = createTestConversationStore();
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
  // An empty input-images list is not a removal — the wire carries no "the
  // input images are gone" signal for a userMessage/steering item (only
  // OutputImages does), so a settle that carries `images: []` keeps the
  // attachment row the reader is looking at, exactly as the hub's own merge
  // reads `len(incoming.Images) == 0` (server/appwire_turns.go:884-889).
  publish({ ...item, images: [] });
  expect(store.getState().conversation?.items).toEqual([
    { kind: "user", id: "user", text: "look" },
    {
      kind: "attachments",
      id: "user:attachments",
      items: [
        {
          id: "user:0",
          src: "data:image/png;base64,BAUG",
          name: "second.png",
        },
      ],
    },
  ]);
  expect(reads()).toBe(1);
});

it("keeps live input images when a settle omits the images field entirely", async () => {
  const { store, publish } = await setup();
  const item: ThreadItem = {
    type: "userMessage",
    id: "user",
    status: "completed",
    text: "look",
    images: [
      { type: "image", mediaType: "image/png", data: "AQID", name: "first.png" },
    ],
  };
  publish(item);
  expect(
    store.getState().conversation?.items.find((i) => i.kind === "attachments"),
  ).toBeDefined();
  // No `images` key at all — the same "said nothing" reading as an empty
  // list (imagesToItemImagesForSession answers undefined for both).
  const { images: _omitted, ...withoutImages } = item;
  publish(withoutImages as ThreadItem);
  expect(
    store.getState().conversation?.items.find((i) => i.kind === "attachments"),
  ).toMatchObject({
    items: [{ src: "data:image/png;base64,AQID", name: "first.png" }],
  });
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

it("keeps a live image replacement when an older snapshot arrives", async () => {
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
  publish({
    ...item,
    images: [{ type: "image", mediaType: "image/png", data: "BAUG", name: "new.png" }],
  });
  held.release();
  await refresh;
  const rows = store
    .getState()
    .conversation?.items.filter((item) => item.kind === "attachments");
  expect(rows).toHaveLength(1);
  expect(rows?.[0]).toMatchObject({
    items: [{ src: "data:image/png;base64,BAUG" }],
  });
});

// An empty images list on the live settle is not a removal, so the item's
// images survive both the live frame and the racing older-cut snapshot
// (which never said otherwise either) — there is nothing here for the
// snapshot to "win" over.
it("keeps the item's images when a live settle carries an empty list and an older snapshot arrives", async () => {
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
  publish({ ...item, images: [] });
  held.release();
  await refresh;
  const rows = store
    .getState()
    .conversation?.items.filter((item) => item.kind === "attachments");
  expect(rows).toHaveLength(1);
  expect(rows?.[0]).toMatchObject({
    items: [{ src: "data:image/png;base64,AQID" }],
  });
});

// An overlapping older page carrying the same attachment adds no second row:
// the live settle's empty list denied nothing (so the row the reader is
// looking at survives), and page history is prepended only for identities
// the projection does not already hold.
it("adds no duplicate attachment row from an overlapping older page", async () => {
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
    ["user", "attachments"],
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
