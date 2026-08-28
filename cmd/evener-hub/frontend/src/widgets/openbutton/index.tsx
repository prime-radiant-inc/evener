// The standard "open out of this surface" affordance: the box-arrow OpenIcon
// glyph, alone (iconOnly, for dense rows) or after a word ("open",
// "open in editor"). Every place the UI opens something outside the current
// surface - a child transcript pane, a file doc pane, an external editor -
// routes through this one component, so a rendering change lands once.
//
// The affordance always rides inside something clickable (a disclosure head,
// a tool row's summary line, an activity-tree row), so it owns
// stopPropagation: a click here must never also toggle the enclosing row.
import type { MouseEvent } from "react";
import { Button } from "../button";
import { IconButton } from "../iconbutton";
import { requireClass } from "../internal/requireClass";
import styles from "./openbutton.module.css";

const CLASS = {
  link: requireClass(styles.link, "openbutton.module.css", "link"),
};

// The traditional "open out of the box" glyph - a box with its top-right
// corner open and an arrow leaving through it - in the app's 16x16 stroke
// grammar (same geometry as PopoutHeaderAction.tsx's PopoutIcon, at this
// control's 14px size), currentColor so it inherits the Button/IconButton
// variant colour exactly as the text label it replaced did (kata 3qnd - the
// surrounding pane chrome, Pop out/Fork from here, is all icons; this was
// the one text label left).
export function OpenIcon() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
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
  /** Accessible name. Word form: an aria-label more specific than the bare
   * word (many "open" controls share a screen) - defaults to `word`.
   * iconOnly: the control's ONLY name (required there by IconButton). */
  label?: string;
  /** The visible word the glyph follows (word and anchor forms). */
  word?: string;
  /** Dense-row form: just the glyph, `label` as the accessible name, for
   * surfaces (the activity tree's first line) with no room for the word. */
  iconOnly?: boolean;
  /** iconOnly density: xs rides a text row (activity tree), sm stands in
   * pane chrome or a tool row's trailing slot. The word form is always
   * Button sm. */
  size?: "xs" | "sm";
  /** An external target renders an <a> (new tab, no opener access) instead
   * of a <button> - the settings "open in editor" case. */
  href?: string;
  onClick?: (event: MouseEvent<HTMLButtonElement>) => void;
  /** Forwards to the underlying control: activity-tree rows are their own
   * tab stop, so their nested open glyph takes -1 there. */
  tabIndex?: number;
  /** Hover text; iconOnly defaults it to `label` (its only visible hint). */
  title?: string;
}

export function OpenButton({
  label,
  word = "open",
  iconOnly = false,
  size = "sm",
  href,
  onClick,
  tabIndex,
  title,
}: OpenButtonProps) {
  if (href !== undefined) {
    // The anchor names itself from its visible words unless a more specific
    // label is given; the glyph is aria-hidden, so it adds nothing.
    return (
      <a
        className={CLASS.link}
        href={href}
        target="_blank"
        rel="noopener noreferrer"
        aria-label={label}
        title={title}
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
  if (iconOnly) {
    return (
      <IconButton
        label={label ?? word}
        title={title ?? label ?? word}
        tabIndex={tabIndex}
        icon={<OpenIcon />}
        variant="quiet"
        size={size}
        onClick={handleClick}
      />
    );
  }
  return (
    <Button
      variant="quiet"
      size="sm"
      aria-label={label ?? word}
      title={title}
      tabIndex={tabIndex}
      onClick={handleClick}
    >
      {word}
      <OpenIcon />
    </Button>
  );
}
