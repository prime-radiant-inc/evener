import type { CadenceState } from "../../widgets/cadence";
import { StatusDot } from "../../widgets/statusdot";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

const STATES: CadenceState[] = ["idle", "working", "needs-you", "failed", "ended"];

export default function StatusDotGallerySection() {
  return (
    <GallerySection title="StatusDot">
      {STATES.map((state) => (
        <div className={styles.row} key={state}>
          <p className={styles.rowLabel}>{state}</p>
          <StatusDot state={state} />
        </div>
      ))}
    </GallerySection>
  );
}
