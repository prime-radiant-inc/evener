import { useId } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./switch.module.css";

export interface SwitchProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  /** Always-visible text, and the switch's sole accessible name (via
   * aria-labelledby) - kept required so an unlabeled switch can't ship. */
  label: string;
}

const BASE_CLASS = {
  wrapper: requireClass(styles.wrapper, "switch.module.css", "wrapper"),
  track: requireClass(styles.track, "switch.module.css", "track"),
  thumb: requireClass(styles.thumb, "switch.module.css", "thumb"),
  label: requireClass(styles.label, "switch.module.css", "label"),
};

/**
 * A binary on/off control: a native <button role="switch"> (not a styled
 * checkbox), so Space/Enter toggling and disabled-blocks-activation both
 * come from ordinary browser button semantics, not a hand-rolled keydown
 * handler. aria-labelledby points at the visible label span rather than
 * relying on implicit HTML label-wrapping, whose accessible-name behavior
 * for a non-native-checkbox role is inconsistent across browsers/jsdom.
 */
export function Switch({ checked, onChange, disabled = false, label }: SwitchProps) {
  const labelId = useId();

  // The <button>'s own disabled attribute already blocks its native click
  // (and Space/Enter, which activate via that same click) - but the label
  // span is a plain <span>, not a form control, so it has no native
  // disabled concept and needs the guard here instead.
  function toggle() {
    if (!disabled) onChange(!checked);
  }

  return (
    <span className={BASE_CLASS.wrapper}>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-labelledby={labelId}
        disabled={disabled}
        className={BASE_CLASS.track}
        onClick={toggle}
      >
        <span className={BASE_CLASS.thumb} />
      </button>
      <span id={labelId} className={BASE_CLASS.label} onClick={toggle}>
        {label}
      </span>
    </span>
  );
}
