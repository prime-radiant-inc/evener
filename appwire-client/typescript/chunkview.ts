// chunkview.ts -- the immutable-append chunk view behind streaming-item
// accumulation.
//
// The reducer's streaming-delta path needs two things at once: a per-item
// pendingText value that reads as a plain string[] to every consumer (React
// memo comparators, expect(...).toEqual, spread, JSON.stringify) and a
// genuinely O(1) append that never copies an earlier chunk. This module owns
// that data structure, so the trap machinery and its view-purity tests live
// together.
//
// Descriptor asymmetry (deliberate): getOwnPropertyDescriptor reports
// `writable: true, configurable: true` for a present index and
// `writable: true, configurable: false` for "length" -- the descriptors a
// plain string[] carries -- so toEqual cannot distinguish a view from an
// array. Immutability is enforced by the set/deleteProperty/defineProperty
// and setPrototypeOf traps THROWING, not by the writable flag;
// Object.freeze(view) throws too, via the preventExtensions trap, rather than
// silently freezing a target that carries no real elements, and
// Object.setPrototypeOf(view, ...) throws rather than stripping
// Array.prototype from the shared target (which would delete every array
// method from the view while leaving its descriptors "intact").
//

// Streaming-delta chunk accumulation, in O(1) per delta.
//
// The public shape is ItemModel.pendingText: string[] holding every chunk in
// arrival order, readable at any time mid-stream. Producing the next state
// must not copy that array: rebuilding it per delta (pendingText:
// [...(item.pendingText ?? []), delta]) is O(current-length) work per delta
// and O(n^2) over an item streamed in n chunks, which dominates a long
// streaming turn.
//
// Shape: a per-item append-only backing array plus, per fold state, an
// IMMUTABLE fixed-length view of that backing (a Proxy over an empty array
// target that presents backing[0..length) — real Array.prototype methods
// work on it, Array.isArray is true, and every mutating trap throws). A
// delta appends the chunk to the backing (O(1)) and mints a fresh view one
// longer (O(1)) — no copy of any earlier chunk ever happens.
// The view's brand also carries the running JOINED text (`brand.text`,
// maintained per append), so the hot readers of a live view (settleItem,
// AgentMessageItem's per-render markdown source) get the full text in O(1)
// instead of paying the per-element Proxy-trap cost of a join.
// Maintaining that join is not where the quadratic cost would be, though:
// `brand.text + delta` is the same flat-per-delta concatenation
// item/toolOutput/delta's `output` uses — a rope on V8, a buffered primitive
// on Hermes — so the array copy, not the string concat, is the quadratic
// shape appendChunk avoids.
//
// Purity: the reducer's contract is immutable updates, and this preserves
// it OBSERVATIONALLY. The one deliberate alias — new views share the
// backing with the view they extend — is safe because a chunk is only ever
// appended at the index one past the current view's fixed length: no view
// already handed out can read that index (its length is frozen), so every
// view ever returned reads the exact same bytes for its whole life. A fold
// whose chunk history diverges from the backing's (a delta arriving on a
// model state whose pendingText is not the backing's latest view — the
// replay/branch/reset interleavings that would otherwise alias a future
// push into an old view) takes copyChunkPrefix instead, detaching from the
// shared backing entirely. So no output of applyNotification is ever
// mutated by a later applyNotification — same inputs, same observable
// outputs, which is the purity the reducer's file header promises.
//
// Why a Proxy view rather than "mutate the item's array in place": the
// model is handed to React and tests that treat it as deeply immutable
// (memo comparators, hydrate-vs-fold snapshots, expect(...).toEqual). A
// bare mutable array would change observable content under an existing
// reference after the fact, which is exactly the purity violation this
// design exists to avoid; the view keeps each state's snapshot frozen.

// Brands a view so appendChunk can recognize its own kind. Stored as a
// non-enumerable symbol-keyed prop that ownKeys does not report, keeping
// the view structurally identical to a plain string[] for every consumer
// (toEqual, JSON.stringify, spread, iteration) while appendChunk still has
// a way to check "is this one of mine" without an O(1)-breaking lookup.
const CHUNK_VIEW = Symbol("evener.chunkView");

// The per-view brand, carried ON THE PROXY TARGET as a non-enumerable
// symbol-keyed prop (so the traps — one shared module-level handler, not a
// fresh closure set per view — can read it with Reflect.get on the raw
// target). `text` is the cached join of backing[0..length); it is only
// current while the view is the backing's newest (length ===
// backing.length), which is exactly when the O(1) readers use it.
interface ChunkViewBrand {
  backing: string[];
  length: number;
  text: string;
}

type ChunkView = string[] & { [CHUNK_VIEW]?: ChunkViewBrand };

// "5" | "12" -> 5 | 12; anything else (including "length", symbols,
// negatives, fractions) -> undefined. Canonical numeric-string form only.
function chunkIndex(prop: string): number | undefined {
  const n = Number(prop);
  return Number.isInteger(n) && n >= 0 && String(n) === prop ? n : undefined;
}

// A view's brand, in ONE trap hit (a view created by chunkView answers
// here; anything else — a plain array from tests or hydrate — returns
// undefined). Callers that need both the backing and the length read them
// off this single result rather than paying the get trap twice.
function chunkBrand(chunks: string[]): ChunkViewBrand | undefined {
  return (chunks as ChunkView)[CHUNK_VIEW];
}

// Every view shares this ONE handler (module-level, not minted per view):
// the per-view state it needs — the brand — rides on the target, where the
// traps read it with Reflect.get on the raw target (no closure capture, no
// per-delta handler allocation). Every trap mirrors the exact descriptor
// semantics of a real Array of that length so structural equality with a
// plain string[] holds.
const CHUNK_VIEW_HANDLER: ProxyHandler<string[]> = {
  get(t, prop, receiver) {
    const brand = Reflect.get(t, CHUNK_VIEW) as ChunkViewBrand | undefined;
    if (prop === CHUNK_VIEW) return brand;
    if (prop === "length") return brand?.length;
    if (typeof prop === "string" && brand !== undefined) {
      const i = chunkIndex(prop);
      if (i !== undefined) return i < brand.length ? brand.backing[i] : undefined;
    }
    return Reflect.get(t, prop, receiver);
  },
  has(t, prop) {
    if (prop === CHUNK_VIEW) return false;
    const brand = Reflect.get(t, CHUNK_VIEW) as ChunkViewBrand | undefined;
    if (typeof prop === "string" && brand !== undefined) {
      const i = chunkIndex(prop);
      if (i !== undefined) return i < brand.length;
    }
    return Reflect.has(t, prop);
  },
  ownKeys(t) {
    const brand = Reflect.get(t, CHUNK_VIEW) as ChunkViewBrand | undefined;
    const length = brand?.length ?? 0;
    const keys: string[] = [];
    for (let i = 0; i < length; i++) keys.push(String(i));
    keys.push("length");
    return keys;
  },
  getOwnPropertyDescriptor(t, prop) {
    if (prop === CHUNK_VIEW) return undefined;
    const brand = Reflect.get(t, CHUNK_VIEW) as ChunkViewBrand | undefined;
    if (brand !== undefined) {
      if (prop === "length") return { value: brand.length, writable: true, enumerable: false, configurable: false };
      if (typeof prop === "string") {
        const i = chunkIndex(prop);
        if (i !== undefined) {
          if (i >= brand.length) return undefined;
          return { value: brand.backing[i], writable: true, enumerable: true, configurable: true };
        }
      }
    }
    return Reflect.getOwnPropertyDescriptor(t, prop);
  },
  set() {
    throw new TypeError("pendingText views are immutable (append via the reducer)");
  },
  deleteProperty() {
    throw new TypeError("pendingText views are immutable (append via the reducer)");
  },
  defineProperty() {
    throw new TypeError("pendingText views are immutable (append via the reducer)");
  },
  preventExtensions() {
    throw new TypeError("pendingText views are immutable (append via the reducer)");
  },
  setPrototypeOf() {
    throw new TypeError("pendingText views are immutable (append via the reducer)");
  },
};

// Returns a fresh immutable view of backing[0..length). `text` (the cached
// join of that prefix) may be passed by a caller that already knows it —
// the append fast path does, saving the O(length) recompute.
function chunkView(backing: string[], length: number, text?: string): string[] {
  // A zero-length view has nothing to protect; an empty plain array is
  // cheaper than a Proxy and toEqual-identical. Also keeps RawItemView's
  // `chunks.length === 0` and AgentMessageItem's empty-array fallback on
  // their existing code paths.
  if (length === 0) return [];
  const brand: ChunkViewBrand = { backing, length, text: text ?? backing.slice(0, length).join("") };
  const target: string[] = [];
  // configurable (not frozen) is REQUIRED by the Proxy invariants: a
  // non-configurable target prop would force ownKeys to report the symbol,
  // breaking toEqual/spread/JSON structural invisibility. Configurable, the
  // traps above may hide it from ownKeys, which is what keeps the symbol
  // structurally invisible.
  Object.defineProperty(target, CHUNK_VIEW, { value: brand, enumerable: false, writable: true, configurable: true });
  return new Proxy(target, CHUNK_VIEW_HANDLER);
}

// Appends `delta` to `item.pendingText`, O(1). Fast path: the item's view
// is the newest over its backing (the plain sequential-stream case, by far
// the common one) — push and mint. Slow path (copy-on-branch): the item's
// view is stale (a folded state that predates later pushes on the same
// backing) or a plain array (hydrated/test-constructed). Copying is the
// only correct option there: pushing onto the old view's backing would
// overwrite a chunk some other view already reads, and pushing onto a plain
// array would mutate an array the caller may still hold.
export function appendChunk(current: string[] | undefined, delta: string): string[] {
  if (current === undefined) return copyChunkPrefix([], delta);
  const brand = chunkBrand(current);
  if (brand !== undefined && brand.length === brand.backing.length) {
    brand.backing.push(delta);
    return chunkView(brand.backing, brand.backing.length, brand.text + delta);
  }
  return copyChunkPrefix(current, delta);
}

// The joined text of a pendingText value, O(1) for a live view (the brand
// caches it per append) and a plain join for anything else (a plain array
// from tests or hydrate). THE one read both hot consumers — settleItem's
// finalize and AgentMessageItem's per-render markdown source — go through,
// so the O(1) brand-cache read lives in exactly one place.
export function pendingTextJoined(chunks: string[]): string {
  const brand = chunkBrand(chunks);
  if (brand !== undefined && brand.length === brand.backing.length) return brand.text;
  return chunks.join("");
}

// Test-only white-box accessor: the backing array a view reads (undefined
// for a plain array). The O(1) test discriminates append from copy by
// asserting this reference is IDENTICAL across consecutive folds — the one
// property a per-delta copy cannot fake (chunk strings are primitives, so
// element-level Object.is survives a copy).
export function chunkViewBackingForTests(chunks: string[]): string[] | undefined {
  return chunkBrand(chunks)?.backing;
}

// Detached append: copies `chunks` (a plain array or a view) and appends.
// O(length) — only ever reached when the item's chunk history has already
// diverged from any shared backing, so the total work across a whole
// divergent replay is still O(n) for n deltas (one copy per branch point,
// not per delta).
function copyChunkPrefix(chunks: string[], delta: string): string[] {
  const backing = [...chunks, delta];
  return chunkView(backing, backing.length);
}
