import { Badge, type BadgeTone } from "../../widgets/badge";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

const TONES: BadgeTone[] = ["neutral", "attention", "alive", "danger"];

function ToneRow({ tone }: { tone: BadgeTone }) {
  return (
    <div className={styles.row}>
      <p className={styles.rowLabel}>{tone}</p>
      <Badge count={3} tone={tone} />
      <Badge count={42} tone={tone} />
      <Badge count={0} tone={tone} />
      <Badge count={150} tone={tone} />
    </div>
  );
}

export default function BadgeGallerySection() {
  return (
    <section>
      <h2>Badge</h2>
      <ThemeFlip>
        {TONES.map((tone) => (
          <ToneRow key={tone} tone={tone} />
        ))}
      </ThemeFlip>
    </section>
  );
}
