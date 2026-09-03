import { Timestamp } from "../../widgets/timestamp";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

// A fixed anchor instant, not Date.now() (same reasoning as cadence.tsx's and
// loader.tsx's own gallery sections): Timestamp is a pure render of its props,
// so its sample data — including the frozen relative readout — should not
// look different on every reload.
const NOW = 1_700_000_000_000;

const SAMPLES: { label: string; value: number }[] = [
  { label: "just now", value: NOW - 4_000 },
  { label: "seconds", value: NOW - 30_000 },
  { label: "minutes", value: NOW - 5 * 60_000 },
  { label: "hours", value: NOW - 3 * 3_600_000 },
  { label: "days (other-day → date+time on hover)", value: NOW - 2 * 86_400_000 },
];

export default function TimestampGallerySection() {
  return (
    <section>
      <h2>Timestamp</h2>
      <ThemeFlip>
        {SAMPLES.map(({ label, value }) => (
          <div className={styles.row} key={label}>
            <p className={styles.rowLabel}>{label}</p>
            <Timestamp value={value} now={NOW} />
          </div>
        ))}
      </ThemeFlip>
    </section>
  );
}
