import type { CadenceState } from "../../widgets/cadence";
import { StatusDot } from "../../widgets/statusdot";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

const STATES: CadenceState[] = ["idle", "working", "needs-you", "failed", "ended"];

export default function StatusDotGallerySection() {
  return (
    <section>
      <h2>StatusDot</h2>
      <ThemeFlip>
        {STATES.map((state) => (
          <div className={styles.row} key={state}>
            <p className={styles.rowLabel}>{state}</p>
            <StatusDot state={state} />
          </div>
        ))}
      </ThemeFlip>
    </section>
  );
}
