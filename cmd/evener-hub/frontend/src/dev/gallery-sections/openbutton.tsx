import { OpenButton } from "../../widgets/openbutton";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

export default function OpenButtonGallerySection() {
  return (
    <GallerySection
      title="OpenButton"
      note={
        <p className={styles.note}>
          The one open-out affordance (transcript panes, file docs): icon-only, specific accessible name, tooltip
          "Open", a 28px (phone: tap-min) touch target that never reaches the line box. The anchor form is reserved for
          external targets.
        </p>
      }
    >
      <div className={styles.row}>
        <p className={styles.rowLabel}>button</p>
        <OpenButton label="Open transcript" onClick={() => {}} />
        <OpenButton label="Open subagent" onClick={() => {}} />
        <OpenButton label="Open beside: src/sheet.test.tsx" onClick={() => {}} />
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>anchor</p>
        <OpenButton href="editor://open?path=/plugins/reviewer.md" word="open in editor" />
      </div>
    </GallerySection>
  );
}
