import { SteeringGlyph } from "../../widgets/steeringglyph";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

export default function SteeringGlyphGallerySection() {
  return (
    <GallerySection title="SteeringGlyph">
      <div className={styles.row}>
        <p className={styles.rowLabel}>on its own</p>
        <SteeringGlyph />
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>inline in a row of text</p>
        <span>
          <SteeringGlyph /> System steered: Tasks complete
        </span>
      </div>
    </GallerySection>
  );
}
