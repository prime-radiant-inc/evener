import { beforeEach, describe, expect, test } from "vitest";
import { createLiveSessionController, type LiveSessionDeps } from "./liveSessionController";

interface PendingLoad {
  offset: number;
  resolve: (loaded: boolean) => void;
  reject: (error: unknown) => void;
}

// A mutable fake world. Each load stays pending until the test settles it, and
// settling appends the rows the page "carries".
function fakeWorld(initial: { rows?: string[]; remaining?: number; focused?: string | null } = {}) {
  const world = {
    generation: "g1",
    rows: initial.rows ?? [],
    remaining: initial.remaining ?? 0,
    focused: initial.focused ?? null,
    pane: "pane-1" as string | null,
    epoch: 1,
    blocked: false,
    canDemandUnloaded: false,
    loads: [] as PendingLoad[],
    opened: [] as string[],
  };
  const deps: LiveSessionDeps = {
    generation: () => world.generation,
    liveRefs: () => world.rows,
    liveRemaining: () => world.remaining,
    canDemandUnloaded: () => world.canDemandUnloaded,
    nextOffset: () => world.rows.length,
    loadPage: (offset) =>
      new Promise<boolean>((resolve, reject) => {
        world.loads.push({ offset, resolve, reject });
      }),
    focusedPaneId: () => world.pane,
    focusedSessionRef: () => world.focused,
    routeEpoch: () => world.epoch,
    completionBlocked: () => world.blocked,
    openLiveSession: (ref) => {
      world.opened.push(ref);
      world.focused = ref;
    },
  };
  return { world, deps };
}

const settle = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

describe("loaded rows", () => {
  test("next and previous wrap within the loaded rows", () => {
    const { world, deps } = fakeWorld({ rows: ["a", "b", "c"], focused: "c" });
    const controller = createLiveSessionController(deps);
    controller.next();
    controller.previous();
    controller.previous();
    expect(world.opened).toEqual(["a", "c", "b"]);
    expect(world.loads).toHaveLength(0);
  });
});

describe("next at the last loaded row with more pages", () => {
  let ctx: ReturnType<typeof fakeWorld>;
  beforeEach(() => {
    ctx = fakeWorld({ rows: ["a", "b"], remaining: 1, focused: "b" });
  });

  test("loads the next page and opens its first new row", async () => {
    const controller = createLiveSessionController(ctx.deps);
    controller.next();
    expect(ctx.world.loads).toHaveLength(1);
    ctx.world.rows = ["a", "b", "c", "d"];
    ctx.world.loads[0]?.resolve(true);
    await settle();
    expect(ctx.world.opened).toEqual(["c"]);
  });

  test("a repeated press while the load is fresh waits on the same load", async () => {
    const controller = createLiveSessionController(ctx.deps);
    controller.next();
    controller.next();
    expect(ctx.world.loads).toHaveLength(1);
    ctx.world.rows = ["a", "b", "c"];
    ctx.world.loads[0]?.resolve(true);
    await settle();
    expect(ctx.world.opened).toEqual(["c"]);
  });

  test("a press against a stale owner adopts the load and navigates when it lands", async () => {
    const controller = createLiveSessionController(ctx.deps);
    controller.next();
    ctx.world.epoch++; // left and returned: same pane and ref, new route epoch
    controller.next();
    expect(ctx.world.loads).toHaveLength(2);
    ctx.world.rows = ["a", "b", "c"];
    // The first load lands: its owner was displaced, so it stays quiet.
    ctx.world.loads[0]?.resolve(true);
    await settle();
    expect(ctx.world.opened).toEqual([]);
    ctx.world.loads[1]?.resolve(true);
    await settle();
    expect(ctx.world.opened).toEqual(["c"]);
  });

  test("a direct navigation supersedes the in-flight demand", async () => {
    const controller = createLiveSessionController(ctx.deps);
    controller.next();
    ctx.world.rows = ["a", "b", "c"];
    ctx.world.focused = "a";
    ctx.world.remaining = 0;
    controller.next(); // direct step a -> b
    ctx.world.focused = "b";
    ctx.world.loads[0]?.resolve(true);
    await settle();
    expect(ctx.world.opened).toEqual(["b"]);
  });

  test.each([
    ["the route changed", (w: ReturnType<typeof fakeWorld>["world"]) => w.epoch++],
    ["the focused session changed", (w: ReturnType<typeof fakeWorld>["world"]) => (w.focused = "a")],
    ["the focused pane changed", (w: ReturnType<typeof fakeWorld>["world"]) => (w.pane = "pane-2")],
    ["the client reconnected", (w: ReturnType<typeof fakeWorld>["world"]) => (w.generation = "g2")],
    ["a palette, modal or editable has focus", (w: ReturnType<typeof fakeWorld>["world"]) => (w.blocked = true)],
  ])("goes inert when %s mid-flight", async (_name, change) => {
    const controller = createLiveSessionController(ctx.deps);
    controller.next();
    change(ctx.world);
    ctx.world.rows = ["a", "b", "c"];
    ctx.world.loads[0]?.resolve(true);
    await settle();
    expect(ctx.world.opened).toEqual([]);
  });

  test("goes inert after dispose", async () => {
    const controller = createLiveSessionController(ctx.deps);
    controller.next();
    controller.dispose();
    ctx.world.rows = ["a", "b", "c"];
    ctx.world.loads[0]?.resolve(true);
    await settle();
    expect(ctx.world.opened).toEqual([]);
  });

  test.each([
    ["settles with an error", (load: PendingLoad) => load.resolve(false)],
    ["rejects", (load: PendingLoad) => load.reject(new Error("boom"))],
  ])("a load that %s lets the next press retry", async (_name, fail) => {
    const controller = createLiveSessionController(ctx.deps);
    controller.next();
    const first = ctx.world.loads[0];
    if (!first) throw new Error("no load started");
    fail(first);
    await settle();
    controller.next();
    expect(ctx.world.loads).toHaveLength(2);
  });

  test("a reconnect between presses forgets the in-flight demand", () => {
    const controller = createLiveSessionController(ctx.deps);
    controller.next();
    ctx.world.generation = "g2";
    controller.next();
    expect(ctx.world.loads).toHaveLength(2);
  });

  test("a direct step in the other direction supersedes the demand", async () => {
    const controller = createLiveSessionController(ctx.deps);
    controller.next();
    ctx.world.focused = "b";
    controller.previous(); // index 1 > 0: direct step, bumps the intent
    expect(ctx.world.opened).toEqual(["a"]);
    ctx.world.focused = "b";
    ctx.world.rows = ["a", "b", "c"];
    ctx.world.loads[0]?.resolve(true);
    await settle();
    expect(ctx.world.opened).toEqual(["a"]);
  });
});

describe("previous", () => {
  test("walks every remaining page to the tail before opening it", async () => {
    const { world, deps } = fakeWorld({ rows: ["a", "b"], remaining: 2, focused: "a" });
    const controller = createLiveSessionController(deps);
    controller.previous(); // index 0 with pages remaining: demand through
    expect(world.loads).toHaveLength(1);
    world.rows = ["a", "b", "c"];
    world.remaining = 1;
    world.loads[0]?.resolve(true);
    await settle();
    expect(world.opened).toEqual([]);
    expect(world.loads).toHaveLength(2);
    world.rows = ["a", "b", "c", "d"];
    world.remaining = 0;
    world.loads[1]?.resolve(true);
    await settle();
    expect(world.opened).toEqual(["d"]);
  });

  test("an unloaded section demands its first page, and nothing when it cannot", async () => {
    const { world, deps } = fakeWorld({ rows: [], focused: null });
    const controller = createLiveSessionController(deps);
    controller.previous();
    expect(world.loads).toHaveLength(0);
    world.canDemandUnloaded = true;
    controller.previous();
    expect(world.loads).toHaveLength(1);
    world.rows = ["a", "b"];
    world.loads[0]?.resolve(true);
    await settle();
    expect(world.opened).toEqual(["b"]);
  });
});

test("next on an unloaded section demands the first page and opens its first row", async () => {
  const { world, deps } = fakeWorld({ rows: [], focused: null });
  world.canDemandUnloaded = true;
  const controller = createLiveSessionController(deps);
  controller.next();
  world.rows = ["a", "b"];
  world.loads[0]?.resolve(true);
  await settle();
  expect(world.opened).toEqual(["a"]);
});
