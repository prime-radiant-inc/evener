import type { ReactNode } from "react";
import { requireClass } from "../widgets/internal/requireClass";
import styles from "./theme-flip.module.css";

const CLASS = {
  flip: requireClass(styles.flip, "theme-flip.module.css", "flip"),
  pane: requireClass(styles.pane, "theme-flip.module.css", "pane"),
  label: requireClass(styles.label, "theme-flip.module.css", "label"),
};

/**
 * Gallery-only utility: renders `children` twice side by side, once under
 * an explicit `data-theme="dark"` wrapper and once under a
 * `data-theme="light"` wrapper, so a widget's every state can be eyeballed
 * in both themes at once regardless of the app's ambient theme.
 * tokens.css scopes its light overrides as both `:root[data-theme="light"]`
 * and the bare attribute selector precisely so this nested flip resolves
 * correctly (light theme applied on a div nested well below <html>).
 */
export function ThemeFlip({ children }: { children: ReactNode }) {
  return (
    <div className={CLASS.flip}>
      <div className={CLASS.pane} data-theme="dark">
        <p className={CLASS.label}>Dark</p>
        {children}
      </div>
      <div className={CLASS.pane} data-theme="light">
        <p className={CLASS.label}>Light</p>
        {children}
      </div>
    </div>
  );
}
