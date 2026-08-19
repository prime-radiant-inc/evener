import { Disclosure } from "../../widgets/disclosure";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./disclosure.module.css";

// One row ships defaultOpen and one collapsed, so both an expanded and a
// collapsed disclosure are visible at rest without any interaction. Open state
// lives in disclosureStore keyed by `id`, not component-local useState (the
// whole point of this widget: it survives the VirtualList/dockview remount
// that resets a local state). ThemeFlip renders this demo twice, so both
// copies share these two ids and toggle in lockstep - fine here (unlike the
// Tooltip section's focus caveat, there's no single-owner resource to fight
// over): each theme pane still renders whatever the store says, correctly, in
// its own palette.
function DisclosureDemo() {
  return (
    <div className={styles.frame}>
      <Disclosure id="gallery-disclosure-open" summary="Tool call · read_file" defaultOpen>
        <p className={styles.body}>The collapsible body. Expansion survives a remount because it lives in the store.</p>
      </Disclosure>
      <Disclosure id="gallery-disclosure-closed" summary="Tool call · write_file">
        <p className={styles.body}>This one starts collapsed. Click the summary row to toggle it.</p>
      </Disclosure>
    </div>
  );
}

export default function DisclosureGallerySection() {
  return (
    <section>
      <h2>Disclosure</h2>
      <ThemeFlip>
        <DisclosureDemo />
      </ThemeFlip>
    </section>
  );
}
