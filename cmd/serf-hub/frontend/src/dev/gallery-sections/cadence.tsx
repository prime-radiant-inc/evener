import { Cadence, type CadenceState } from "../../widgets/cadence";
import { ThemeFlip } from "../ThemeFlip";
import styles from "../gallery-section.module.css";

// A fixed anchor instant, not Date.now(): the gallery is a design review
// tool, and Cadence itself is pure, so there's no reason for its sample
// data to look different on every reload.
const NOW = 1_700_000_000_000;

function every(intervalMs: number, count: number, startAgo: number): number[] {
  return Array.from({ length: count }, (_, i) => NOW - startAgo - i * intervalMs);
}

// Each state's frameTimes are built from multiple every() bursts at
// different rates rather than one uniform run, so the trace's density
// actually varies across the 60s window instead of reading as a single
// evenly-spaced smear - that variation IS the instrument's story
// (density = activity, sparseness/fade = silence).
const SAMPLES: { state: CadenceState; frameTimes: number[] }[] = [
  {
    // Dense and weighted fresh: a tight, busy cluster in the last ~14s
    // thinning out across the rest of the window, ending in a faint
    // stretch near the 60s edge - 20 ticks total.
    state: "working",
    frameTimes: [
      ...every(1_500, 10, 0), // 0-13.5s ago: busy
      ...every(3_000, 5, 16_000), // 16-28s ago: thinning
      ...every(5_000, 3, 32_000), // 32-42s ago: sparser
      ...every(6_000, 2, 47_000), // 47-53s ago: faint tail
    ],
  },
  {
    // Moderate density, same shape idea as "working" but roughly half the
    // ticks - and because needs-you tints the whole trace amber, the
    // freshest few (highest opacity) read as a distinct amber trailing
    // edge against the paler, older ticks behind them.
    state: "needs-you",
    frameTimes: [
      ...every(1_500, 3, 0), // 0-3s ago: bright trailing edge
      ...every(4_000, 4, 6_000), // 6-18s ago: moderate
      ...every(7_000, 3, 24_000), // 24-38s ago: aging out
    ],
  },
  {
    // A short, tight burst that just stops - nothing in the ~9s closest to
    // "now" - reading as an agent that was working and then failed.
    state: "failed",
    frameTimes: every(1_800, 6, 9_000),
  },
  {
    // Sparse and aged toward the oldest two buckets (30-60s ago), nothing
    // fresh - the "quiet, decaying" read.
    state: "idle",
    frameTimes: [...every(7_000, 2, 33_000), ...every(5_500, 3, 47_000)],
  },
  { state: "ended", frameTimes: [] }, // no history left in the window
];

export default function CadenceGallerySection() {
  return (
    <section>
      <h2>Cadence</h2>
      <ThemeFlip>
        {SAMPLES.map(({ state, frameTimes }) => (
          <div className={styles.row} key={state}>
            <p className={styles.rowLabel}>{state}</p>
            <Cadence state={state} frameTimes={frameTimes} now={NOW} />
          </div>
        ))}
      </ThemeFlip>
    </section>
  );
}
