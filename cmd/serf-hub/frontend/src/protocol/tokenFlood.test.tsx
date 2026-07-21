// Wave-4 token-flood benchmark: correctness half (the timing/growth-curve
// profile lives in tokenFlood.bench.ts, `vitest bench` only - see that
// file's own header comment for why the split). The wave plan's binding
// gate (docs/superpowers/plans/2026-07-20-webui-rewrite-wave4-transcript.md):
// "recorded 10k-delta stream replayed through the store; frame budget
// documented, no dropped-chunk correctness failures." This file asserts the
// "no dropped-chunk correctness failures" half exactly; see
// docs/superpowers/plans/wave4-report.md for the measured frame-budget
// numbers this half's sibling produced.
import { act, cleanup, render, waitFor } from "@testing-library/react";
import { memo } from "react";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import Session from "../panes/session/Session";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../panes/session/transcript/types";
import { ClientProvider } from "../shell/clientContext";
import { connectionStore } from "../stores/connection";
import { resetThreadsStoreForTests } from "../stores/threads";
import { applyNotification } from "./reducer";
import { FakeClient } from "./testing/fakeClient";
import { buildFloodChunks, buildFloodStream, hydrateFloodModel } from "./testing/tokenFlood";
import type { AnyNotification, Thread, ThreadCapabilities, ThreadReadResponse } from "./types.gen";

const FLOOD_SIZE = 10_000;

describe("token-flood: 10k-delta correctness", () => {
  test("pendingText accumulates the exact concatenation of all 10,000 chunks, mid-stream, before settle", () => {
    const { notifications, expectedText, ref, itemId } = buildFloodStream(FLOOD_SIZE);
    let model = hydrateFloodModel(ref);
    let now = 1000;
    for (const n of notifications) {
      now += 1;
      if (n.method === "item/completed" || n.method === "turn/completed") break;
      model = applyNotification(model, n, now);
    }
    const item = model.turns[0]?.items.find((it) => it.id === itemId);
    expect(item?.pendingText).toHaveLength(FLOOD_SIZE);
    expect(item?.pendingText?.join("")).toBe(expectedText);
  });

  test("item/completed settles the exact wire text with no drops or reorders", () => {
    const { notifications, expectedText, ref, itemId } = buildFloodStream(FLOOD_SIZE);
    let model = hydrateFloodModel(ref);
    let now = 1000;
    for (const n of notifications) {
      now += 1;
      if (n.method === "turn/completed") break;
      model = applyNotification(model, n, now);
    }
    const item = model.turns[0]?.items.find((it) => it.id === itemId);
    expect(item?.text).toBe(expectedText);
    expect(item?.text.length).toBe(expectedText.length);
    expect(item?.pendingText).toBeUndefined();
    expect(item?.status).toBe("completed");
  });

  test("a bare turn/completed settle PRESERVES the streamed item (R1 settle-preserve semantics) - text, count, and status all survive", () => {
    const { notifications, expectedText, ref, itemId } = buildFloodStream(FLOOD_SIZE);
    let model = hydrateFloodModel(ref);
    let now = 1000;
    for (const n of notifications) {
      now += 1;
      model = applyNotification(model, n, now);
    }
    const turn = model.turns[0];
    expect(turn?.status).toBe("completed");
    // The pre-R1 bug: a bare turn/completed stamp REPLACED the turn's items
    // with the (wire-empty) stamp payload's own items, wiping the streamed
    // text entirely. This is the exact regression this test pins.
    expect(turn?.items).toHaveLength(1);
    expect(turn?.items[0]?.id).toBe(itemId);
    expect(turn?.items[0]?.text).toBe(expectedText);
  });

  test("sanity ceiling: folding 10,000 deltas sequentially completes well within a generous time budget (tripwire, not a perf gate - see tokenFlood.bench.ts for the actual measured profile)", () => {
    const { notifications, ref } = buildFloodStream(FLOOD_SIZE);
    let model = hydrateFloodModel(ref);
    let now = 1000;
    const start = performance.now();
    for (const n of notifications) {
      now += 1;
      model = applyNotification(model, n, now);
    }
    const elapsed = performance.now() - start;
    // Generous on purpose (observed fold time is documented in
    // wave4-report.md, far below this) - this exists only to catch a
    // catastrophic future regression (e.g. an accidental O(n^3)), never to
    // gate on absolute performance.
    expect(elapsed).toBeLessThan(5000);
    expect(model.turns[0]?.items[0]?.status).toBe("completed");
  });
});

const SESSION_FLOOD_SIZE = 500;

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function floodThread(ref: string): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "flood",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "active" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "serf",
    // One turn holding TWO items: a settled sibling (probe-instrumented, see
    // below) and the live agentMessage item this test floods with deltas -
    // both inside the SAME turn deliberately, since reducer.ts's
    // item/agentMessage/delta case (`mapTurn`) replaces the whole enclosing
    // TurnModel object on every delta even though only one of its items
    // actually changed (`{...turn, items: mapItem(...)}}`) - the exact
    // shape TurnBlock.tsx passes straight through to every item renderer as
    // the `turn` prop (`<ItemRenderer item={item} turn={turn} .../>`, T1's
    // own locked ItemRenderProps). A settled sibling in a DIFFERENT turn
    // would trivially never re-render (its own TurnModel reference never
    // changes) - this setup is the one that actually exercises "does a
    // live delta re-render an unrelated ALREADY-SETTLED ROW SHARING THE
    // SAME TURN," which is the realistic shape of a long multi-item turn
    // (e.g. a tool call followed by the model's streamed response).
    turns: [
      {
        id: "turn_flood_settled",
        status: "inProgress",
        itemsView: "full",
        items: [
          {
            id: "item_settled_sibling",
            turnId: "turn_flood_settled",
            type: "tokenflood-render-probe",
            text: "settled sibling content",
            status: "completed",
          },
          {
            id: "item_flood_live",
            turnId: "turn_flood_settled",
            type: "agentMessage",
            status: "inProgress",
          },
        ],
      },
    ],
    serf: { ref, capabilities: CAPABILITIES, queue: {}, activeTurnId: "turn_flood_settled" },
  };
}

// jsdom performs no real layout (every element's offsetHeight is 0, no
// ResizeObserver) - VirtualList's own suite stubs this for the same reason
// (widgets/virtuallist/virtuallist.test.tsx's file comment; Session.test.tsx
// does the same): without it, @tanstack/react-virtual sees a 0px-tall
// viewport and renders no rows at all, which wouldn't mount either item.
const CONTAINER_HEIGHT = 500;
let offsetHeightDescriptor: PropertyDescriptor | undefined;

describe("token-flood: 500-delta streaming fast path through a mounted Session", () => {
  beforeEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    resetThreadsStoreForTests();
    offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: CONTAINER_HEIGHT });
  });

  afterEach(() => {
    cleanup();
    if (offsetHeightDescriptor) {
      Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
    }
  });

  test("500 deltas on a live item do not re-render an already-settled sibling item in the same turn - render-count probe", async () => {
    // Established pattern for observing per-item render behavior:
    // TurnBlock.test.tsx registers a synthetic item type via
    // registerItemRenderer and asserts against ITS OWN render output/props
    // ("DummyRenderer"/"LiveEcho"/"TurnEcho") rather than instrumenting a
    // real production component. Mirrored here: a probe component counts
    // its own render-function invocations for the settled sibling; the
    // LIVE item renders through the real, unmodified AgentMessageItem/
    // StreamingText path (registered by SessionPane's own `import
    // "./transcript/messages"` side effect), so the flood still exercises
    // the genuine end-to-end streaming fast path this test is named for.
    //
    // Wrapped with the same `memo(Component, ignoringTurn)` treatment T5c
    // gives every registered production renderer except SystemNoticeItem
    // (types.ts's own comment) - a settled sibling registered this way is
    // exactly what a real settled row (a tool call, an agent message, a
    // think block, ...) now does.
    let renderCount = 0;
    const RenderCountProbe = memo(function RenderCountProbe(_props: ItemRenderProps) {
      renderCount += 1;
      return <div data-testid="settled-probe">settled sibling content</div>;
    }, ignoringTurn);
    registerItemRenderer("tokenflood-render-probe", RenderCountProbe);

    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    const ref = "ref_flood_session";
    fake.on("thread/read", () => ({ thread: floodThread(ref) }) as ThreadReadResponse);

    render(
      <ClientProvider client={fake}>
        <Session params={{ ref }} paneId="p1" focused={true} />
      </ClientProvider>,
    );

    await waitFor(() => expect(document.querySelector('[data-testid="settled-probe"]')).toBeTruthy());
    const renderCountAfterMount = renderCount;
    expect(renderCountAfterMount).toBeGreaterThan(0);

    const chunks = buildFloodChunks(SESSION_FLOOD_SIZE, 2);
    // Each delta emitted in its OWN act() call, not batched together -
    // faithful to the real world, where each delta arrives as its own
    // WebSocket frame producing its own separate store commit, rather than
    // React's auto-batching collapsing many synchronous updates issued in
    // one call stack into a single render pass (which would silently hide
    // exactly the per-delta re-render cost this probe exists to measure).
    for (const delta of chunks) {
      act(() => {
        fake.emitNotification({
          method: "item/agentMessage/delta",
          params: { ref, turnId: "turn_flood_settled", itemId: "item_flood_live", delta },
        } as AnyNotification);
      });
    }

    await waitFor(() =>
      expect(document.querySelector('[data-testid="streaming-text"]')?.textContent).toBe(chunks.join("")),
    );

    // The settled sibling's own DOM content must still be correct after the
    // flood regardless of how many times it re-rendered - this probe
    // separates "wasteful" from "wrong": React re-executing a component
    // function is not itself a correctness bug, only a performance one.
    expect(document.querySelector('[data-testid="settled-probe"]')?.textContent).toBe("settled sibling content");

    const rerendersDuringFlood = renderCount - renderCountAfterMount;
    // THIS IS A DOCUMENTED PIN OF CURRENTLY-OBSERVED BEHAVIOR, NOT AN
    // ASPIRATIONAL BOUND (wave-4 T5c, post-fix): TurnBlock still passes the
    // WHOLE enclosing `turn` object through to every item renderer (types.ts's
    // locked ItemRenderProps) - a `turn` prop object created fresh on every
    // mapTurn call (reducer.ts's item/agentMessage/delta case) - but the
    // settled sibling above is registered wrapped in `memo(Component,
    // ignoringTurn)`, exactly like every production renderer this wave
    // memoizes (ToolCallItem, RawItemView, and every messages/ renderer
    // except SystemNoticeItem). `ignoringTurn` compares `item` by reference
    // and `live` by value, deliberately IGNORING `turn`'s identity - the
    // settled sibling's `item` prop stays reference-stable across every
    // delta (reducer.ts's immutable-update discipline only replaces the item
    // a delta actually targets), so React bails out of re-rendering it every
    // time, regardless of how many times the enclosing turn object is
    // rebuilt. Before this fix, this same probe (unwrapped) measured exactly
    // 500 - see wave4-report.md's punch list and its T5c addendum.
    expect(rerendersDuringFlood).toBe(0);
  });
});
