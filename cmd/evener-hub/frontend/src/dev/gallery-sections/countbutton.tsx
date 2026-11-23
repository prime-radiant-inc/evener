import { CountButton } from "../../widgets/countbutton";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

export default function CountButtonGallerySection() {
  const agents = {
    glyph: "⌘",
    label: "Agents",
    count: "3/4",
    accessibleLabel: "Agents, 3 of 4 active",
  };
  return (
    <GallerySection title="CountButton">
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
    </GallerySection>
  );
}
