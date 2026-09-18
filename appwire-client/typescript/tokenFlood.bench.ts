// Wave-4 token-flood benchmark: timing half. The correctness half
// (tokenFlood.test.tsx) is part of `vitest run` and asserts the wave plan's
// "no dropped-chunk correctness failures" gate; THIS file measures and
// reports (never asserts - the brief's own framing) the frame-budget half:
// total fold time, mean/p99 per-delta, and the growth curve (a true O(n^2)
// accumulation shows up as a super-linear late/early cost ratio).
//
// `.bench.ts` files are NOT part of `vitest run`'s default include pattern
// - only `npx vitest bench` discovers them - so this file's console output
// never touches the gate battery's pristine-output requirement. Run it
// manually to reproduce the numbers transcribed into
// docs/superpowers/plans/wave4-report.md.
import { bench, describe } from "vitest";
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

// Tool-output fold case (issue #1562): the #1547 / D23c-2a review claimed
// `item/toolOutput/delta`'s `output + delta` accumulation (reducer.ts case
// "item/toolOutput/delta") is O(n^2). It is not - the append is one flat
// string per delta, which V8 folds into a rope at O(1) amortized (Hermes
// buffers the primitive, likewise non-quadratic) - so a real-reducer fold of
// 64 B deltas shows a ~flat late/early decile ratio at every size. The
// agentMessage path is measured alongside as the contrast: THAT path's
// pre-appendChunk array spread is the one that was quadratic (reducer.ts's
// chunk-view comment), and it folds flat too now. The original refutation ran
// these same sizes under Node/V8 and Hermes; this in-tree case runs under
// whatever engine executes `vitest bench`.
function foldStringDeltas(kind: "toolOutput" | "agentMessage", n: number) {
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
  // Time ten EQUAL-SIZE deciles by their cumulative elapsed windows rather
  // than per-delta: a single reducer apply is ~1us, below the timer's practical
  // noise floor, so per-delta means are dominated by jitter. A decile window
  // aggregates 200/800/3,200 deltas into a millisecond-scale sample - the same
  // late/early decile ratio, measured where the signal is real. (Quadratic
  // accumulation would show last/first ~19x regardless: the pre-appendChunk
  // agentMessage path measured ~11x at n=10,000.)
  const tenth = Math.floor(n / 10);
  const run = () => {
    let m = model;
    let now = 1002;
    const decileMs: number[] = [];
    let mark = performance.now();
    for (let i = 0; i < n; i += 1) {
      now += 1;
      m = applyNotification(m, notification, now);
      if ((i + 1) % tenth === 0 && decileMs.length < 10) {
        const t = performance.now();
        decileMs.push(t - mark);
        mark = t;
      }
    }
    return { m, decileMs };
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
  // Proves the fold actually accumulated: output for a tool call, joined
  // pending chunks for the agent message.
  const item = m.turns.flatMap((t) => t.items).find((it) => it.id === itemId);
  const accumulated = item?.output ?? (item?.pendingText ?? []).join("") + (item?.text ?? "");
  return { ratio: lastMs / firstMs, firstMs, lastMs, bytes: accumulated.length };
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
