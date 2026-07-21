import { useState } from "react";
import { FocusScope } from "../../widgets/focusscope";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./focusscope.module.css";

// FocusScope has no visual chrome of its own (Dialog/Sheet/Menu own all the
// styling that wraps it) - this demo is a live interactive one instead of a
// static state grid: toggle "Trap focus" and Tab through to feel the
// difference between focus looping inside the scope versus leaving it.
function FocusScopeDemo() {
  const [trap, setTrap] = useState(true);
  // Remounting on every trap flip re-triggers FocusScope's mount-time
  // "focus the first tabbable element" behavior, so flipping the checkbox
  // always leaves the demo in a freshly-focused, easy-to-explore state.
  const [generation, setGeneration] = useState(0);

  return (
    <div className={styles.demo}>
      <label className={styles.label}>
        <input
          type="checkbox"
          checked={trap}
          onChange={(event) => {
            setTrap(event.target.checked);
            setGeneration((g) => g + 1);
          }}
        />
        Trap focus
      </label>
      <p className={styles.hint}>
        {trap
          ? "Tab/Shift+Tab loop between the three fields below - they never reach “Outside”."
          : "Tab/Shift+Tab move through the three fields below and on to “Outside” normally."}
      </p>
      <div className={styles.row}>
        <FocusScope key={generation} trap={trap}>
          <input placeholder="First" />
          <input placeholder="Second" />
          <input placeholder="Third" />
        </FocusScope>
        <input placeholder="Outside" />
      </div>
    </div>
  );
}

export default function FocusScopeGallerySection() {
  return (
    <section>
      <h2>FocusScope</h2>
      <ThemeFlip>
        <FocusScopeDemo />
      </ThemeFlip>
    </section>
  );
}
