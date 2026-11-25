import { Meter, type MeterTone } from "../../widgets/meter";
import { GallerySection } from "../GallerySection";
import styles from "./meter.module.css";

// Realistic labels per row, not a repeated placeholder - label is Meter's
// accessible name (role=meter requires one), so the gallery should show it
// carrying real meaning rather than obscuring the requirement.
const SAMPLES: { tone: MeterTone; label: string }[] = [
  { tone: "neutral", label: "Tokens" },
  { tone: "attention", label: "Context used" },
  { tone: "alive", label: "Sync progress" },
  { tone: "danger", label: "Storage over quota" },
];

function ToneRow({ tone, label }: { tone: MeterTone; label: string }) {
  return (
    <div className={styles.row}>
      <p className={styles.rowLabel}>{tone}</p>
      <div className={styles.track}>
        <Meter label={label} value={65} max={100} tone={tone} />
      </div>
      <Meter label={`${label} ring`} value={65} max={100} tone={tone} variant="ring" />
      <span className={styles.ringPill}>
        <Meter label={`${label} pill ring`} value={65} max={100} tone={tone} variant="ring" />
      </span>
    </div>
  );
}

export default function MeterGallerySection() {
  return (
    <GallerySection title="Meter">
      {SAMPLES.map(({ tone, label }) => (
        <ToneRow key={tone} tone={tone} label={label} />
      ))}
      <div className={styles.row}>
        <p className={styles.rowLabel}>0% empty</p>
        <div className={styles.track}>
          <Meter label="Empty bar" value={0} max={100} />
        </div>
        <Meter label="Empty ring" value={0} max={100} variant="ring" />
        <span className={styles.ringPill}>
          <Meter label="Empty pill ring" value={0} max={100} variant="ring" />
        </span>
      </div>
    </GallerySection>
  );
}
