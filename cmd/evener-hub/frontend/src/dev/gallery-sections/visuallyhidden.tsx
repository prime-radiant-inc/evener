import { IconButton } from "../../widgets/iconbutton";
import { VisuallyHidden } from "../../widgets/visuallyhidden";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

export default function VisuallyHiddenGallerySection() {
  return (
    <section>
      <h2>VisuallyHidden</h2>
      <ThemeFlip>
        {/* Nothing to see by design: the row shows that the hidden text takes
            no space, while a screen reader still reads it. */}
        <div className={styles.row}>
          <p className={styles.rowLabel}>beside visible text</p>
          <span>
            3 sessions
            <VisuallyHidden>, one needs you</VisuallyHidden>
          </span>
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>naming an icon</p>
          <IconButton label="Close" icon={<span aria-hidden="true">×</span>} />
        </div>
      </ThemeFlip>
    </section>
  );
}
