import { Button } from "../../widgets/button";
import { SendIcon } from "../../widgets/sendicon";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

export default function SendIconGallerySection() {
  return (
    <GallerySection title="SendIcon">
      <div className={styles.row}>
        <p className={styles.rowLabel}>on its own</p>
        <SendIcon />
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>in its two habitats - the composer's Send and the spawn pane's Start</p>
        <Button variant="primary" size="xs" icon={<SendIcon />}>
          Send
        </Button>
        <Button variant="primary" size="xs" icon={<SendIcon />}>
          Start
        </Button>
      </div>
    </GallerySection>
  );
}
