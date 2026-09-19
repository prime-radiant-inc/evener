import { expect, test } from "vitest";
import { appendChunk, chunkViewBackingForTests, pendingTextJoined } from "./chunkview";
import type { ThreadModel } from "./model";
import { applyNotification } from "./reducer";
import { itemAt, turnAt } from "./testing/modelAccessors";
import { hydrateStreamingAgentMessage } from "./testing/tokenFlood";
import type { AnyNotification } from "./types.gen";

// View-purity tests for chunkview.ts's immutable-append chunk view, colocated
// with the module that owns the machinery. The reducer is exercised only as
// the producer of views (applyNotification folds item/agentMessage/delta
// through appendChunk) — the properties under test are the view's own: a
// genuinely O(1) append, a frozen view that later folds cannot alias into, and
// copy-on-branch when a fold diverges from the backing.

// --- O(1) per-delta accumulation --------------------------------------------
// Rationale and machinery: chunkview.ts's module header.

// The shared streaming-agentMessage scaffold (appwire-client/typescript/
// testing/tokenFlood.ts) on this suite's thr_t/ref_t/turn_1/item_1 identity.
function streamingItem(): ThreadModel {
  return hydrateStreamingAgentMessage("ref_t", { threadId: "thr_t" });
}

function agentMessageDelta(delta: string): AnyNotification {
  return {
    method: "item/agentMessage/delta",
    params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1", delta },
  };
}

// The O(1)-append test's ceiling is a tripwire for a hang, not a
// responsiveness bar: its 20,000-delta fold measures ~0.4s in isolation and
// in-suite, so this ceiling sits far above the work while staying bounded.
// Sized like hookTimeout/WARM_ROUTE_TRIPWIRE_MS, and a regression to O(n^2)
// blows through it regardless (a copy per delta is ~200M string copies at
// N=20,000).
const O1_APPEND_TRIPWIRE_MS = 30_000;

test("every delta's fold appends onto the SAME backing array, which grows by exactly one (O(1) append)", {
  timeout: O1_APPEND_TRIPWIRE_MS,
}, () => {
  // White-box on purpose (chunkViewBackingForTests): chunk strings are
  // PRIMITIVES, so element-level identity checks survive a per-delta copy
  // ([...chunks, delta] preserves every string reference) — only the
  // BACKING ARRAY reference distinguishes a true append from a copy: a
  // copy mints a fresh backing per delta, an append returns the same
  // array one longer. Each iteration therefore asserts (a) the backing
  // reference is IDENTICAL to the previous fold's and (b) its length grew
  // by exactly one, keeping the test O(n) — it must not recreate the very
  // blowup it guards against. Rationale: chunkview.ts's header.
  const N = 20_000;
  let model = streamingItem();
  let prevBacking: string[] | undefined;
  for (let i = 0; i < N; i++) {
    model = applyNotification(model, agentMessageDelta(`c${i} `), 1003 + i);
    const chunks = itemAt(turnAt(model, 0), 0).pendingText;
    expect(chunks).toHaveLength(i + 1);
    const backing = chunkViewBackingForTests(chunks ?? []);
    expect(backing).toBeDefined();
    if (i === 0) {
      prevBacking = backing;
    } else {
      expect(backing).toBe(prevBacking);
      expect(backing?.length).toBe(i + 1);
    }
  }
  const chunks = itemAt(turnAt(model, 0), 0).pendingText;
  expect(chunks?.length).toBe(N);
  expect(chunks).toEqual(Array.from({ length: N }, (_, i) => `c${i} `));
  // And the O(1) joined-text cache agrees with a full structural join.
  expect(chunks?.join("")).toBe(pendingTextJoined(chunks ?? []));
});

test("a mid-stream model state stays observationally frozen while later deltas continue folding (view purity)", () => {
  let model = streamingItem();
  model = applyNotification(model, agentMessageDelta("Hel"), 1003);
  model = applyNotification(model, agentMessageDelta("lo"), 1004);
  const frozen = itemAt(turnAt(model, 0), 0);
  const snapshot = [...(frozen.pendingText ?? [])];
  // Snapshot the JOIN too — the most common read (settleItem, renderers).
  const joined = frozen.pendingText?.join("");
  // Keep folding well past the snapshotted state.
  for (let i = 0; i < 500; i++) {
    model = applyNotification(model, agentMessageDelta("x"), 1005 + i);
  }
  expect(frozen.pendingText?.length).toBe(2);
  expect([...(frozen.pendingText ?? [])]).toEqual(snapshot);
  expect(frozen.pendingText?.join("")).toBe(joined);
  // The shared reader agrees: a stale view is not the backing's newest, so
  // pendingTextJoined falls back to a structural join over the frozen prefix
  // — it must not read the advanced backing.
  expect(pendingTextJoined(frozen.pendingText ?? [])).toBe(joined);
  expect(pendingTextJoined(frozen.pendingText ?? [])).toBe("Hello");
  // And the live item carries all 502 chunks, first two unchanged.
  const live = itemAt(turnAt(model, 0), 0);
  expect(live.pendingText?.length).toBe(502);
  expect(live.pendingText?.slice(0, 2)).toEqual(["Hel", "lo"]);
  // Mutating traps throw rather than corrupting the shared backing.
  expect(() => (live.pendingText as string[]).push("y")).toThrow();
  expect(() => {
    (live.pendingText as string[])[0] = "z";
  }).toThrow();
  // Settling after the fold still joins exactly the streamed text.
  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turn: { id: "turn_1", status: "completed", itemsView: "" },
      },
    },
    2000,
  );
  const settled = itemAt(turnAt(model, 0), 0);
  expect(settled.text).toBe(`Hello${"x".repeat(500)}`);
  expect(settled.pendingText).toBeUndefined();
});

test("a delta folded onto a STALE mid-stream state branches cleanly — no aliasing into the newer fold (copy-on-branch)", () => {
  let base = streamingItem();
  base = applyNotification(base, agentMessageDelta("a"), 1003);
  base = applyNotification(base, agentMessageDelta("b"), 1004);
  const branchedAt = base;

  // One line of history continues from branchedAt...
  let live = branchedAt;
  for (let i = 0; i < 100; i++) {
    live = applyNotification(live, agentMessageDelta("L"), 1100 + i);
  }
  const liveChunks = itemAt(turnAt(live, 0), 0).pendingText;
  expect(liveChunks?.length).toBe(102);
  expect(liveChunks?.join("")).toBe(`ab${"L".repeat(100)}`);

  // ...and a second fold from the SAME stale state takes its own branch.
  // The stale state's view is not the backing's newest, so this append
  // must NOT push into the backing the live branch reads — it copies.
  let fork = branchedAt;
  for (let i = 0; i < 100; i++) {
    fork = applyNotification(fork, agentMessageDelta("F"), 1200 + i);
  }
  const forkChunks = itemAt(turnAt(fork, 0), 0).pendingText;
  expect(forkChunks?.length).toBe(102);
  expect(forkChunks?.join("")).toBe(`ab${"F".repeat(100)}`);
});

// The descriptor asymmetry the module header documents: getOwnPropertyDescriptor
// reports plain-array descriptors (so expect(...).toEqual holds structurally)
// while the mutating traps, including preventExtensions behind Object.freeze,
// still THROW. A view must never be mutable just because its descriptors say
// writable, and must never be indistinguishable from an array at the
// descriptor level — both directions are pinned here.
test("a view reports plain-array property descriptors while every mutating trap still throws", () => {
  let view = appendChunk(undefined, "a");
  view = appendChunk(view, "b");
  expect(Array.isArray(view)).toBe(true);
  expect(view).toEqual(["a", "b"]);
  expect(view).not.toBe(["a", "b"]);
  expect(Object.getOwnPropertyDescriptor(view, "0")).toEqual({
    value: "a",
    writable: true,
    enumerable: true,
    configurable: true,
  });
  expect(Object.getOwnPropertyDescriptor(view, "length")).toEqual({
    value: 2,
    writable: true,
    enumerable: false,
    configurable: false,
  });
  expect(Object.getOwnPropertyDescriptor(view, "2")).toBeUndefined();
  expect(() => (view as string[]).push("c")).toThrow();
  expect(() => Object.defineProperty(view, "2", { value: "c", enumerable: true })).toThrow();
  expect(() => {
    delete (view as unknown as Record<string, unknown>)[0];
  }).toThrow();
  expect(() => Object.freeze(view)).toThrow();
  // The traps threw, so the underlying backing is untouched and the view still
  // reads its own prefix even after later appends to the same backing chain.
  const later = appendChunk(view, "c");
  expect(view).toEqual(["a", "b"]);
  expect(later).toEqual(["a", "b", "c"]);
  expect(chunkViewBackingForTests(later)).toBe(chunkViewBackingForTests(view));
});

// The other half of the mutation story. The target stays extensible (the
// ownKeys invariant forbids preventing extensions on it), so without a
// setPrototypeOf trap Object.setPrototypeOf(view, null) would strip
// Array.prototype off the shared target and delete every array method from
// the view. The trap must throw, and array behaviour must survive.
test("an attempted prototype change throws and leaves array behaviour intact", () => {
  const view = appendChunk(appendChunk(undefined, "a"), "b");
  expect(Object.getPrototypeOf(view)).toBe(Array.prototype);
  expect(() => Object.setPrototypeOf(view, null)).toThrow();
  expect(() => Reflect.setPrototypeOf(view, Object.prototype)).toThrow();
  // The trap threw, so the prototype is untouched and the view still walks,
  // joins and spreads exactly like the plain string[] it presents as.
  expect(Object.getPrototypeOf(view)).toBe(Array.prototype);
  expect(view.join("")).toBe("ab");
  expect(view.slice(1)).toEqual(["b"]);
  expect([...view]).toEqual(["a", "b"]);
});

// appendChunk onto a plain array (a hydrated/test-constructed value, never the
// backing's newest view) must copy rather than mutate the caller's array.
test("appending onto a plain array detaches into a fresh backing (copy-on-branch)", () => {
  const plain = ["x", "y"];
  const view = appendChunk(plain, "z");
  expect(plain).toEqual(["x", "y"]);
  expect(view).toEqual(["x", "y", "z"]);
  expect(chunkViewBackingForTests(view)).not.toBe(plain);
  expect(pendingTextJoined(view)).toBe("xyz");
});
