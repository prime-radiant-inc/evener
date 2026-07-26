import { SteeringGlyph } from "../../widgets/steeringglyph";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

export default function SteeringGlyphGallerySection() {
  return (
    <section>
      <h2>SteeringGlyph</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>on its own</p>
          <SteeringGlyph />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>inline in a row of text</p>
          <span>
            <SteeringGlyph /> System steered: Tasks done
          </span>
        </div>
      </ThemeFlip>
    </section>
  );
}
