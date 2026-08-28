import { OpenButton } from "../../widgets/openbutton";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

export default function OpenButtonGallerySection() {
  return (
    <section>
      <h2>OpenButton</h2>
      <p className={styles.note}>
        The one open-out affordance (transcript panes, file docs, external editor). Rendering is planned to change -
        shown here so the change is reviewed in one place.
      </p>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>word</p>
          <OpenButton label="Open transcript" onClick={() => {}} />
          <OpenButton label="Open subagent" onClick={() => {}} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>iconOnly</p>
          <OpenButton iconOnly size="xs" label="Open transcript (xs)" onClick={() => {}} />
          <OpenButton iconOnly size="sm" label="Open transcript (sm)" onClick={() => {}} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>anchor</p>
          <OpenButton href="editor://open?path=/plugins/reviewer.md" word="open in editor" />
        </div>
      </ThemeFlip>
    </section>
  );
}
