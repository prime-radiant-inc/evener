import { Chip, type ChipTone } from "../../widgets/chip";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

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
    <section>
      <h2>Chip</h2>
      <ThemeFlip>
        {TONES.map((tone) => (
          <ToneRow key={tone} tone={tone} />
        ))}
      </ThemeFlip>
    </section>
  );
}
