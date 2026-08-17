import { Button } from "../../widgets/button";
import { SendIcon } from "../../widgets/sendicon";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

export default function SendIconGallerySection() {
  return (
    <section>
      <h2>SendIcon</h2>
      <ThemeFlip>
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
      </ThemeFlip>
    </section>
  );
}
