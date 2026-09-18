// Wave-4 token-flood benchmark: timing half. The correctness half
// (tokenFlood.test.tsx) is part of `vitest run` and asserts the wave plan's
// "no dropped-chunk correctness failures" gate; THIS file measures and
// reports (never asserts - the brief's own framing) the frame-budget half:
// total fold time, mean/p99 per-delta, and the growth curve (a true O(n^2)
// accumulation shows up as a super-linear late/early cost ratio). One case
// (issue #1560) additionally READS the accumulated text after every delta,
// because the fold cases alone never touch it and so hid the read-side cost.
//
// `.bench.ts` files are NOT part of `vitest run`'s default include pattern
// - only `npx vitest bench` discovers them - so this file's console output
// never touches the gate battery's pristine-output requirement. Run it
// manually to reproduce the numbers transcribed into
// docs/superpowers/plans/wave4-report.md.
import { bench, describe } from "vitest";
import { pendingTextJoined } from "./chunkview";
import { applyNotification } from "./reducer";
import { buildFloodStream, foldWithTiming, hydrateFloodModel, mean, percentile } from "./testing/tokenFlood";
import type { AnyNotification } from "./types.gen";

// vitest's own bench reporter gives hz/mean/p75/p99/p995/p999 ACROSS REPEATED
// WHOLE-RUN invocations of each case - comparing these three cases' own
// reported mean time is a second, framework-native way to see the growth
// curve (roughly 5x/10x mean-time growth from 1000->5000->10000 is linear;
// ~25x/~100x would be quadratic).
describe("token-flood: fold time by size (vitest bench's own hz/mean/p99)", () => {
  bench("fold 1,000 deltas", () => {
    const { notifications, ref } = buildFloodStream(1_000, 7);
    const model = hydrateFloodModel(ref);
    foldWithTiming(model, notifications);
  });

  bench("fold 5,000 deltas", () => {
    const { notifications, ref } = buildFloodStream(5_000, 7);
    const model = hydrateFloodModel(ref);
    foldWithTiming(model, notifications);
  });

  bench("fold 10,000 deltas", () => {
    const { notifications, ref } = buildFloodStream(10_000, 7);
    const model = hydrateFloodModel(ref);
    foldWithTiming(model, notifications);
  });
});

// This second block computes and prints the PER-DELTA distribution and the
// WITHIN-ONE-RUN growth curve (mean/p99 per delta, plus first-10%-vs-
// last-10% cost to catch O(n^2) directly, not just inferred by comparing
// three separate runs at different N) - the exact numbers documented in
// wave4-report.md. vitest re-invokes a bench() callback many times to
// gather statistics; the profile is deterministic given a fixed seed, so
// every invocation prints (approximately) the same numbers - read any one
// block from the captured output.
function printProfile(n: number, seed: number): void {
  const { notifications, ref } = buildFloodStream(n, seed);
  const model = hydrateFloodModel(ref);
  const { perDeltaMs, totalMs } = foldWithTiming(model, notifications);
  const tenth = Math.floor(n / 10);
  const firstTenth = perDeltaMs.slice(0, tenth);
  const lastTenth = perDeltaMs.slice(-tenth);
  const firstMean = mean(firstTenth);
  const lastMean = mean(lastTenth);
  // Deliberate console output - this file is never part of `vitest run`
  // (see the file header comment), and lint/suspicious/noConsole isn't
  // enabled in this project's biome.jsonc.
  console.log(
    [
      "",
      `--- token-flood profile: n=${n} ---`,
      `total fold time (all ${n} notifications incl. turn/started, item/started, item/completed, turn/completed): ${totalMs.toFixed(2)}ms`,
      `mean per-delta:        ${mean(perDeltaMs).toFixed(4)}ms`,
      `p99 per-delta:         ${percentile(perDeltaMs, 99).toFixed(4)}ms`,
      `first 10% mean (n=${tenth}): ${firstMean.toFixed(4)}ms`,
      `last 10% mean  (n=${tenth}): ${lastMean.toFixed(4)}ms`,
      `late/early ratio:      ${(lastMean / firstMean).toFixed(2)}x  (~1x = flat/O(1) per delta -> O(n) total; ~10x at n=${n} would track a linear-in-position O(n) per-delta cost -> O(n^2) total)`,
    ].join("\n"),
  );
}

describe("token-flood: per-delta profile + growth curve (console output - see file header)", () => {
  bench("profile n=10,000", () => {
    printProfile(10_000, 7);
  });
});

// The scaffold both 64 B-delta cases below share: a fresh model with one
// streaming item open and the delta notification that feeds it. Extracted so
// the fold case and the read case (issue #1560) cannot drift apart.
function stringDeltaScaffold(kind: "toolOutput" | "agentMessage") {
  const ref = `ref_${kind}`;
  const threadId = `thr_${kind}`;
  const turnId = `turn_${kind}`;
  const itemId = `item_${kind}`;
  let model = hydrateFloodModel(ref);
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId, ref, turn: { id: turnId, status: "inProgress", itemsView: "" } },
    } as AnyNotification,
    1000,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId,
        ref,
        turnId,
        item: {
          type: kind === "toolOutput" ? "commandExecution" : "agentMessage",
          id: itemId,
          turnId,
          status: "inProgress",
        },
      },
    } as AnyNotification,
    1001,
  );

  const delta = "0123456789abcdef".repeat(4); // 64 B
  const notification =
    kind === "toolOutput"
      ? ({
          method: "item/toolOutput/delta",
          params: { threadId, ref, turnId, itemId, callId: `call_${kind}`, delta },
        } as AnyNotification)
      : ({ method: "item/agentMessage/delta", params: { threadId, ref, turnId, itemId, delta } } as AnyNotification);
  return { model, notification, itemId };
}

// A model's item by id - the streaming item the scaffold above opened. A
// nested find, not turns.flatMap(...).find(...): the read case calls this once
// per delta, and flatMap allocates an array per call - work in every decile
// that has nothing to do with the read being measured.
function streamedItem(m: ReturnType<typeof hydrateFloodModel>, itemId: string) {
  for (const turn of m.turns) {
    const item = turn.items.find((it) => it.id === itemId);
    if (item) return item;
  }
  return undefined;
}

// The item's accumulated text as both cases read it: tool output, or the
// joined pending chunks plus any settled text.
function accumulatedText(item: ReturnType<typeof streamedItem>) {
  return item?.output ?? pendingTextJoined(item?.pendingText ?? []) + (item?.text ?? "");
}

// Ten equal-size decile windows by cumulative elapsed time, shared by both
// 64 B-delta cases so their ratios use the same boundaries. A single reducer
// apply is ~1us, below the timer's practical noise floor, so per-delta means
// are dominated by jitter and a decile window (many deltas aggregated into a
// millisecond-scale sample) is where the signal is real. `observe(i)` closes a
// window every n/10 deltas.
function decileAccumulator(n: number) {
  const tenth = Math.floor(n / 10);
  const decileMs: number[] = [];
  let mark = performance.now();
  return {
    decileMs,
    observe(i: number): void {
      if ((i + 1) % tenth === 0 && decileMs.length < 10) {
        const t = performance.now();
        decileMs.push(t - mark);
        mark = t;
      }
    },
  };
}

// Tool-output fold case (issue #1562): the #1547 / D23c-2a review claimed
// `item/toolOutput/delta`'s `output + delta` accumulation (reducer.ts case
// "item/toolOutput/delta") is O(n^2). It is not - the append is one flat
// string per delta, which V8 folds into a rope at O(1) amortized (Hermes
// buffers the primitive, likewise non-quadratic) - so a real-reducer fold of
// 64 B deltas shows a ~flat late/early decile ratio at every size. The
// agentMessage path is measured alongside as the contrast: THAT path's
// pre-appendChunk array spread is the one that was quadratic (chunkview.ts's
// module comment), and it folds flat too now. The original refutation ran
// these same sizes under Node/V8 and Hermes; this in-tree case runs under
// whatever engine executes `vitest bench`.
function foldStringDeltas(kind: "toolOutput" | "agentMessage", n: number) {
  const { model, notification, itemId } = stringDeltaScaffold(kind);
  const run = () => {
    let m = model;
    let now = 1002;
    const acc = decileAccumulator(n);
    for (let i = 0; i < n; i += 1) {
      now += 1;
      m = applyNotification(m, notification, now);
      acc.observe(i);
    }
    return { m, decileMs: acc.decileMs };
  };
  // One untimed pass first so the timed pass's first decile isn't inflated by
  // cold interpretation, which would fake an O(n^2) ratio. Then the MIN of
  // each decile across five timed passes: noise only ever adds time, so the
  // minimum is the cleanest estimate of the decile's true cost, and it keeps a
  // stray GC pause from reading as growth.
  run();
  const minDecileMs: number[] = [];
  let m = model;
  for (let r = 0; r < 5; r += 1) {
    const rep = run();
    if (r === 0) m = rep.m;
    rep.decileMs.forEach((ms, i) => {
      minDecileMs[i] = Math.min(minDecileMs[i] ?? Number.POSITIVE_INFINITY, ms);
    });
  }
  const firstMs = minDecileMs[0] ?? 0;
  const lastMs = minDecileMs[minDecileMs.length - 1] ?? 0;
  // accumulatedText's non-zero length also proves the fold accumulated.
  return { ratio: lastMs / firstMs, firstMs, lastMs, bytes: accumulatedText(streamedItem(m, itemId)).length };
}

describe("token-flood: tool-output vs agentMessage fold (64 B deltas)", () => {
  bench("late/early decile ratio at n=2,000/8,000/32,000", () => {
    for (const n of [2_000, 8_000, 32_000]) {
      const tool = foldStringDeltas("toolOutput", n);
      const msg = foldStringDeltas("agentMessage", n);
      console.log(
        `--- fold n=${n} (64 B deltas): tool-output late/early ${tool.ratio.toFixed(2)}x ` +
          `(first decile ${tool.firstMs.toFixed(2)}ms, last ${tool.lastMs.toFixed(2)}ms, ${tool.bytes} B) ` +
          `| agentMessage late/early ${msg.ratio.toFixed(2)}x ` +
          `(first decile ${msg.firstMs.toFixed(2)}ms, last ${msg.lastMs.toFixed(2)}ms, ${msg.bytes} B)`,
      );
    }
  });
});

// Issue #1560: the cases above never READ the accumulated text, so they time
// only the accumulate half and the frame-budget number hides the read. A
// consumer that touches the whole text after every delta - a renderer
// redrawing each frame - pays a full O(current-length) read every time: on V8
// the read flattens the rope that `output + delta` (tool output) and
// `appendChunk`'s cached `brand.text + delta` (pendingText) both build, so
// fold+read is quadratic even though either fold alone is linear. (The chunk
// backing removed the array copy, not the read.) This case times that read
// half at the same decile granularity: tens of x last/first is the
// super-linear signature (the fold alone sits near 1x).
function readStringDeltas(kind: "toolOutput" | "agentMessage", n: number) {
  const { model: base, notification, itemId } = stringDeltaScaffold(kind);
  const acc = decileAccumulator(n);
  let m = base;
  let now = 1002;
  // The read's RESULT must be consumed: the reader's whole point is the
  // flatten, and a discarded `charCodeAt` is pure enough that V8 eliminates
  // it, silently turning this case back into a fold. `checksum` is printed
  // with the timing as the proof the read actually ran.
  let checksum = 0;
  for (let i = 0; i < n; i += 1) {
    now += 1;
    m = applyNotification(m, notification, now);
    // The per-delta reader: consume the WHOLE accumulated text, not just one
    // code unit. A trailing-char access can be satisfied by descending a rope
    // to its rightmost leaf without flattening, so it alone does not reliably
    // exercise the full-text read a renderer does; summing every code unit
    // forces the flatten on any rope implementation. It reads the same value
    // `accumulatedText` reports, so the timed read and the `bytes` check below
    // cannot drift (roborev on #1909).
    const text = accumulatedText(streamedItem(m, itemId));
    for (let c = 0; c < text.length; c += 1) checksum += text.charCodeAt(c);
    acc.observe(i);
  }
  const firstMs = acc.decileMs[0] ?? 0;
  const lastMs = acc.decileMs[acc.decileMs.length - 1] ?? 0;
  const totalMs = acc.decileMs.reduce((a, b) => a + b, 0);
  // A single pass, not the fold case's min-of-five: each read is O(n), so the
  // min-of-five rig would multiply a run that is already seconds long. The
  // read's growth is tens of x, far outside jitter, so one pass is decisive.
  return {
    ratio: lastMs / firstMs,
    firstMs,
    lastMs,
    totalMs,
    bytes: accumulatedText(streamedItem(m, itemId)).length,
    checksum,
  };
}

// Sizes stop at 4,000: the read is super-linear, so the fold cases' 32,000
// would take minutes per pass here. The GROWTH CURVE is the finding and is
// already unmistakable at these sizes - the fold cases above run to 32,000
// precisely because they are cheap; this one cannot. One bench per size so
// vitest's own hz/mean describes a single size rather than the sum of them all
// (roborev on #1909); the console-logged decile lines are the primary signal.
describe("token-flood: fold+READ after every delta (64 B deltas) - the half the fold cases omit", () => {
  for (const n of [1_000, 2_000, 4_000]) {
    bench(`fold-and-read n=${n} (64 B deltas)`, () => {
      const tool = readStringDeltas("toolOutput", n);
      const msg = readStringDeltas("agentMessage", n);
      console.log(
        `--- fold+read n=${n} (64 B deltas): tool-output late/early ${tool.ratio.toFixed(1)}x ` +
          `(total ${tool.totalMs.toFixed(0)}ms, first decile ${tool.firstMs.toFixed(2)}ms, last ${tool.lastMs.toFixed(2)}ms, ${tool.bytes} B, sum ${tool.checksum}) ` +
          `| agentMessage late/early ${msg.ratio.toFixed(1)}x ` +
          `(total ${msg.totalMs.toFixed(0)}ms, first decile ${msg.firstMs.toFixed(2)}ms, last ${msg.lastMs.toFixed(2)}ms, ${msg.bytes} B, sum ${msg.checksum})`,
      );
    });
  }
});
