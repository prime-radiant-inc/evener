import { CountButton } from "../../widgets/countbutton";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

export default function CountButtonGallerySection() {
  const agents = {
    glyph: "⌘",
    label: "Agents",
    count: "3/4",
    accessibleLabel: "Agents, 3 of 4 active",
  };
  return (
    <section>
      <h2>CountButton</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>wash</p>
          <CountButton {...agents} />
          <CountButton {...agents} pressed />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>pill</p>
          <CountButton {...agents} chrome="pill" />
          <CountButton {...agents} chrome="pill" compact />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>column</p>
          <CountButton {...agents} orientation="column" compact />
        </div>
      </ThemeFlip>
    </section>
  );
}
