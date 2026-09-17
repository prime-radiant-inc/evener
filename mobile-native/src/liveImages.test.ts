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

// The read response is ordered at the snapshot cut, so a live image change
// this store folded before the response is already reflected in the snapshot
// that arrives: the snapshot's images are the ones that commit.
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
  it(`commits the snapshot's image over a live ${replacement.length ? "replacement" : "removal"}`, async () => {
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
    // The live change is on screen at once.
    expect(
      store
        .getState()
        .conversation?.items.filter((row) => row.kind === "attachments"),
    ).toHaveLength(replacement.length);
    held.release();
    await refresh;
    // The snapshot then settles it: its own image is what the row shows.
    const rows = store
      .getState()
      .conversation?.items.filter((row) => row.kind === "attachments");
    expect(rows).toHaveLength(1);
    expect(rows?.[0]).toMatchObject({
      items: [{ src: "data:image/png;base64,AQID" }],
    });
  });
}

// An overlapping older page is the only evidence anybody has about an input
// image a live frame did not mention. The wire has no "the images are gone"
// signal for input images — an empty or absent list says nothing, which is how
// the hub reads it too (`len(incoming.Images) == 0` keeps the existing list:
// server/appwire_turns.go:884-886, internal/apptranscript/logical_turn.go:309)
// — so the page restores them rather than a stale removal winning. D23d's own
// delta: with pages merged into the model, mergePageItem decides this, where the
// row-level prepend used to leave the live row alone.
it("restores input images from an overlapping older page when no frame denied them", async () => {
  const item: ThreadItem = {
    type: "userMessage",
    id: "user",
    status: "completed",
    text: "look",
    images: [{ type: "image", mediaType: "image/png", data: "AQID" }],
  };
  const { store, service, publish } = await setup([item]);
  publish({ ...item, images: [] });
  expect(
    store.getState().conversation?.items.map((row) => row.kind),
  ).toEqual(["user"]);
  expect((await store.getState().loadOlder(service)).status).toBe("loaded");
  expect(store.getState().conversation?.items.map((row) => row.kind)).toEqual(
    ["user", "attachments"],
  );
});

it("commits the snapshot's own rows over an image added while the read was in flight", async () => {
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
  // On screen at once, beside its source...
  expect(store.getState().conversation?.items.map((row) => row.id)).toEqual([
    "user",
    "user:attachments",
    "assistant",
  ]);
  held.release();
  await refresh;
  // ...and settled by the snapshot, which carries no image for that item.
  expect(store.getState().conversation?.items.map((row) => row.id)).toEqual([
    "user",
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
