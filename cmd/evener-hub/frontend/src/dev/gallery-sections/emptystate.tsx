import { Button } from "../../widgets/button";
import { EmptyState } from "../../widgets/emptystate";
import { GallerySection } from "../GallerySection";
import styles from "./emptystate.module.css";

export default function EmptyStateGallerySection() {
  return (
    <GallerySection title="EmptyState">
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
        <div className={styles.sample}>
          <EmptyState
            title="Couldn't load the workspace"
            hint="Several actions: a fragment in the action slot."
            action={
              <>
                <Button size="sm">Retry</Button>
                <Button size="sm" variant="quiet">
                  Reload page
                </Button>
              </>
            }
          />
        </div>
        <div className={styles.sample}>
          <EmptyState title="No session open" hint="size=display: the welcome hero" size="display" />
        </div>
        <div className={styles.sample}>
          <EmptyState size="compact" title="No plugins installed yet. Install one from Browse." />
        </div>
        <div className={styles.sample}>
          <EmptyState
            size="compact"
            title="No matches"
            hint="Nothing in live, past, or this session."
            action={
              <Button variant="quiet" size="sm">
                Clear filter
              </Button>
            }
          />
        </div>
      </div>
    </GallerySection>
  );
}
