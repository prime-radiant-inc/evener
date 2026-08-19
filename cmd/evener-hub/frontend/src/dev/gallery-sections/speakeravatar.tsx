import { SpeakerAvatar } from "../../widgets/speakeravatar";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

export default function SpeakerAvatarGallerySection() {
  return (
    <section>
      <h2>SpeakerAvatar</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>user (person)</p>
          <SpeakerAvatar speaker="user" />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>agent (skill)</p>
          <SpeakerAvatar speaker="agent" />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>size 32</p>
          <SpeakerAvatar speaker="user" size={32} />
        </div>
      </ThemeFlip>
    </section>
  );
}
