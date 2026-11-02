// Inline slash-command completion menu, adapted from Beautiful UI's
// prompt-bar affordance (beautifului.dev, MIT © 2026 Shane Levine) - see
// LICENSES/beautiful-ui.txt. Composer.tsx owns all the keyboard mechanics
// (Arrow/Tab/Enter/Escape) via its own onKeyDown - this component is a pure
// rendering of the current item list plus a click-to-select affordance, a
// listbox pinned above the composer's top edge.
//
// The listbox renders through a portal at its anchor's document level: the
// comic seat mounts the shared Composer inside a bounded, scrolling
// interaction area (max-height + overflow), and a popup inside that subtree
// would be clipped by it. A placement loop pins the box to the anchor's live
// rect every frame while the menu is open - typing can grow the draft and
// move the composer's top edge mid-menu - so the box always sits exactly
// where the old in-flow anchor put it, visible above any ancestor.
//
// items is slashCompletion.ts's own merged SlashMenuItem list (2026-08-14:
// session-scoped BUILT-INS merged with the plugin catalog, mergeSlashCommands'
// own doc comment) - this component itself has no notion of which source a
// row came from beyond rendering its already-resolved label/hint/invocation.

import type { SlashMenuItem } from "@evener/appwire-client";
import type { RefObject } from "react";
import { useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./slashcompletionmenu.module.css";

const CLASS = {
  menu: requireClass(styles.menu, "slashcompletionmenu.module.css", "menu"),
  option: requireClass(styles.option, "slashcompletionmenu.module.css", "option"),
  optionActive: requireClass(styles.optionActive, "slashcompletionmenu.module.css", "optionActive"),
  name: requireClass(styles.name, "slashcompletionmenu.module.css", "name"),
  hint: requireClass(styles.hint, "slashcompletionmenu.module.css", "hint"),
};

export interface SlashCompletionMenuProps {
  id: string;
  items: SlashMenuItem[];
  highlightedIndex: number;
  onSelect: (item: SlashMenuItem) => void;
  /** The composer's form anchor, whose live rect the portaled box tracks. */
  anchorRef: RefObject<HTMLElement | null>;
}

// optionId is exported so Composer.tsx's own aria-activedescendant wiring
// (set imperatively on the textarea DOM node it already refs - Textarea
// itself takes no aria-activedescendant prop, and widgets/ is out of this
// stream's scope) can point at exactly the id this component renders,
// without either side hand-duplicating the "<id>-option-<index>" scheme.
export function optionId(listboxId: string, index: number): string {
  return `${listboxId}-option-${index}`;
}

export function SlashCompletionMenu({ id, items, highlightedIndex, onSelect, anchorRef }: SlashCompletionMenuProps) {
  const menuRef = useRef<HTMLDivElement>(null);
  const anchor = anchorRef.current;
  useEffect(() => {
    const menu = menuRef.current;
    if (!menu || !anchor) return;
    let frame = 0;
    const place = () => {
      const rect = anchor.getBoundingClientRect();
      const view = anchor.ownerDocument.defaultView;
      const gap = Number.parseFloat(getComputedStyle(menu).getPropertyValue("--space-1")) || 0;
      menu.style.left = `${rect.left}px`;
      menu.style.width = `${rect.width}px`;
      menu.style.bottom = `${(view?.innerHeight ?? 0) - rect.top - gap}px`;
      // The anchor inherits its surface's font stack (the comic seat's own
      // token); the portaled box would otherwise fall back to the app root's.
      menu.style.fontFamily = getComputedStyle(anchor).fontFamily;
      frame = requestAnimationFrame(place);
    };
    frame = requestAnimationFrame(place);
    return () => cancelAnimationFrame(frame);
  }, [anchor]);
  if (!anchor) return null;
  return createPortal(
    <div
      ref={menuRef}
      className={CLASS.menu}
      role="listbox"
      id={id}
      aria-label="Slash commands and skills"
      data-testid="composer-slash-menu"
    >
      {items.map((item, index) => {
        const active = index === highlightedIndex;
        return (
          <button
            key={item.key}
            type="button"
            id={optionId(id, index)}
            role="option"
            aria-selected={active}
            className={active ? `${CLASS.option} ${CLASS.optionActive}` : CLASS.option}
            // Prevents the textarea from ever blurring on this click, so the
            // composer's own blur-closes-the-menu handler never races the
            // click's own onSelect - focus simply never leaves the field.
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => onSelect(item)}
          >
            <span className={CLASS.name}>/{item.label}</span>
            {item.hint && <span className={CLASS.hint}>{item.hint}</span>}
          </button>
        );
      })}
    </div>,
    anchor.ownerDocument.body,
  );
}
