import { Chevron } from "../../widgets/chevron";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

export default function ChevronGallerySection() {
  return (
    <section>
      <h2>Chevron</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>right</p>
          <Chevron direction="right" />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>down</p>
          <Chevron direction="down" />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>left</p>
          <Chevron direction="left" />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>up</p>
          <Chevron direction="up" />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>size 16</p>
          <Chevron size={16} />
        </div>
        {/* The reason the icon is square rather than a text triangle: the
            disclosure sites turn it in CSS, and a taller-than-wide box gets
            WIDER when turned, escaping whatever row holds it. Shown turned
            here so a reviewer can see the box hold. */}
        <div className={styles.row}>
          <p className={styles.rowLabel}>turned 90°</p>
          <span style={{ display: "inline-flex", transform: "rotate(90deg)" }}>
            <Chevron direction="right" />
          </span>
        </div>
        {/* currentColor: the glyph takes whatever ink its consumer sets, so a
            quiet disclosure and a full-contrast control share one icon. */}
        <div className={styles.row}>
          <p className={styles.rowLabel}>inherits ink</p>
          <span style={{ display: "inline-flex", color: "var(--ink-low)" }}>
            <Chevron />
          </span>
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>beside text</p>
          <span style={{ display: "inline-flex", alignItems: "center", gap: "var(--space-2)" }}>
            <Chevron /> Delegated: run the test suite
          </span>
        </div>
      </ThemeFlip>
    </section>
  );
}
