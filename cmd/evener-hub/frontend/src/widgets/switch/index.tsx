import { useId } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./switch.module.css";

export interface SwitchProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  /** The control's own activation started the work that made it unavailable (a
   * toggle whose RPC is out): the state is a REFUSAL - aria-disabled plus the
   * guard in toggle() - so the switch keeps the keyboard the click put on it
   * instead of dropping it to <body>, which would make the surface put focus
   * back somewhere else and scroll there. */
  pending?: boolean;
  /** Unavailable for a reason of its own: a read-only surface, a refused write,
   * another control's work in flight. The native attribute, so the control is
   * inert and out of the tab order like any disabled control. */
  disabled?: boolean;
  /** Always-visible text, and the switch's sole accessible name (via
   * aria-labelledby) - kept required so an unlabeled switch can't ship. */
  label: string;
}

const BASE_CLASS = {
  wrapper: requireClass(styles.wrapper, "switch.module.css", "wrapper"),
  control: requireClass(styles.control, "switch.module.css", "control"),
  track: requireClass(styles.track, "switch.module.css", "track"),
  thumb: requireClass(styles.thumb, "switch.module.css", "thumb"),
  label: requireClass(styles.label, "switch.module.css", "label"),
};

/**
 * A binary on/off control: a native <button role="switch"> (not a styled
 * checkbox), so Space/Enter toggling comes from ordinary browser button
 * semantics rather than a hand-rolled keydown handler. aria-labelledby points
 * at the visible label span rather than relying on implicit HTML
 * label-wrapping, whose accessible-name behavior for a non-native-checkbox
 * role is inconsistent across browsers/jsdom.
 *
 * Two unavailability states, deliberately different: `pending` is the refusal
 * above (keeps the keyboard, stays tabbable), `disabled` is the native
 * attribute (inert, out of the tab order). A control whose OWN activation made
 * it unavailable is the first; everything else is the second.
 */
export function Switch({ checked, onChange, pending = false, disabled = false, label }: SwitchProps) {
  const labelId = useId();
  const refused = pending || disabled;

  // Both states block activation here: `pending` has no native attribute to
  // block it with - that is the point, a natively disabled control cannot hold
  // focus (Chrome drops it to <body>, and the surface behind it then puts focus
  // back somewhere else, scrolling there) - and the label span below is a plain
  // <span> with no native disabled concept at all.
  function toggle() {
    if (!refused) onChange(!checked);
  }

  return (
    <span className={BASE_CLASS.wrapper}>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-labelledby={labelId}
        aria-disabled={pending || undefined}
        disabled={disabled}
        className={BASE_CLASS.control}
        onClick={toggle}
      >
        <span className={BASE_CLASS.track} aria-hidden="true">
          <span className={BASE_CLASS.thumb} />
        </span>
      </button>
      {/* Mouse-only convenience mirroring a native <label for=...> click
          target for the switch above (see this file's own top comment for
          why a real <label> wrapper isn't used instead) - the actual
          control is the fully keyboard-operable button; this span gives
          mouse/touch users the same larger, click-anywhere-on-the-label
          hit target a native form control's label would. */}
      {/* biome-ignore lint/a11y/noStaticElementInteractions: mouse-only label click target, the real control is the button above */}
      {/* biome-ignore lint/a11y/useKeyWithClickEvents: mouse-only label click target, the real control is the button above */}
      <span id={labelId} className={BASE_CLASS.label} onClick={toggle}>
        {label}
      </span>
    </span>
  );
}
