import { useState } from "react";
import { Popover } from "../../widgets/popover";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./popover.module.css";

// Shown in its natural closed state, not forced open, for the same two
// reasons the Tooltip section documents - amplified here:
//   1. Popover builds on a trapping FocusScope; ThemeFlip renders this demo
//      twice, and auto-opening both would have two trapped scopes fight over
//      the document's single real focus.
//   2. The panel portals to document.body, OUTSIDE ThemeFlip's own
//      data-theme="light" wrapper, so an open panel would render in the
//      ambient (dark) palette in BOTH panes - the side-by-side theme
//      comparison this gallery exists for wouldn't hold for it anyway.
// The trigger stays fully live: click it to open the panel (a real toggle,
// so both open and closed are explorable), and outside-click/Escape close it.
function PopoverDemo() {
  const [open, setOpen] = useState(false);
  return (
    <div className={styles.frame}>
      <p className={styles.hint}>Click the button to open the floating panel; Escape or an outside click closes it.</p>
      <Popover
        open={open}
        onClose={() => setOpen(false)}
        trigger={
          <button type="button" onClick={() => setOpen((v) => !v)}>
            Details
          </button>
        }
      >
        <p className={styles.panelBody}>
          A floating panel, portaled out of flow so opening it never pushes page content down.
        </p>
      </Popover>
    </div>
  );
}

export default function PopoverGallerySection() {
  return (
    <section>
      <h2>Popover</h2>
      <ThemeFlip>
        <PopoverDemo />
      </ThemeFlip>
    </section>
  );
}
