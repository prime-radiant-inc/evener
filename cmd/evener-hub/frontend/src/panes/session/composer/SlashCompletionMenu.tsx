// Inline slash-command completion menu, adapted from Beautiful UI's
// prompt-bar affordance (beautifului.dev, MIT © 2026 Shane Levine) - see
// LICENSES/beautiful-ui.txt. Composer.tsx owns all the keyboard mechanics
// (Arrow/Tab/Enter/Escape) via its own onKeyDown - this component is a pure
// rendering of the current item list plus a click-to-select affordance, a
// listbox anchored above the composer's control row (see
// slashcompletionmenu.module.css's own header comment for the float
// recipe).
//
// items is slashCompletion.ts's own merged SlashMenuItem list (2026-08-14:
// session-scoped BUILT-INS merged with the plugin catalog, mergeSlashCommands'
// own doc comment) - this component itself has no notion of which source a
// row came from beyond rendering its already-resolved label/hint/invocation.

import type { SlashMenuItem } from "@evener/appwire-client";
import { ListboxOption } from "../../../widgets/internal/listbox";
import { requireClass } from "../../../widgets/internal/requireClass";
import { optionDomId } from "../../../widgets/internal/useListbox";
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
  /** The row key the driving input's aria-activedescendant names. */
  activeKey: string | undefined;
  onSelect: (item: SlashMenuItem) => void;
}

export function SlashCompletionMenu({ id, items, activeKey, onSelect }: SlashCompletionMenuProps) {
  return (
    /* ul role="listbox" with li role="option" rows: the WAI-ARIA APG
       combobox-with-listbox-popup example markup, as in
       widgets/internal/listbox.tsx. */
    <ul
      className={CLASS.menu}
      // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: ul/li is the ARIA APG's own listbox markup, see above
      role="listbox"
      id={id}
      aria-label="Slash commands and skills"
      data-testid="composer-slash-menu"
    >
      {items.map((item) => {
        const active = item.key === activeKey;
        return (
          <ListboxOption
            key={item.key}
            id={optionDomId(id, item.key)}
            selected={active}
            active={active}
            className={CLASS.option}
            activeClassName={CLASS.optionActive}
            onSelect={() => onSelect(item)}
          >
            <span className={CLASS.name}>/{item.label}</span>
            {item.hint && <span className={CLASS.hint}>{item.hint}</span>}
          </ListboxOption>
        );
      })}
    </ul>
  );
}
