// The presentational half of the shared listbox: the role="listbox"
// container and the virtual-focus option row. Every site renders its own row
// content (group heads, diagnostics, icons, metadata); this owns only the
// ARIA/DOM contract the sites had copied.
//
// Real DOM focus never leaves the driving input (the ARIA 1.2
// activedescendant pattern), so an option is deliberately not focusable and
// needs no key handler of its own. The mousedown guard is what keeps a mouse
// pick from blurring the input.

import type { JSX, ReactNode } from "react";
import styles from "./listbox.module.css";
import { requireClass } from "./requireClass";

const CLASS = {
  list: requireClass(styles.list, "listbox.module.css", "list"),
};

export interface ListboxProps {
  id: string;
  /** The listbox's accessible name ("Model", "Path"). */
  label: string;
  /** Extra classes for a caller's variant (ModelCatalog's sheet list). */
  className?: string;
  children: ReactNode;
}

export function Listbox({ id, label, className, children }: ListboxProps): JSX.Element {
  return (
    // <ul role="listbox">/<li role="option"> is the WAI-ARIA APG
    // combobox-with-listbox-popup pattern's own example markup
    // (w3.org/WAI/ARIA/apg/patterns/combobox) - not an interactive role
    // bolted onto an arbitrary static element, Biome's role-vs-element
    // heuristic just doesn't special-case ul/li for it.
    <ul
      // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: ul/li is the ARIA APG's own listbox markup, see above
      role="listbox"
      id={id}
      aria-label={label}
      className={className === undefined ? CLASS.list : `${CLASS.list} ${className}`}
    >
      {children}
    </ul>
  );
}

export interface ListboxOptionProps {
  id: string;
  /** The current value's row (aria-selected). A single-select listbox marks
   * exactly one row, even when the same value appears in two groups. */
  selected: boolean;
  /** The virtual-focus row (aria-activedescendant). */
  active: boolean;
  className: string;
  activeClassName: string;
  onSelect: () => void;
  children: ReactNode;
}

export function ListboxOption({
  id,
  selected,
  active,
  className,
  activeClassName,
  onSelect,
  children,
}: ListboxOptionProps): JSX.Element {
  return (
    // biome-ignore lint/a11y/useFocusableInteractive: activedescendant pattern, real focus stays on the input, see above
    // biome-ignore lint/a11y/useKeyWithClickEvents: activedescendant pattern, Enter on the input already does this, see above
    <li
      id={id}
      // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: ARIA APG listbox markup, see the ul above
      role="option"
      aria-selected={selected}
      className={active ? `${className} ${activeClassName}` : className}
      // Selecting with the mouse must not blur the input (the ARIA 1.2
      // pattern keeps real focus there throughout).
      onMouseDown={(event) => event.preventDefault()}
      onClick={onSelect}
    >
      {children}
    </li>
  );
}
