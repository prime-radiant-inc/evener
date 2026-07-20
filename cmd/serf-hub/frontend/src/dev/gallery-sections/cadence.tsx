import { Cadence, type CadenceState } from "../../widgets/cadence";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./cadence.module.css";

// A fixed anchor instant, not Date.now(): the gallery is a design review
// tool, and Cadence itself is pure, so there's no reason for its sample
// data to look different on every reload.
const NOW = 1_700_000_000_000;

function every(intervalMs: number, count: number, startAgo: number): number[] {
  return Array.from({ length: count }, (_, i) => NOW - startAgo - i * intervalMs);
}

const SAMPLES: { state: CadenceState; frameTimes: number[] }[] = [
  { state: "working", frameTimes: every(1_500, 14, 0) }, // dense, fresh
  { state: "needs-you", frameTimes: every(1_500, 14, 0) }, // same trace, amber
  { state: "failed", frameTimes: every(2_000, 6, 8_000) }, // stopped abruptly
  { state: "idle", frameTimes: every(6_000, 4, 40_000) }, // sparse, aged
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
