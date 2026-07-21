import { Meter, type MeterTone } from "../../widgets/meter";
import { ThemeFlip } from "../ThemeFlip";
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
    </div>
  );
}

export default function MeterGallerySection() {
  return (
    <section>
      <h2>Meter</h2>
      <ThemeFlip>
        {SAMPLES.map(({ tone, label }) => (
          <ToneRow key={tone} tone={tone} label={label} />
        ))}
      </ThemeFlip>
    </section>
  );
}
