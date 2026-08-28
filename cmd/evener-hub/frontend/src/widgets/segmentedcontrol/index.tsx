import { type KeyboardEvent, useId, useRef } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./segmentedcontrol.module.css";

export interface SegmentedControlOption<T extends string = string> {
  value: T;
  label: string;
  accessibleLabel?: string;
  disabled?: boolean;
}

export interface SegmentedControlProps<T extends string = string> {
  label: string;
  value: T;
  options: readonly SegmentedControlOption<T>[];
  onChange(value: T): void;
  disabled?: boolean;
  size?: "sm" | "md";
  fullWidth?: boolean;
  id?: string;
  "aria-describedby"?: string;
}

const CLASS = {
  root: requireClass(styles.root, "segmentedcontrol.module.css", "root"),
  label: requireClass(styles.label, "segmentedcontrol.module.css", "label"),
  track: requireClass(styles.track, "segmentedcontrol.module.css", "track"),
  fullWidth: requireClass(styles.fullWidth, "segmentedcontrol.module.css", "fullWidth"),
  option: requireClass(styles.option, "segmentedcontrol.module.css", "option"),
  optionLabel: requireClass(styles.optionLabel, "segmentedcontrol.module.css", "optionLabel"),
  sm: requireClass(styles.sm, "segmentedcontrol.module.css", "sm"),
  md: requireClass(styles.md, "segmentedcontrol.module.css", "md"),
};

function validateSegmentedControl<T extends string>(
  label: string,
  value: T,
  options: readonly SegmentedControlOption<T>[],
): void {
  if (options.length < 2 || options.length > 6) throw new Error("SegmentedControl requires two through six options");
  if (label.trim() === "") throw new Error("SegmentedControl requires a non-empty group label");
  if (new Set(options.map((option) => option.value)).size !== options.length)
    throw new Error("SegmentedControl option values must be unique");
  if (options.filter((option) => option.value === value).length !== 1)
    throw new Error("SegmentedControl value must match exactly one option");
  for (const option of options) {
    if (option.label.trim() === "") throw new Error("SegmentedControl options require visible labels");
    if (option.accessibleLabel !== undefined && option.accessibleLabel.trim() === "")
      throw new Error("SegmentedControl accessible labels must be non-empty");
  }
}

function firstEnabledIndex<T extends string>(options: readonly SegmentedControlOption<T>[]): number {
  return options.findIndex((option) => !option.disabled);
}

function lastEnabledIndex<T extends string>(options: readonly SegmentedControlOption<T>[]): number {
  for (let index = options.length - 1; index >= 0; index--) {
    if (!options[index]?.disabled) return index;
  }
  return -1;
}

function stepEnabledIndex<T extends string>(
  options: readonly SegmentedControlOption<T>[],
  from: number,
  delta: 1 | -1,
): number {
  const count = options.length;
  if (count === 0) return -1;
  let index = from;
  for (let step = 0; step < count; step++) {
    index = (index + delta + count) % count;
    if (!options[index]?.disabled) return index;
  }
  return -1;
}

export function SegmentedControl<T extends string = string>({
  label,
  value,
  options,
  onChange,
  disabled = false,
  size = "md",
  fullWidth = false,
  id,
  "aria-describedby": ariaDescribedby,
}: SegmentedControlProps<T>) {
  const generatedGroupId = useId();
  const labelId = useId();
  const optionRefs = useRef<(HTMLButtonElement | null)[]>([]);

  validateSegmentedControl(label, value, options);

  const selectedIndex = options.findIndex((option) => option.value === value);
  const selectedOption = options[selectedIndex];
  let tabbableIndex = selectedIndex;
  if (disabled) {
    tabbableIndex = -1;
  } else if (selectedOption?.disabled) {
    tabbableIndex = firstEnabledIndex(options);
  }
  const sizeClass = size === "sm" ? CLASS.sm : CLASS.md;

  function choose(index: number) {
    const option = options[index];
    if (!option || disabled || option.disabled === true || option.value === value) return;
    onChange(option.value);
  }

  function focusAndChoose(index: number) {
    if (index === -1 || disabled || options[index]?.disabled) return;
    choose(index);
    optionRefs.current[index]?.focus();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    if (disabled || options[index]?.disabled) return;
    switch (event.key) {
      case "ArrowRight":
      case "ArrowDown":
        event.preventDefault();
        focusAndChoose(stepEnabledIndex(options, index, 1));
        break;
      case "ArrowLeft":
      case "ArrowUp":
        event.preventDefault();
        focusAndChoose(stepEnabledIndex(options, index, -1));
        break;
      case "Home":
        event.preventDefault();
        focusAndChoose(firstEnabledIndex(options));
        break;
      case "End":
        event.preventDefault();
        focusAndChoose(lastEnabledIndex(options));
        break;
      default:
        break;
    }
  }

  return (
    <div className={CLASS.root}>
      <div id={labelId} className={CLASS.label}>
        {label}
      </div>
      <div
        id={id ?? generatedGroupId}
        role="radiogroup"
        aria-labelledby={labelId}
        aria-describedby={ariaDescribedby}
        aria-orientation="horizontal"
        aria-disabled={disabled ? "true" : undefined}
        className={`${CLASS.track}${fullWidth ? ` ${CLASS.fullWidth}` : ""}`}
        style={{ gridTemplateColumns: `repeat(${options.length}, minmax(0, 1fr))` }}
      >
        {options.map((option, index) => (
          // biome-ignore lint/a11y/useSemanticElements: button-based radio is intentionally native-activatable and follows the ARIA radiogroup pattern.
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={option.value === value}
            aria-label={option.accessibleLabel ?? option.label}
            disabled={disabled || option.disabled === true}
            tabIndex={index === tabbableIndex ? 0 : -1}
            className={`${CLASS.option} ${sizeClass}`}
            ref={(node) => {
              optionRefs.current[index] = node;
            }}
            onClick={() => choose(index)}
            onKeyDown={(event) => handleKeyDown(event, index)}
          >
            <span className={CLASS.optionLabel}>{option.label}</span>
          </button>
        ))}
      </div>
    </div>
  );
}
