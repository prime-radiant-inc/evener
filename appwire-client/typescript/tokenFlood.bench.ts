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
import { buildFloodStream, foldWithTiming, hydrateFloodModel, mean, percentile } from "./testing/tokenFlood";

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
