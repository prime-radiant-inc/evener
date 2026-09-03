// The standard "open out of this surface" affordance: the box-arrow OpenIcon
// glyph, alone (the button form) or after words (the anchor form, an external
// target). Every place the UI opens something outside the current surface -
// a child transcript pane, a file doc pane, an external editor - routes
// through this one component, so a rendering change lands once.
//
// ONE treatment: icon-only, everywhere. The accessible name stays specific
// ("Open transcript", "Open beside: src/a.ts") because many open controls
// share a screen; the TOOLTIP is the one word "Open" everywhere.
//
// The touch target never reaches the line box: the .inline wrapper is the
// full hit size (28px, --tap-min on phones - the IconButton sm inside fills
// it) and its negative margin-block hands exactly 1em back to layout, so a
// row with the affordance is the height of a row without it.
//
// The affordance always rides inside something clickable (a disclosure head,
// a tool row's summary line, an activity-tree row), so it owns
// stopPropagation: a click here must never also toggle the enclosing row.
import type { MouseEvent } from "react";
import { IconButton } from "../iconbutton";
import { requireClass } from "../internal/requireClass";
import styles from "./openbutton.module.css";

const CLASS = {
  link: requireClass(styles.link, "openbutton.module.css", "link"),
  inline: requireClass(styles.inline, "openbutton.module.css", "inline"),
};

// The traditional "open out of the box" glyph - a box with its top-right
// corner open and an arrow leaving through it - in the app's 16x16 stroke
// grammar, currentColor so it inherits the Button/IconButton variant colour.
// Defaults to this control's 14px; the pane header's pop-out action
// (shell/PopoutHeaderAction.tsx) renders it at 16px.
export function OpenIcon({ size = 14 }: { size?: number }) {
  return (
    <svg viewBox="0 0 16 16" width={size} height={size} aria-hidden="true">
      <path
        d="M12.5 8.5V12.5H3.5V3.5H7.5"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
      <path
        d="M8 8L13 3M9.5 3H13V6.5"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}

export interface OpenButtonProps {
  /** Accessible name. Stay specific ("Open transcript", "Open beside:
   * src/a.ts") - many open controls share a screen. Defaults to "Open". */
  label?: string;
  /** Click handler for the button form. An href anchor navigates instead
   * and only stopPropagates. */
  onClick?: (event: MouseEvent<HTMLButtonElement>) => void;
  /** Forwards to the underlying control: activity-tree rows are their own
   * tab stop, so their nested open glyph takes -1 there. */
  tabIndex?: number;
  /** An external target renders an <a> (new tab, no opener access) instead
   * of a <button> - the settings "open in editor" case. The anchor names
   * itself from its visible words and ignores onClick. */
  href?: string;
  /** The visible words the glyph follows (anchor form only). */
  word?: string;
}

export function OpenButton({ label, onClick, tabIndex, href, word = "open" }: OpenButtonProps) {
  if (href !== undefined) {
    return (
      <a
        className={CLASS.link}
        href={href}
        target="_blank"
        rel="noopener noreferrer"
        aria-label={label}
        tabIndex={tabIndex}
        onClick={(event) => event.stopPropagation()}
      >
        {word}
        <OpenIcon />
      </a>
    );
  }
  function handleClick(event: MouseEvent<HTMLButtonElement>) {
    event.stopPropagation();
    onClick?.(event);
  }
  return (
    <span className={CLASS.inline}>
      <IconButton
        label={label ?? "Open"}
        title="Open"
        tabIndex={tabIndex}
        icon={<OpenIcon />}
        variant="quiet"
        size="sm"
        onClick={handleClick}
      />
    </span>
  );
}
