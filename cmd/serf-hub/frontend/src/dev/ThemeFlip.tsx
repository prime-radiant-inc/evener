import type { ReactNode } from "react";
import styles from "./theme-flip.module.css";

// CSS Modules import as an index signature, so under this project's
// noUncheckedIndexedAccess every styles.foo access is string | undefined
// (see src/widgets/cadence/index.tsx's requireClass for the full story).
function requireClass(value: string | undefined, name: string): string {
  if (value === undefined) throw new Error(`theme-flip.module.css is missing the "${name}" class`);
  return value;
}

const CLASS = {
  flip: requireClass(styles.flip, "flip"),
  pane: requireClass(styles.pane, "pane"),
  label: requireClass(styles.label, "label"),
};

/**
 * Gallery-only utility: renders `children` twice side by side, once under
 * the ambient (dark) theme and once wrapped in a `data-theme="light"` div,
 * so a widget's every state can be eyeballed in both themes at once.
 * tokens.css scopes its light overrides as both `:root[data-theme="light"]`
 * and the bare attribute selector precisely so this nested flip resolves
 * correctly (light theme applied on a div nested well below <html>).
 */
export function ThemeFlip({ children }: { children: ReactNode }) {
  return (
    <div className={CLASS.flip}>
      <div className={CLASS.pane}>
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
