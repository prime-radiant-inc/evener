import { expect, test } from "vitest";
import { appendChunk, chunkViewBackingForTests, pendingTextJoined } from "./chunkview";

// View-purity tests for chunkview.ts's immutable-append chunk view. These
// call appendChunk directly rather than through applyNotification: the
// reducer's own item/agentMessage/delta (which used to drive appendChunk on
// ItemModel.pendingText) is gone, and its read-model replacement,
// overlay/delta, folds a plain string append (`(held.item.output ?? "") +
// params.delta`) onto OverlayItem, never through chunkview.ts at all — see
// applyOverlayDelta in reducer.ts. appendChunk is therefore no longer
// exercised by any production code path (flagged in the porting task's
// report as a possible O(n^2) regression for long streams under the new
// protocol, not something this pass should silently paper over). The
// properties under test — O(1) append, a frozen view later folds cannot
// alias into, copy-on-branch — are the view's own, so calling appendChunk
// directly still proves them.

// --- O(1) per-delta accumulation --------------------------------------------
// Rationale and machinery: chunkview.ts's module header.

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
  let chunks: string[] | undefined;
  let prevBacking: string[] | undefined;
  for (let i = 0; i < N; i++) {
    chunks = appendChunk(chunks, `c${i} `);
    expect(chunks).toHaveLength(i + 1);
    const backing = chunkViewBackingForTests(chunks);
    expect(backing).toBeDefined();
    if (i === 0) {
      prevBacking = backing;
    } else {
      expect(backing).toBe(prevBacking);
      expect(backing?.length).toBe(i + 1);
    }
  }
  expect(chunks?.length).toBe(N);
  expect(chunks).toEqual(Array.from({ length: N }, (_, i) => `c${i} `));
  // And the O(1) joined-text cache agrees with a full structural join.
  expect(chunks?.join("")).toBe(pendingTextJoined(chunks ?? []));
});

test("a mid-stream chunk view stays observationally frozen while later appends continue folding (view purity)", () => {
  let chunks = appendChunk(appendChunk(undefined, "Hel"), "lo");
  const frozen = chunks;
  const snapshot = [...frozen];
  // Snapshot the JOIN too — the most common read (settleItem, renderers).
  const joined = frozen.join("");
  // Keep folding well past the snapshotted state.
  for (let i = 0; i < 500; i++) {
    chunks = appendChunk(chunks, "x");
  }
  expect(frozen.length).toBe(2);
  expect([...frozen]).toEqual(snapshot);
  expect(frozen.join("")).toBe(joined);
  // The shared reader agrees: a stale view is not the backing's newest, so
  // pendingTextJoined falls back to a structural join over the frozen prefix
  // — it must not read the advanced backing.
  expect(pendingTextJoined(frozen)).toBe(joined);
  expect(pendingTextJoined(frozen)).toBe("Hello");
  // And the live view carries all 502 chunks, first two unchanged.
  expect(chunks.length).toBe(502);
  expect(chunks.slice(0, 2)).toEqual(["Hel", "lo"]);
  // Mutating traps throw rather than corrupting the shared backing.
  expect(() => (frozen as string[]).push("y")).toThrow();
  expect(() => {
    (frozen as string[])[0] = "z";
  }).toThrow();
  // Joining the full fold still reads exactly the streamed text.
  expect(pendingTextJoined(chunks)).toBe(`Hello${"x".repeat(500)}`);
});

test("a delta folded onto a STALE mid-stream state branches cleanly — no aliasing into the newer fold (copy-on-branch)", () => {
  const branchedAt = appendChunk(appendChunk(undefined, "a"), "b");

  // One line of history continues from branchedAt...
  let live = branchedAt;
  for (let i = 0; i < 100; i++) {
    live = appendChunk(live, "L");
  }
  expect(live.length).toBe(102);
  expect(live.join("")).toBe(`ab${"L".repeat(100)}`);

  // ...and a second fold from the SAME stale state takes its own branch.
  // The stale state's view is not the backing's newest, so this append
  // must NOT push into the backing the live branch reads — it copies.
  let fork = branchedAt;
  for (let i = 0; i < 100; i++) {
    fork = appendChunk(fork, "F");
  }
  expect(fork.length).toBe(102);
  expect(fork.join("")).toBe(`ab${"F".repeat(100)}`);
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
