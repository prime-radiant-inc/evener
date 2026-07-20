import { Meter, type MeterTone } from "../../widgets/meter";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./meter.module.css";

const TONES: MeterTone[] = ["neutral", "attention", "alive", "danger"];

function ToneRow({ tone }: { tone: MeterTone }) {
  return (
    <div className={styles.row}>
      <p className={styles.rowLabel}>{tone}</p>
      <div className={styles.track}>
        <Meter value={65} max={100} tone={tone} />
      </div>
    </div>
  );
}

export default function MeterGallerySection() {
  return (
    <section>
      <h2>Meter</h2>
      <ThemeFlip>
        {TONES.map((tone) => (
          <ToneRow key={tone} tone={tone} />
        ))}
      </ThemeFlip>
    </section>
  );
}
