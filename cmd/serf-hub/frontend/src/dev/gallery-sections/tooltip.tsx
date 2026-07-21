import { Tooltip } from "../../widgets/tooltip";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./tooltip.module.css";

// Shown in its natural closed state, not forced open, unlike this batch's
// other overlays: real DOM focus can only ever belong to one element at a
// time, and ThemeFlip renders this demo twice (dark pane + light pane) -
// autofocusing either copy's trigger would blur (and so hide) the other
// the instant the second one mounts, giving an asymmetric, flickering
// result rather than a clean side-by-side comparison. Hover or Tab to
// either copy below to see it - both are fully live.
//
// The trigger is a plain native <button>, not the Button widget: Tooltip's
// aria-describedby association (see its own doc comment) only reaches a
// child that forwards extra props to its DOM node, which Button's fixed
// prop list does not do. This is the fully-wired reference pattern; a
// <Tooltip><Button/></Tooltip> pairing still shows/hides correctly (that
// part works via event bubbling regardless of the child) but skips the
// screen-reader description link - see this task's report.
function TooltipDemo() {
  return (
    <div className={styles.demo}>
      <p className={styles.hint}>Hover or focus the button.</p>
      <Tooltip label="Copies the session ID to your clipboard">
        <button type="button">Copy ID</button>
      </Tooltip>
    </div>
  );
}

export default function TooltipGallerySection() {
  return (
    <section>
      <h2>Tooltip</h2>
      <ThemeFlip>
        <TooltipDemo />
      </ThemeFlip>
    </section>
  );
}
