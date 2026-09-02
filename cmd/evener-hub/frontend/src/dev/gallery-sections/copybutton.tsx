import { CopyButton } from "../../widgets/copybutton";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

export default function CopyButtonGallerySection() {
  return (
    <section>
      <h2>CopyButton</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>default</p>
          <CopyButton text="const x = 1;" label="Copy" />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>custom label</p>
          <CopyButton text={'{"id":"dlg_42"}'} label="Copy raw JSON" />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>sm size</p>
          <CopyButton text="short text" label="Copy" size="sm" />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>primary variant</p>
          <CopyButton text="important text" label="Copy" variant="primary" />
        </div>
      </ThemeFlip>
    </section>
  );
}
