import { Eyebrow } from "../../widgets/eyebrow";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./eyebrow.module.css";

export default function EyebrowGallerySection() {
  return (
    <section>
      <h2>Eyebrow</h2>
      <ThemeFlip>
        <div className={styles.stack}>
          <Eyebrow>Agent setup</Eyebrow>
          <Eyebrow as="h3">Navigation</Eyebrow>
          <p className={styles.inline}>
            <Eyebrow as="span">Session</Eyebrow> as=span, inline beside a status
          </p>
        </div>
      </ThemeFlip>
    </section>
  );
}
