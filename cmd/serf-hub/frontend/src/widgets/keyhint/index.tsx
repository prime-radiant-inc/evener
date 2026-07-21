import { Fragment } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./keyhint.module.css";

export interface KeyHintProps {
  keys: string[];
}

const BASE_CLASS = {
  keyHint: requireClass(styles.keyHint, "keyhint.module.css", "keyHint"),
  key: requireClass(styles.key, "keyhint.module.css", "key"),
  separator: requireClass(styles.separator, "keyhint.module.css", "separator"),
};

// "Mod" is the one platform-split key name this widget understands: the
// primary modifier, ⌘ on macOS and Ctrl everywhere else. Every other key
// name (e.g. "Shift", "K", "Enter") renders verbatim - KeyHint doesn't
// attempt to prettify the rest of the keyboard.
const MOD_KEY = "Mod";

function isApplePlatform(): boolean {
  return /Mac|iPhone|iPad|iPod/.test(window.navigator.platform);
}

function displayOf(key: string): string {
  if (key !== MOD_KEY) return key;
  return isApplePlatform() ? "⌘" : "Ctrl";
}

/** A keyboard-shortcut hint: one <kbd> per key, "+" separated. Informational
 * - no interaction, no focus ring. */
export function KeyHint({ keys }: KeyHintProps) {
  return (
    <span className={BASE_CLASS.keyHint}>
      {keys.map((key, i) => (
        <Fragment key={i}>
          {i > 0 && <span className={BASE_CLASS.separator}>+</span>}
          <kbd className={BASE_CLASS.key}>{displayOf(key)}</kbd>
        </Fragment>
      ))}
    </span>
  );
}
