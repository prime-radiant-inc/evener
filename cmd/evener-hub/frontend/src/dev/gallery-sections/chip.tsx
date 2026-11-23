import { Chip, type ChipTone } from "../../widgets/chip";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

const TONES: ChipTone[] = ["neutral", "attention", "alive", "danger"];

function ToneRow({ tone }: { tone: ChipTone }) {
  return (
    <div className={styles.row}>
      <p className={styles.rowLabel}>{tone}</p>
      <Chip tone={tone}>backend</Chip>
      <Chip tone={tone} onRemove={() => {}}>
        removable
      </Chip>
    </div>
  );
}

export default function ChipGallerySection() {
  return (
    <GallerySection title="Chip">
      {TONES.map((tone) => (
        <ToneRow key={tone} tone={tone} />
      ))}
      <div className={styles.row}>
        <p className={styles.rowLabel}>mono</p>
        <Chip mono>apply_patch</Chip>
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>quiet</p>
        <Chip variant="quiet">current</Chip>
        <Chip variant="quiet" mono>
          apply_patch
        </Chip>
      </div>
    </GallerySection>
  );
}
