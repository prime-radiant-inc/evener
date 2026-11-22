// A field-shaped dropdown over a list of plain rows. The closed trigger is the
// shared picker-trigger box (widgets/internal/fieldTrigger.module.css), so it
// lines up with Input/Select beside it; the open panel is a floating Popover
// of ListRows. Unlike Select it can hold rows with their own action: an
// offline host's Connect sits on its row without making the row a choice.
//
// Spawn's host picker is the first caller: every source is in one list, the
// online ones select the launch target, and each offline one carries Connect.

import { type KeyboardEvent, type ReactNode, useState } from "react";
import { Chevron } from "../chevron";
import { requireClass } from "../internal/requireClass";
import { ListRow } from "../listrow";
import { Popover } from "../popover";
import styles from "./listpicker.module.css";

interface ListPickerOptionBase {
  id: string;
  /** One line, truncating. */
  label: string;
  /** One secondary line under the label. */
  meta?: ReactNode;
}

/** A row that selects its id and closes the picker. */
export interface ListPickerChoice extends ListPickerOptionBase {
  onSelect: () => void;
  action?: never;
}

/** A row that cannot be selected; its one control is `action`. */
export interface ListPickerActionRow extends ListPickerOptionBase {
  onSelect?: never;
  action?: ReactNode;
}

export type ListPickerOption = ListPickerChoice | ListPickerActionRow;

export interface ListPickerProps {
  /** The trigger's id, for a caller that associates its own label. */
  id: string;
  /** The field's name: names the trigger and the options group. */
  label: string;
  /** The chosen option's id. */
  value: string;
  options: readonly ListPickerOption[];
  /** The trigger's text when `value` matches no option. */
  emptyLabel?: string;
  /** A final action after a separating rule, e.g. "Connect host". It runs
   * after the picker closes and never changes `value` itself. */
  footer?: { label: string; onSelect: () => void };
  disabled?: boolean;
  "data-testid"?: string;
}

const CLASS = {
  trigger: requireClass(styles.trigger, "listpicker.module.css", "trigger"),
  value: requireClass(styles.value, "listpicker.module.css", "value"),
  default: requireClass(styles.default, "listpicker.module.css", "default"),
  chevron: requireClass(styles.chevron, "listpicker.module.css", "chevron"),
  popoverPanel: requireClass(styles.popoverPanel, "listpicker.module.css", "popoverPanel"),
  panel: requireClass(styles.panel, "listpicker.module.css", "panel"),
  list: requireClass(styles.list, "listpicker.module.css", "list"),
  separator: requireClass(styles.separator, "listpicker.module.css", "separator"),
};

/**
 * A control-shaped trigger that opens a list of rows. Selecting a choice row
 * closes the picker and runs its `onSelect`; a row with its own `action` (an
 * offline host's Connect) stays open and does not change the value. The
 * `footer` is one final action after a rule. Arrow/Home/End move through every
 * enabled button in the panel, including row actions and the footer.
 */
export function ListPicker({
  id,
  label,
  value,
  options,
  emptyLabel = "(none)",
  footer,
  disabled = false,
  "data-testid": testId,
}: ListPickerProps) {
  const [open, setOpen] = useState(false);
  const panelId = `${id}-options`;
  const selected = options.find((option) => option.id === value);
  const text = selected?.label ?? emptyLabel;

  function handleKeyDown(event: KeyboardEvent<HTMLFieldSetElement>): void {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp" && event.key !== "Home" && event.key !== "End") {
      return;
    }
    const controls = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>("button:not(:disabled)"));
    if (controls.length === 0) return;
    event.preventDefault();
    const current = (event.target as HTMLElement).closest("button");
    const index = current === null ? -1 : controls.indexOf(current as HTMLButtonElement);
    let next: number;
    if (event.key === "Home") next = 0;
    else if (event.key === "End") next = controls.length - 1;
    else if (event.key === "ArrowDown") next = index < 0 ? 0 : (index + 1) % controls.length;
    else next = index < 0 ? controls.length - 1 : (index - 1 + controls.length) % controls.length;
    controls[next]?.focus();
  }

  function choose(option: ListPickerChoice): void {
    setOpen(false);
    option.onSelect();
  }

  return (
    <Popover
      open={open}
      onClose={() => setOpen(false)}
      closeOnScroll={false}
      stretchTrigger
      trigger={
        <button
          id={id}
          type="button"
          className={CLASS.trigger}
          data-testid={testId}
          aria-label={`${label}: ${text}`}
          aria-expanded={open}
          aria-controls={panelId}
          disabled={disabled}
          onClick={() => setOpen((current) => !current)}
        >
          <span className={`${CLASS.value} ${selected === undefined ? CLASS.default : ""}`}>{text}</span>
          <span className={CLASS.chevron} aria-hidden="true">
            <Chevron direction="down" />
          </span>
        </button>
      }
    >
      <div className={CLASS.popoverPanel}>
        <fieldset id={panelId} aria-label={`${label} options`} className={CLASS.panel} onKeyDown={handleKeyDown}>
          <ul className={CLASS.list}>
            {options.map((option) => (
              <li key={option.id}>
                {option.onSelect ? (
                  <ListRow
                    variant="plain"
                    label={option.label}
                    meta={option.meta}
                    aria-current={option.id === value ? "true" : undefined}
                    onSelect={() => choose(option)}
                  />
                ) : (
                  <ListRow variant="plain" label={option.label} meta={option.meta} action={option.action} />
                )}
              </li>
            ))}
          </ul>
          {footer && (
            <>
              <hr className={CLASS.separator} />
              <ListRow
                variant="plain"
                label={footer.label}
                onSelect={() => {
                  setOpen(false);
                  footer.onSelect();
                }}
              />
            </>
          )}
        </fieldset>
      </div>
    </Popover>
  );
}
