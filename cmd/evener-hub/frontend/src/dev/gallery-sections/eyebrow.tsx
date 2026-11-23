import { Eyebrow } from "../../widgets/eyebrow";
import { GallerySection } from "../GallerySection";
import styles from "./eyebrow.module.css";

export default function EyebrowGallerySection() {
  return (
    <GallerySection title="Eyebrow">
      <div className={styles.stack}>
        <Eyebrow>Agent setup</Eyebrow>
        <Eyebrow as="h3">Navigation</Eyebrow>
        <p className={styles.inline}>
          <Eyebrow as="span">Session</Eyebrow> as=span, inline beside a status
        </p>
      </div>
    </GallerySection>
  );
}
