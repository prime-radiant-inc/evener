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
          <Card variant="outlined" padding="none">
            <div style={{ padding: "var(--space-3) var(--space-4)", borderBottom: "1px solid var(--edge)" }}>
              <p className={styles.body}>A flush tool body: the card owns the edge, its sections own the inset.</p>
            </div>
            <div style={{ padding: "var(--space-3) var(--space-4)" }}>
              <p className={styles.body}>padding="none" lets a section boundary run edge to edge.</p>
            </div>
          </Card>
        </div>
      </ThemeFlip>
    </section>
  );
}
