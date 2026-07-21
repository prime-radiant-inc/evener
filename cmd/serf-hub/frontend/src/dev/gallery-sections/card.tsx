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
            <p className={styles.body}>A raised, bordered surface for grouping related content.</p>
          </Card>
        </div>
      </ThemeFlip>
    </section>
  );
}
