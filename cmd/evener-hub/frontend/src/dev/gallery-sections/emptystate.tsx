import { Button } from "../../widgets/button";
import { EmptyState } from "../../widgets/emptystate";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./emptystate.module.css";

export default function EmptyStateGallerySection() {
  return (
    <section>
      <h2>EmptyState</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <div className={styles.sample}>
            <EmptyState title="No sessions yet" />
          </div>
          <div className={styles.sample}>
            <EmptyState title="No sessions yet" hint="Start one from the command palette." />
          </div>
          <div className={styles.sample}>
            <EmptyState
              title="No sessions yet"
              hint="Start one from the command palette."
              action={<Button size="sm">New session</Button>}
            />
          </div>
        </div>
      </ThemeFlip>
    </section>
  );
}
