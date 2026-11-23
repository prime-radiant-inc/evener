import { CopyButton } from "../../widgets/copybutton";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

export default function CopyButtonGallerySection() {
  return (
    <GallerySection title="CopyButton">
      <div className={styles.row}>
        <p className={styles.rowLabel}>default</p>
        <CopyButton text="const x = 1;" label="Copy" />
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>custom label</p>
        <CopyButton text={'{"id":"dlg_42"}'} label="Copy raw JSON" />
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>text presentation</p>
        <CopyButton text="ABCD-1234" label="Copy code" presentation="text" />
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>sm size</p>
        <CopyButton text="short text" label="Copy" size="sm" />
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>primary variant</p>
        <CopyButton text="important text" label="Copy" variant="primary" />
      </div>
    </GallerySection>
  );
}
