import { KeyHint } from "../../widgets/keyhint";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./keyhint.module.css";

// "Mod" resolves against the reviewing browser's actual platform (⌘ on
// macOS, Ctrl elsewhere) - there's no prop to force the other branch, so
// this row shows whichever this machine actually is; the other rows use
// concrete keys to demonstrate the separator and single-key cases without
// depending on platform.
const SAMPLES: { label: string; keys: string[] }[] = [
  { label: "save (Mod)", keys: ["Mod", "S"] },
  { label: "multi-key", keys: ["Mod", "Shift", "K"] },
  { label: "single key", keys: ["Enter"] },
];

export default function KeyHintGallerySection() {
  return (
    <section>
      <h2>KeyHint</h2>
      <ThemeFlip>
        {SAMPLES.map(({ label, keys }) => (
          <div className={styles.row} key={label}>
            <p className={styles.rowLabel}>{label}</p>
            <KeyHint keys={keys} />
          </div>
        ))}
      </ThemeFlip>
    </section>
  );
}
