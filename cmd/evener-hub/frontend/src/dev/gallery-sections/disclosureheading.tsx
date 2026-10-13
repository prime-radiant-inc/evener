import { useState } from "react";
import { DisclosureHeading, Menu } from "../../widgets";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./disclosure.module.css";

// The rail's section headings: an eyebrow group label open and folded, and
// an authored (pinned-section) name carrying its "⋯" action.
function DisclosureHeadingDemo() {
  const [liveOpen, setLiveOpen] = useState(true);
  const [archivedOpen, setArchivedOpen] = useState(false);
  const [pinsOpen, setPinsOpen] = useState(true);

  return (
    <div className={styles.frame}>
      <DisclosureHeading level={3} label="Live" eyebrow open={liveOpen} onOpenChange={setLiveOpen} />
      {liveOpen && <p className={styles.body}>The group the heading folds, rendered by the caller.</p>}
      <DisclosureHeading
        level={3}
        label="Archived sessions (12)"
        eyebrow
        open={archivedOpen}
        onOpenChange={setArchivedOpen}
      />
      {archivedOpen && <p className={styles.body}>Folded at rest.</p>}
      <DisclosureHeading
        level={3}
        label="Release prep"
        open={pinsOpen}
        onOpenChange={setPinsOpen}
        action={
          <Menu
            variant="quiet"
            icon="⋯"
            label="Actions for Release prep"
            items={[
              { id: "rename", label: "Rename", onSelect: () => {} },
              { id: "delete", label: "Delete", onSelect: () => {} },
            ]}
          />
        }
      />
      {pinsOpen && (
        <p className={styles.body}>An authored name keeps its casing; the action sits beside the heading.</p>
      )}
    </div>
  );
}

export default function DisclosureHeadingGallerySection() {
  return (
    <section>
      <h2>DisclosureHeading</h2>
      <ThemeFlip>
        <DisclosureHeadingDemo />
      </ThemeFlip>
    </section>
  );
}
