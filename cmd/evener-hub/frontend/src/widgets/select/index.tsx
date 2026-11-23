import type { ChangeEvent } from "react";
import { Chevron } from "../chevron";
import { requireClass } from "../internal/requireClass";
import styles from "./select.module.css";

export interface SelectOption {
  value: string;
  label: string;
  /** Listed but not pickable - a choice that exists but is unavailable right
   * now (an offline host). Say why in the label, since a native option has no
   * room for anything else. */
  disabled?: boolean;
}

export interface SelectProps {
  value: string;
  onChange: (event: ChangeEvent<HTMLSelectElement>) => void;
  options: SelectOption[];
  disabled?: boolean;
  id?: string;
  name?: string;
  /** The quiet variant shows the current value as the control, with a chevron
   * and a transparent native <select> laid over it. The default variant is the
   * bordered field every existing caller renders. */
  variant?: "default" | "quiet";
  /** Quiet only: the closed-state text. Defaults to the selected option's
   * label. The effort controls override it because an unset effort reads as
   * the tier it resolves to while the open option keeps its own distinct
   * "<tier> (default)" label. */
  readout?: string;
  /** Names the native control when there is no visible <label>. */
  ariaLabel?: string;
  /** Test hook for the native control (default) or the quiet trigger (quiet). */
  "data-testid"?: string;
  /** Test hook for the quiet variant's visible readout, which is aria-hidden
   * because the select already speaks the value. */
  valueTestId?: string;
  /** Fill the containing grid/flex cell instead of resting at the control's
   * intrinsic width. Applies to the control face: the native select in the
   * default variant, the trigger in the quiet one. */
  fill?: boolean;
}

const BASE_CLASS = {
  select: requireClass(styles.select, "select.module.css", "select"),
  fillWidth: requireClass(styles.fillWidth, "select.module.css", "fillWidth"),
  quiet: requireClass(styles.quietVariant, "select.module.css", "quietVariant"),
  quietReadout: requireClass(styles.quietReadout, "select.module.css", "quietReadout"),
  quietCaret: requireClass(styles.quietCaret, "select.module.css", "quietCaret"),
  quietSelect: requireClass(styles.quietSelect, "select.module.css", "quietSelect"),
};

/** A native <select>, restyled to match the widget set - no custom
 * listbox/positioning JS - the pickers that need richer rows (ModelCatalog,
 * PathField) each own theirs. Controlled only,
 * mirroring Input. */
export function Select({
  value,
  onChange,
  options,
  disabled = false,
  id,
  name,
  variant = "default",
  readout,
  ariaLabel,
  "data-testid": testId,
  valueTestId,
  fill = false,
}: SelectProps) {
  const nativeSelect = (
    <select
      id={id}
      name={name}
      className={
        variant === "quiet" ? BASE_CLASS.quietSelect : `${BASE_CLASS.select}${fill ? ` ${BASE_CLASS.fillWidth}` : ""}`
      }
      value={value}
      onChange={onChange}
      disabled={disabled}
      aria-label={ariaLabel}
      data-testid={variant === "default" ? testId : undefined}
    >
      {options.map((option) => (
        <option key={option.value} value={option.value} disabled={option.disabled}>
          {option.label}
        </option>
      ))}
    </select>
  );
  if (variant === "quiet") {
    // The select still renders every option; the readout is just the label of
    // the one it currently holds, kept in sync by the same controlled value.
    const currentLabel = readout ?? options.find((option) => option.value === value)?.label ?? "";
    return (
      <span
        className={`${BASE_CLASS.quiet}${fill ? ` ${BASE_CLASS.fillWidth}` : ""}`}
        data-testid={testId}
        data-disabled={disabled ? "true" : undefined}
      >
        <span className={BASE_CLASS.quietReadout} data-testid={valueTestId} aria-hidden="true">
          {currentLabel}
        </span>
        <span className={BASE_CLASS.quietCaret} aria-hidden="true">
          <Chevron direction="down" />
        </span>
        {nativeSelect}
      </span>
    );
  }
  return nativeSelect;
}
