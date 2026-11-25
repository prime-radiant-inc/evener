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

function ToneRow({
  rowLabel,
  label,
  barLabel = label,
  tone,
  value = 65,
}: {
  rowLabel: string;
  label: string;
  barLabel?: string;
  tone?: MeterTone;
  value?: number;
}) {
  return (
    <div className={styles.row}>
      <p className={styles.rowLabel}>{rowLabel}</p>
      <div className={styles.track}>
        <Meter label={barLabel} value={value} max={100} tone={tone} />
      </div>
      <Meter label={`${label} ring`} value={value} max={100} tone={tone} variant="ring" />
      <span className={styles.ringPill}>
        <Meter label={`${label} pill ring`} value={value} max={100} tone={tone} variant="ring" />
      </span>
    </div>
  );
}

export default function MeterGallerySection() {
  return (
    <GallerySection title="Meter">
      {SAMPLES.map(({ tone, label }) => (
        <ToneRow key={tone} rowLabel={tone} tone={tone} label={label} />
      ))}
      <ToneRow rowLabel="0% empty" label="Empty" barLabel="Empty bar" value={0} />
    </GallerySection>
  );
}
