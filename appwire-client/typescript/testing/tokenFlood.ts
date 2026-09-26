// Shared harness for the wave-4 token-flood benchmark (wave plan's binding
// gate: "recorded 10k-delta stream replayed through the store; frame budget
// documented, no dropped-chunk correctness failures" -
// docs/superpowers/plans/2026-07-20-webui-rewrite-wave4-transcript.md).
// Not itself a test/bench file - imported by both tokenFlood.test.ts
// (correctness assertions, part of `vitest run`) and tokenFlood.bench.ts
// (timing profile, `vitest bench` only), mirroring this directory's existing
// fakeClient.ts/fakeSocket.ts precedent for shared non-test test
// infrastructure.
import type { ThreadModel } from "../model";
import { applyNotification, hydrateThread } from "../reducer";
import type { AnyNotification, OverlayItem, Thread, ThreadCapabilities, ThreadReadResponse } from "../types.gen";

// mulberry32: a small, fast, deterministic PRNG (public-domain algorithm) -
// used only for chunk-length variety, never for anything security-sensitive.
// Deterministic so the correctness assertions below are 100% reproducible
// (a real dropped/reordered chunk must never depend on which random run
// happened to expose it).
export function mulberry32(seed: number): () => number {
  let a = seed;
  return () => {
    a |= 0;
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// A small representative vocabulary - long enough, repeated, to source
// chunks for any requested count. Content realism matters far less here
// than SIZE realism (the actual gate constraint); this reads as plausible
// streamed assistant prose without needing an embedded corpus.
const WORD_BANK = [
  "the",
  "answer",
  "is",
  "because",
  "when",
  "we",
  "consider",
  "this",
  "approach",
  "and",
  "checking",
  "the",
  "math",
  "again",
  "the",
  "result",
  "turns",
  "out",
  "to",
  "be",
  "forty",
  "two",
  "after",
  "reviewing",
  "the",
  "full",
  "context",
  "carefully",
  "one",
  "more",
  "time",
  "before",
  "responding",
  "with",
  "confidence",
  "and",
  "clarity",
  "for",
  "the",
  "user",
  "who",
  "asked",
  "a",
  "clear",
  "question",
  "about",
  "numbers",
];

// Builds `count` chunks whose lengths are uniform-random in [2,40] - the
// live-proof fixtures' own observed range (appwire-client/typescript/fixtures/*.jsonl:
// basic-turn 3-5 chars, streaming-with-reset 1-23 chars). The source text is
// generated long enough (count*40, the worst case if every chunk hit the
// max) that slicing never runs out, so `chunks.join("")` is guaranteed to
// equal a clean prefix of `source` - correct by construction, not by luck.
export function buildFloodChunks(count: number, seed = 1): string[] {
  const rand = mulberry32(seed);
  let source = "";
  let wi = 0;
  while (source.length < count * 40) {
    source += (wi > 0 ? " " : "") + WORD_BANK[wi % WORD_BANK.length];
    wi += 1;
  }
  const chunks: string[] = [];
  let pos = 0;
  for (let c = 0; c < count; c += 1) {
    const len = 2 + Math.floor(rand() * 39); // uniform 2..40 inclusive
    chunks.push(source.slice(pos, pos + len));
    pos += len;
  }
  return chunks;
}

export interface FloodStream {
  notifications: AnyNotification[];
  expectedText: string;
  chunkCount: number;
  ref: string;
  threadId: string;
  turnId: string;
  itemId: string;
}

// Builds the exact wire-shaped stream the wave plan's gate names, ported to
// the read model: history/updated (opens the turn) -> overlay/upserted
// (starts the stream, reducer.history.test.ts's own streamOverlay
// convention: key "stream:<roundId>/<attempt>:agentMessage") -> `count`
// overlay/delta notifications (item/agentMessage/delta's read-model
// replacement) -> overlay/end (closes the round) -> history/updated
// (settling the turn AND the item with the server's own authoritative final
// text, matching real wire behavior - see streaming-with-reset.jsonl line
// 15, where item/completed's item.text is exactly the concatenation of the
// deltas that preceded it).
export function buildFloodStream(count: number, seed = 1): FloodStream {
  const threadId = "thr_flood";
  const ref = "ref_flood";
  const turnId = "turn_flood";
  const itemId = "item_flood";
  const roundId = "round_flood";
  const streamId = `${roundId}/0`;
  const overlayKey = `stream:${streamId}:agentMessage`;
  const chunks = buildFloodChunks(count, seed);
  const expectedText = chunks.join("");

  const streamOverlayItem: OverlayItem = {
    key: overlayKey,
    kind: "stream",
    turnId,
    roundId,
    streamId,
    item: { type: "agentMessage", id: itemId, turnId, roundId, status: "inProgress" },
  };

  const notifications: AnyNotification[] = [
    {
      method: "history/updated",
      params: {
        threadId,
        ref,
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc_flood", length: 1 },
        turns: [{ id: turnId, status: "inProgress", itemsView: "" }],
      },
    } as AnyNotification,
    {
      method: "overlay/upserted",
      params: { threadId, ref, item: streamOverlayItem },
    } as AnyNotification,
    ...chunks.map(
      (delta) =>
        ({
          method: "overlay/delta",
          params: { threadId, ref, key: overlayKey, field: "text", delta },
        }) as AnyNotification,
    ),
    {
      method: "overlay/end",
      params: { threadId, ref, roundId },
    } as AnyNotification,
    {
      method: "history/updated",
      params: {
        threadId,
        ref,
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc_flood", length: 2 },
        turns: [{ id: turnId, status: "completed", itemsView: "" }],
        items: [{ type: "agentMessage", id: itemId, turnId, roundId, text: expectedText, status: "completed" }],
      },
    } as AnyNotification,
  ];

  return { notifications, expectedText, chunkCount: count, ref, threadId, turnId, itemId };
}

// A model hydrated and folded up to (and including) overlay/upserted for one
// in-flight agentMessage stream — the canonical "streaming item" scaffold the
// token-flood stream (buildFloodStream above) opens with. `ids` overrides the
// flood defaults so callers can pin their own thread/ref/turn/item identity;
// determinism is by construction (no randomness anywhere).
export function hydrateStreamingAgentMessage(
  ref: string,
  ids?: { threadId?: string; turnId?: string; itemId?: string },
): ThreadModel {
  const threadId = ids?.threadId ?? `thr_${ref}`;
  const turnId = ids?.turnId ?? "turn_1";
  const itemId = ids?.itemId ?? "item_1";
  const roundId = `${turnId}_round`;
  let model = hydrateFloodModel(ref);
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId,
        ref,
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc_flood", length: 1 },
        turns: [{ id: turnId, status: "inProgress", itemsView: "" }],
      },
    } as AnyNotification,
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "overlay/upserted",
      params: {
        threadId,
        ref,
        item: {
          key: `stream:${roundId}/0:agentMessage`,
          kind: "stream",
          turnId,
          roundId,
          streamId: `${roundId}/0`,
          item: { type: "agentMessage", id: itemId, turnId, roundId, status: "inProgress" },
        },
      },
    } as AnyNotification,
    1002,
  );
  return model;
}

const CAPABILITIES: ThreadCapabilities = {
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
  sharedNotes: true,
  rename: true,
};

export interface FoldTimingResult {
  model: ThreadModel;
  /** One entry per overlay/delta notification, in stream order. */
  perDeltaMs: number[];
  /** Wall time for the WHOLE fold (every notification, not just deltas). */
  totalMs: number;
}

// Folds `notifications` through applyNotification sequentially, timing each
// individual overlay/delta application (item/agentMessage/delta's read-model
// replacement) with performance.now() - the primitive both
// tokenFlood.test.tsx's sanity-ceiling test and tokenFlood.bench.ts's
// growth-curve profile are built from.
export function foldWithTiming(model: ThreadModel, notifications: AnyNotification[]): FoldTimingResult {
  const perDeltaMs: number[] = [];
  let m = model;
  let now = 1000;
  const start = performance.now();
  for (const n of notifications) {
    now += 1;
    if (n.method === "overlay/delta") {
      const t0 = performance.now();
      m = applyNotification(m, n, now);
      perDeltaMs.push(performance.now() - t0);
    } else {
      m = applyNotification(m, n, now);
    }
  }
  const totalMs = performance.now() - start;
  return { model: m, perDeltaMs, totalMs };
}

// A fresh, empty hydrated model for `ref` - mirrors reducer.test.ts's own
// testHydrate() shape (kept local rather than imported: that file is a test
// file, not shared infrastructure, and this harness's Thread fixture only
// needs a handful of the same required fields).
export function hydrateFloodModel(ref: string): ThreadModel {
  const thread: Thread = {
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
    source: "evener",
    evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
  };
  const resp: ThreadReadResponse = { thread };
  return hydrateThread(resp, ref, 1000);
}

export function mean(xs: number[]): number {
  if (xs.length === 0) return 0;
  return xs.reduce((a, b) => a + b, 0) / xs.length;
}

export function percentile(xs: number[], p: number): number {
  if (xs.length === 0) return 0;
  const sorted = [...xs].sort((a, b) => a - b);
  const idx = Math.min(sorted.length - 1, Math.floor((p / 100) * sorted.length));
  return sorted[idx] ?? 0;
}
