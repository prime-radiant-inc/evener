import { Fragment } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./keyhint.module.css";

export interface KeyHintProps {
  keys: string[];
  /** Render the chord as one bare glyph run (⇧↵, ⌘↵) instead of bordered
   * <kbd> boxes with "+" separators - for a hint that sits INSIDE another
   * control (a Send/Steer button), where three nested boxes dominate the
   * button they annotate. The glyph run is aria-hidden and the same words
   * the bordered form shows ride along visually-hidden, so an enclosing
   * control's accessible name is identical either way. */
  compact?: boolean;
}

const BASE_CLASS = {
  keyHint: requireClass(styles.keyHint, "keyhint.module.css", "keyHint"),
  key: requireClass(styles.key, "keyhint.module.css", "key"),
  separator: requireClass(styles.separator, "keyhint.module.css", "separator"),
  glyphs: requireClass(styles.glyphs, "keyhint.module.css", "glyphs"),
  srOnly: requireClass(styles.srOnly, "keyhint.module.css", "srOnly"),
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

// The compact form's glyphs, matching the command palette's own help rows
// (shell/palette/CommandPalette.tsx's HELP_ROWS use these same literals).
// Mod goes through displayOf so the platform split stays in one place; any
// key with no glyph here renders verbatim, exactly as the bordered form does.
const GLYPH: Record<string, string> = {
  Shift: "⇧",
  Enter: "↵",
};

function glyphOf(key: string): string {
  return GLYPH[key] ?? displayOf(key);
}

/** A keyboard-shortcut hint: one <kbd> per key, "+" separated. Informational
 * - no interaction, no focus ring. */
export function KeyHint({ keys, compact = false }: KeyHintProps) {
  if (compact) {
    return (
      <span className={BASE_CLASS.keyHint}>
        <span className={BASE_CLASS.glyphs} aria-hidden="true">
          {keys.map(glyphOf).join("")}
        </span>
        {/* The words, not the glyphs, are what an enclosing control's
            accessible name must carry - "Steer ⇧↵" is unspeakable. Same
            text the bordered form renders visibly. */}
        <span className={BASE_CLASS.srOnly}>{keys.map(displayOf).join("+")}</span>
      </span>
    );
  }

  return (
    <span className={BASE_CLASS.keyHint}>
      {keys.map((key, i) => (
        // keys is a caller-supplied literal chord (e.g. ["Mod", "K"]) -
        // its order IS the shortcut's meaning, fixed by whoever renders
        // this KeyHint, never reordered independently of the prop itself.
        // biome-ignore lint/suspicious/noArrayIndexKey: order is the shortcut's own meaning, see above
        <Fragment key={i}>
          {i > 0 && <span className={BASE_CLASS.separator}>+</span>}
          <kbd className={BASE_CLASS.key}>{displayOf(key)}</kbd>
        </Fragment>
      ))}
    </span>
  );
}
