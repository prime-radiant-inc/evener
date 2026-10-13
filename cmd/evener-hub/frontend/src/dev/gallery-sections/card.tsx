import { Card } from "../../widgets/card";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./card.module.css";

export default function CardGallerySection() {
  return (
    <section>
      <h2>Card</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <Card>
            <p className={styles.body}>A flat section for grouping related content through space and alignment.</p>
          </Card>
          <Card variant="outlined">
            <p className={styles.body}>An outlined box: a tool body or form panel that needs its own edge.</p>
          </Card>
          <Card tone="attention">
            <p className={styles.body}>An attention envelope: a pending question or approval waiting on you.</p>
          </Card>
        </div>
      </ThemeFlip>
    </section>
  );
}
