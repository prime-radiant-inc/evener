import { Badge, type BadgeTone } from "../../widgets/badge";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

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
    <GallerySection title="Badge">
      {TONES.map((tone) => (
        <ToneRow key={tone} tone={tone} />
      ))}
    </GallerySection>
  );
}
