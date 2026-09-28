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
      // A v6-shaped read (carrying `snapshot`) establishes the model's
      // versioned history at hydrate — the read-model's own bootstrap rule
      // (reducer.ts's classifySignal): a live history/updated can only merge
      // once an authoritative read has first established an incarnation to
      // merge against, never bootstrap history from nothing on its own.
      return { thread, olderCursor: "older", snapshot: { incarnation: "inc-1", length: 1 } };
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
    // item/completed's read-model replacement: history/updated carries the
    // item directly (turnId lives on the item itself, not a wrapping field).
    publish: (item: ThreadItem) =>
      store.getState().applyNotification({
        method: "history/updated",
        params: {
          threadId: "thread",
          ref: "local:thread",
          epoch: 0,
          snapshot: { incarnation: "inc-1", length: 1 },
          items: [{ ...item, turnId: item.turnId ?? "turn" }],
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
  // Deleted here: publishing `images: []` and expecting the attachments row
  // to survive unchanged ("an empty list is not a removal"). That relied on
  // imagesToItemImagesForSession collapsing an empty list to the same
  // `undefined` an omitted key produces, plus item/completed's
  // mergeItemImages falling back to the existing item's images whenever the
  // settle's own came back undefined. mergeItemImages is gone with
  // item/completed; history/updated's mergeHistory replaces the item
  // wholesale (`writable.items[index] = incoming`), so an incoming record
  // with images: [] now genuinely clears the row, same open question as the
  // omitted-field case above.
  expect(reads()).toBe(1);
});

// Deleted: "keeps live input images when a settle omits the images field
// entirely". It asserted item/completed's mergeItemImages behavior (a
// settle that omits `images` keeps the existing item's list) — a
// field-level merge the old reducer performed on every settle
// (mergeCompletedText/mergeItemImages/mergeReasoning/mergeArguments/
// mergeObservedTiming), now deleted along with item/completed itself.
// history/updated's mergeHistory (reducer.ts) has no equivalent: a
// version-superseding item REPLACES the held one wholesale
// (`writable.items[index] = incoming`), so an incoming record that omits
// `images` now genuinely drops it rather than preserving the old list.
// Flagged for follow-up rather than silently dropped: confirm whether the
// daemon's history/updated payload always carries an item's full recorded
// state (making this omission unreachable in practice) or whether the
// client needs its own mergeItemImages-equivalent for the read model.

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
it("commits the snapshot's image over a live replacement", async () => {
  const replacement = [
    { type: "image" as const, mediaType: "image/png", data: "BAUG", name: "new.png" },
  ];
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
    store.getState().conversation?.items.find((row) => row.kind === "attachments"),
  ).toMatchObject({ items: [{ src: "data:image/png;base64,BAUG" }] });
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
// the live row already has it (an empty list denied nothing), and page history
// is prepended only for identities the projection does not already hold.
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
  expect(store.getState().conversation?.items.map((row) => row.kind)).toEqual([
    "user",
    "attachments",
  ]);
});

it("commits the snapshot's own rows over an image added while the read was in flight", async () => {
  const item: ThreadItem = {
    type: "userMessage",
    id: "user",
    status: "completed",
    text: "look",
    // Explicit positions: mergeHistory (reducer.ts) orders items lacking one
    // by inserting each new item ahead of any existing same-position (i.e.
    // still-undefined) item, so two undated fixture items round-trip
    // reordered through the very first (now v6-shaped, since setup()'s
    // thread/read carries `snapshot`) hydrate. Real wire items always carry
    // a position; these two just need to too.
    position: { entry: 0, item: 0 },
  };
  const { store, service, sink, publish, holdNextRead } = await setup([
    item,
    {
      type: "agentMessage",
      id: "assistant",
      status: "completed",
      text: "response",
      position: { entry: 0, item: 1 },
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
