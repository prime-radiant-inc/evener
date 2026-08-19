import { FailureGlyph } from "../../widgets/failureglyph";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

export default function FailureGlyphGallerySection() {
  return (
    <section>
      <h2>FailureGlyph</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>on its own</p>
          <FailureGlyph />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>inline in a row of text</p>
          <span>
            <FailureGlyph /> Ran ./build.sh
          </span>
        </div>
      </ThemeFlip>
    </section>
  );
}
