import { FailureGlyph } from "../../widgets/failureglyph";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

export default function FailureGlyphGallerySection() {
  return (
    <GallerySection title="FailureGlyph">
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
    </GallerySection>
  );
}
