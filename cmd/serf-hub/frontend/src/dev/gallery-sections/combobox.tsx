import { useEffect, useRef } from "react";
import { Combobox, type ComboboxOption } from "../../widgets/combobox";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./combobox.module.css";

const MODELS: ComboboxOption[] = [
  { id: "opus", label: "Claude Opus" },
  { id: "sonnet", label: "Claude Sonnet" },
  { id: "haiku", label: "Claude Haiku" },
];

// Combobox has no controlled "open" prop (it opens from typing or
// ArrowDown/ArrowUp, per its locked API) - shown open here by genuinely
// focusing the real input and dispatching a real ArrowDown keydown, the
// same path a keyboard user takes to browse without typing first (see the
// widget's own "ArrowDown on a closed-but-populated combobox" behavior).
// Found via role="combobox" (part of its accessibility contract), not an
// internal prop that doesn't exist.
function ComboboxDemo() {
  const frameRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const input = frameRef.current?.querySelector<HTMLInputElement>('[role="combobox"]');
    if (!input) return;
    input.focus();
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
  }, []);

  return (
    <div ref={frameRef} className={styles.frame}>
      <label className={styles.label}>
        Model
        <Combobox options={MODELS} onQuery={() => {}} onPick={() => {}} />
      </label>
    </div>
  );
}

export default function ComboboxGallerySection() {
  return (
    <section>
      <h2>Combobox</h2>
      <ThemeFlip>
        <ComboboxDemo />
      </ThemeFlip>
    </section>
  );
}
