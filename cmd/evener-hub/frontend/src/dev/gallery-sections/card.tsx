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
        </div>
      </ThemeFlip>
    </section>
  );
}
