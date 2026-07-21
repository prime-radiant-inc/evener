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
import type { AnyNotification, Thread, ThreadCapabilities, ThreadReadResponse } from "../types.gen";

// mulberry32: a small, fast, deterministic PRNG (public-domain algorithm) -
// used only for chunk-length variety, never for anything security-sensitive.
// Deterministic so the correctness assertions below are 100% reproducible
// (a real dropped/reordered chunk must never depend on which random run
// happened to expose it).
function mulberry32(seed: number): () => number {
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
// live-proof fixtures' own observed range (src/protocol/fixtures/*.jsonl:
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

// Builds the exact wire-shaped stream the wave plan's gate names: turn/
// started -> item/started -> `count` item/agentMessage/delta notifications
// -> item/completed (carrying the server's own authoritative final text,
// matching real wire behavior - see streaming-with-reset.jsonl line 15,
// where item/completed's item.text is exactly the concatenation of the
// deltas that preceded it) -> a BARE turn/completed stamp (no items,
// itemsView "" - the live wire's real settle shape; reducer.ts's own
// "turn/completed" case comment, and R1's fix, both establish that a
// non-"full" itemsView means "this payload has nothing to say about items,"
// preserving whatever the model already accumulated rather than replacing
// it with an empty list).
export function buildFloodStream(count: number, seed = 1): FloodStream {
  const threadId = "thr_flood";
  const ref = "ref_flood";
  const turnId = "turn_flood";
  const itemId = "item_flood";
  const chunks = buildFloodChunks(count, seed);
  const expectedText = chunks.join("");

  const notifications: AnyNotification[] = [
    {
      method: "turn/started",
      params: { threadId, ref, turn: { id: turnId, status: "inProgress", itemsView: "" } },
    } as AnyNotification,
    {
      method: "item/started",
      params: { threadId, ref, turnId, item: { type: "agentMessage", id: itemId, turnId, status: "inProgress" } },
    } as AnyNotification,
    ...chunks.map(
      (delta) =>
        ({
          method: "item/agentMessage/delta",
          params: { threadId, ref, turnId, itemId, delta },
        }) as AnyNotification,
    ),
    {
      method: "item/completed",
      params: {
        threadId,
        ref,
        turnId,
        item: { type: "agentMessage", id: itemId, turnId, text: expectedText, status: "completed" },
      },
    } as AnyNotification,
    {
      method: "turn/completed",
      params: { turnId, turn: { id: turnId, status: "completed", itemsView: "" } },
    } as AnyNotification,
  ];

  return { notifications, expectedText, chunkCount: count, ref, threadId, turnId, itemId };
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
  queue: true,
  goal: true,
  rename: true,
};

export interface FoldTimingResult {
  model: ThreadModel;
  /** One entry per item/agentMessage/delta notification, in stream order. */
  perDeltaMs: number[];
  /** Wall time for the WHOLE fold (every notification, not just deltas). */
  totalMs: number;
}

// Folds `notifications` through applyNotification sequentially, timing each
// individual item/agentMessage/delta application with performance.now() -
// the primitive both tokenFlood.test.tsx's sanity-ceiling test and
// tokenFlood.bench.ts's growth-curve profile are built from.
export function foldWithTiming(model: ThreadModel, notifications: AnyNotification[]): FoldTimingResult {
  const perDeltaMs: number[] = [];
  let m = model;
  let now = 1000;
  const start = performance.now();
  for (const n of notifications) {
    now += 1;
    if (n.method === "item/agentMessage/delta") {
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
    source: "serf",
    serf: { ref, capabilities: CAPABILITIES, queue: {} },
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
