// Inline slash-command completion menu, adapted from Beautiful UI's
// prompt-bar affordance (beautifului.dev, MIT © 2026 Shane Levine) - see
// LICENSES/beautiful-ui.txt. Composer.tsx owns all the keyboard mechanics
// (Arrow/Tab/Enter/Escape) via its own onKeyDown - this component is a pure
// rendering of the current item list plus a click-to-select affordance, a
// listbox anchored above the composer's control row (see
// slashcompletionmenu.module.css's own header comment for the float
// recipe).
import type { CommandDescriptor } from "../../../protocol/types.gen";
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
  items: CommandDescriptor[];
  highlightedIndex: number;
  onSelect: (item: CommandDescriptor) => void;
}

// optionId is exported so Composer.tsx's own aria-activedescendant wiring
// (set imperatively on the textarea DOM node it already refs - Textarea
// itself takes no aria-activedescendant prop, and widgets/ is out of this
// stream's scope) can point at exactly the id this component renders,
// without either side hand-duplicating the "<id>-option-<index>" scheme.
export function optionId(listboxId: string, index: number): string {
  return `${listboxId}-option-${index}`;
}

export function SlashCompletionMenu({ id, items, highlightedIndex, onSelect }: SlashCompletionMenuProps) {
  return (
    <div className={CLASS.menu} role="listbox" id={id} aria-label="Slash commands" data-testid="composer-slash-menu">
      {items.map((item, index) => {
        const active = index === highlightedIndex;
        const hint = item.description || item.argumentHint;
        return (
          <button
            key={item.name}
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
            <span className={CLASS.name}>/{item.name}</span>
            {hint && <span className={CLASS.hint}>{hint}</span>}
          </button>
        );
      })}
    </div>
  );
}
