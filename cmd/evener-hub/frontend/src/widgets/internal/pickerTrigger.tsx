// The one closed-state trigger every picker in the app draws: a button whose
// visible value is the current choice, a chevron, and a screen-reader-only
// action suffix after it. Three call sites carried the same markup with
// drifted ARIA before this existed - PathField set aria-haspopup/expanded for
// its directory dialog, ModelCatalog and ModelSwitchTrigger set neither - so
// the attributes are owned here and every caller gets the correct ones.
//
// The whitespace text node between the chevron and the action is load-bearing:
// the accessible name is the button's children concatenated, and each child's
// own text is trimmed first, so a space INSIDE either span would be dropped and
// the name would run together as "/home/jesse— browse". A whitespace-only text
// node between them survives that trim and renders nothing of its own (an
// all-whitespace anonymous flex item is not laid out).
//
// The action suffix is VisuallyHidden - the design system's one recipe for
// assistive-tech-only text - for every trigger, with the whitespace immediately
// before it. There is no second sr-only rendering path.
//
// The visual recipes stay with their owners instead of being copied here: each
// caller passes its own trigger/value/chevron classes, and composes the
// default-value ink into the value class itself, because the panes' responsive
// contracts (ModelSwitch's shrink and tap-floor rules, PathField's wrapped
// directory value, ModelCatalog's field slot) live in the stylesheet that owns
// them. This widget owns the structure and the ARIA, which is where the three
// copies had actually drifted.
//
// The slot box is the exception, because the structure needs it: the trigger is
// always one span, from the widget, on every surface that mounts it. A caller
// whose slot sizes the trigger directly (the status row's `.identity > span`)
// therefore sizes this span rather than a per-caller shim, and `fill` gives the
// form-field callers the width they need. See pickerTrigger.module.css.
//
// Internal rather than barrelled? ModelSwitchTrigger lives outside src/widgets,
// so the barrel exports this one; Listbox/useListbox stay internal because only
// widgets use them.

import type { JSX, ReactNode, Ref } from "react";
import { Chevron } from "../chevron";
import { VisuallyHidden } from "../visuallyhidden";
import styles from "./pickerTrigger.module.css";
import { requireClass } from "./requireClass";

const CLASS = {
  wrapper: requireClass(styles.wrapper, "pickerTrigger.module.css", "wrapper"),
  wrapperFill: requireClass(styles.wrapperFill, "pickerTrigger.module.css", "wrapperFill"),
};

/** What the trigger opens, for aria-haspopup: the model catalog's listbox
 * popup, or PathField's directory dialog. */
export type PickerTriggerAriaMode = "listbox" | "dialog";

export interface PickerTriggerClassNames {
  /** The button's own closed-state recipe. */
  trigger: string;
  /** The visible value span, with any kind/default class already composed. */
  value: string;
  chevron: string;
}

export interface PickerTriggerProps {
  /** Whether the picker this trigger opens is currently open. */
  open: boolean;
  onClick: () => void;
  ariaMode: PickerTriggerAriaMode;
  /** The action after the em dash in the accessible name ("change model",
   * "browse"). */
  actionLabel: string;
  /** The visible value; the caller decides what an empty value reads as. */
  value: ReactNode;
  classes: PickerTriggerClassNames;
  /** Stretch the slot box across what it was given instead of hugging the
   * label. The form-field pickers and the spawn card's model slot set it. */
  fill?: boolean;
  /** data-state for the value span (ModelCatalog's "default" marker). */
  valueState?: string;
  disabled?: boolean;
  title?: string;
  id?: string;
  /** Replaces the computed accessible name. Callers that need the field's own
   * name in front of the value compose it themselves. */
  ariaLabel?: string;
  ref?: Ref<HTMLButtonElement>;
  "data-testid"?: string;
  /** Hook for the visible value span, so a test can read the value without the
   * screen-reader suffix riding along in the button's textContent. */
  valueTestId?: string;
}

export function PickerTrigger({
  open,
  onClick,
  ariaMode,
  actionLabel,
  value,
  classes,
  fill = false,
  valueState,
  disabled = false,
  title,
  id,
  ariaLabel,
  ref,
  "data-testid": testId,
  valueTestId,
}: PickerTriggerProps): JSX.Element {
  return (
    <span className={`${CLASS.wrapper}${fill ? ` ${CLASS.wrapperFill}` : ""}`}>
      <button
        ref={ref}
        id={id}
        type="button"
        className={classes.trigger}
        disabled={disabled}
        title={title}
        aria-haspopup={ariaMode}
        aria-expanded={open}
        aria-label={ariaLabel}
        data-testid={testId}
        onClick={onClick}
      >
        <span className={classes.value} data-testid={valueTestId} data-state={valueState}>
          {value}
        </span>
        <span className={classes.chevron} aria-hidden="true">
          <Chevron direction="down" />
        </span>{" "}
        <VisuallyHidden>— {actionLabel}</VisuallyHidden>
      </button>
    </span>
  );
}
