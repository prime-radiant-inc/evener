import { SpeakerAvatar } from "../../widgets/speakeravatar";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

export default function SpeakerAvatarGallerySection() {
  return (
    <GallerySection title="SpeakerAvatar">
      <div className={styles.row}>
        <p className={styles.rowLabel}>user (person)</p>
        <SpeakerAvatar speaker="user" />
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>agent (evener mark)</p>
        <SpeakerAvatar speaker="agent" />
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>size 32</p>
        <SpeakerAvatar speaker="user" size={32} />
      </div>
    </GallerySection>
  );
}
