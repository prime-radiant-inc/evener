import { type KeyboardEvent, useId, useRef } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./radiogroup.module.css";

export interface RadioGroupOption {
  value: string;
  label: string;
  disabled?: boolean;
}

export interface RadioGroupProps {
  /** Always-visible legend text, and the group's accessible name via
   * aria-labelledby - required, mirrors Switch/Meter's own required
   * accessible-name props (a group can't ship unlabeled). */
  label: string;
  value: string;
  onChange: (value: string) => void;
  options: RadioGroupOption[];
  /** Disables every option regardless of its own `disabled` field. */
  disabled?: boolean;
}

const CLASS = {
  root: requireClass(styles.root, "radiogroup.module.css", "root"),
  legend: requireClass(styles.legend, "radiogroup.module.css", "legend"),
  options: requireClass(styles.options, "radiogroup.module.css", "options"),
  option: requireClass(styles.option, "radiogroup.module.css", "option"),
  dot: requireClass(styles.dot, "radiogroup.module.css", "dot"),
  dotInner: requireClass(styles.dotInner, "radiogroup.module.css", "dotInner"),
  optionLabel: requireClass(styles.optionLabel, "radiogroup.module.css", "optionLabel"),
};

function indexOfValue(options: RadioGroupOption[], value: string): number {
  return options.findIndex((option) => option.value === value);
}

function firstEnabledIndex(options: RadioGroupOption[]): number {
  return options.findIndex((option) => !option.disabled);
}

function lastEnabledIndex(options: RadioGroupOption[]): number {
  for (let i = options.length - 1; i >= 0; i--) {
    const option = options[i];
    if (option && !option.disabled) return i;
  }
  return -1;
}

// Next enabled index stepping by `delta` (+1/-1) from `from`, wrapping
// around the ends - mirrors Menu's own stepEnabledIndex (see
// widgets/menu/index.tsx). Returns `from` unchanged if every option is
// disabled.
function stepEnabledIndex(options: RadioGroupOption[], from: number, delta: 1 | -1): number {
  const n = options.length;
  if (n === 0) return -1;
  let i = from;
  for (let step = 0; step < n; step++) {
    i = (i + delta + n) % n;
    const option = options[i];
    if (option && !option.disabled) return i;
  }
  return from;
}

/**
 * A native-radio-equivalent widget of custom `role="radio"` buttons (not
 * real `<input type="radio">` - see Switch's own precedent for why this
 * codebase reimplements form-control-like widgets on `<button>` rather
 * than restyling a native control cross-browser): exactly one option is
 * ever checked, roving tabindex keeps the WHOLE group one Tab stop (landing
 * on the checked option, or the first enabled one if none is checked yet),
 * and ArrowRight/Down and ArrowLeft/Up both MOVE FOCUS AND CHECK the new
 * option immediately (wrapping at the ends, skipping disabled options) -
 * this is the WAI-ARIA APG radiogroup pattern's own key bindings
 * (w3.org/WAI/ARIA/apg/patterns/radio), distinct from Menu/Tree's
 * roving-tabindex-then-Enter-to-activate shape. Space/Enter need no
 * handler of their own: these are real `<button>` elements, so the
 * browser's native activation already fires onClick for both keys, exactly
 * like Switch's own track button.
 */
export function RadioGroup({ label, value, onChange, options, disabled = false }: RadioGroupProps) {
  const legendId = useId();
  const optionRefs = useRef<(HTMLButtonElement | null)[]>([]);

  const checkedIndex = indexOfValue(options, value);
  const checkedOption = checkedIndex !== -1 ? options[checkedIndex] : undefined;
  const tabbableIndex = checkedOption && !checkedOption.disabled ? checkedIndex : firstEnabledIndex(options);

  function choose(index: number) {
    const option = options[index];
    if (!option || option.disabled || disabled) return;
    onChange(option.value);
  }

  function focusAndChoose(index: number) {
    if (index === -1) return;
    choose(index);
    optionRefs.current[index]?.focus();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) {
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
      <div id={legendId} className={CLASS.legend}>
        {label}
      </div>
      <div role="radiogroup" aria-labelledby={legendId} className={CLASS.options}>
        {options.map((option, index) => (
          // biome-ignore lint/a11y/useSemanticElements: custom button-based radio, not native <input type="radio"> - see this widget's own top comment for why (matches Switch's precedent)
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={option.value === value}
            disabled={disabled || option.disabled === true}
            tabIndex={index === tabbableIndex ? 0 : -1}
            className={CLASS.option}
            ref={(node) => {
              optionRefs.current[index] = node;
            }}
            onClick={() => choose(index)}
            onKeyDown={(event) => handleKeyDown(event, index)}
          >
            <span className={CLASS.dot} aria-hidden="true">
              <span className={CLASS.dotInner} />
            </span>
            <span className={CLASS.optionLabel}>{option.label}</span>
          </button>
        ))}
      </div>
    </div>
  );
}
