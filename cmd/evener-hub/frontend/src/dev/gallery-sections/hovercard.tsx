import { HoverCard } from "../../widgets/hovercard";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./tooltip.module.css";

function HoverCardDemo() {
  return (
    <div className={styles.demo}>
      <p className={styles.hint}>Hover or focus the entity reference.</p>
      <HoverCard
        label={
          <div>
            <strong>Shell job · completed</strong>
            <div>Tests passed in 14 seconds</div>
          </div>
        }
      >
        <button type="button">job_01HZX7P9</button>
      </HoverCard>
    </div>
  );
}

export default function HoverCardGallerySection() {
  return (
    <section>
      <h2>HoverCard</h2>
      <ThemeFlip>
        <HoverCardDemo />
      </ThemeFlip>
    </section>
  );
}
