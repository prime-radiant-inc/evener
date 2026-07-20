import { useEffect, useId, useRef, useState, type ChangeEvent, type KeyboardEvent, type ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./combobox.module.css";

export interface ComboboxOption {
  id: string;
  label: string;
}

export interface ComboboxProps<T extends ComboboxOption = ComboboxOption> {
  options: T[];
  onQuery: (query: string) => void;
  onPick: (option: T) => void;
  renderOption?: (option: T) => ReactNode;
}

const QUERY_DEBOUNCE_MS = 150;

const CLASS = {
  root: requireClass(styles.root, "combobox.module.css", "root"),
  input: requireClass(styles.input, "combobox.module.css", "input"),
  listbox: requireClass(styles.listbox, "combobox.module.css", "listbox"),
  option: requireClass(styles.option, "combobox.module.css", "option"),
  optionActive: requireClass(styles.optionActive, "combobox.module.css", "optionActive"),
};

/**
 * ARIA 1.2 combobox-with-listbox-popup: role="combobox" on the input,
 * role="listbox" popup, aria-activedescendant tracks the highlighted
 * option - real DOM focus never leaves the input, so typing is never
 * interrupted. ArrowUp/Down move the highlight (clamped, no wraparound -
 * unlike Menu's roving items, a combobox is narrowing toward one answer,
 * not cycling a fixed set of actions); Enter picks the highlighted option;
 * Escape closes the popup without clearing what was typed; blurring the
 * input closes it (nothing in the popup can steal focus, so any blur means
 * focus genuinely left). onQuery is debounced 150ms after the last
 * keystroke. Never traps focus (no FocusScope) - this is the widget
 * later model/directory pickers build on.
 *
 * Has no label prop (not in the locked API) - give it an accessible name
 * by wrapping it in a native <label>, which works via descendant
 * containment regardless of the wrapper <div> in between:
 * `<label>Model<Combobox .../></label>`.
 */
export function Combobox<T extends ComboboxOption = ComboboxOption>({
  options,
  onQuery,
  onPick,
  renderOption,
}: ComboboxProps<T>) {
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(-1);
  const listboxId = useId();
  const debounceRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => () => clearTimeout(debounceRef.current), []);

  // The caller's options prop can change out from under an open popup
  // (new search results arriving) - drop any stale highlight rather than
  // let aria-activedescendant point at an index that now means something
  // else, or nothing at all.
  useEffect(() => {
    setActiveIndex(-1);
  }, [options]);

  const showPopup = open && options.length > 0;

  function pick(option: T) {
    onPick(option);
    setQuery(option.label);
    setOpen(false);
    setActiveIndex(-1);
  }

  function handleChange(event: ChangeEvent<HTMLInputElement>) {
    const value = event.target.value;
    setQuery(value);
    setOpen(true);
    setActiveIndex(-1);
    clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => onQuery(value), QUERY_DEBOUNCE_MS);
  }

  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        if (options.length === 0) break;
        if (!open) {
          setOpen(true);
          setActiveIndex(0);
        } else {
          setActiveIndex((current) => Math.min(current + 1, options.length - 1));
        }
        break;
      case "ArrowUp":
        event.preventDefault();
        if (options.length === 0) break;
        if (!open) {
          setOpen(true);
          setActiveIndex(options.length - 1);
        } else {
          setActiveIndex((current) => Math.max(current - 1, 0));
        }
        break;
      case "Enter": {
        const active = activeIndex === -1 ? undefined : options[activeIndex];
        if (active) {
          event.preventDefault();
          pick(active);
        }
        break;
      }
      case "Escape":
        if (open) {
          event.preventDefault();
          setOpen(false);
          setActiveIndex(-1);
        }
        break;
      default:
        break;
    }
  }

  function handleBlur() {
    setOpen(false);
    setActiveIndex(-1);
  }

  function optionId(option: T): string {
    return `${listboxId}-${option.id}`;
  }

  return (
    <div className={CLASS.root}>
      <input
        role="combobox"
        className={CLASS.input}
        value={query}
        onChange={handleChange}
        onKeyDown={handleKeyDown}
        onBlur={handleBlur}
        aria-expanded={showPopup}
        aria-autocomplete="list"
        aria-controls={showPopup ? listboxId : undefined}
        aria-activedescendant={activeIndex !== -1 ? optionId(options[activeIndex]!) : undefined}
      />
      {showPopup && (
        <ul role="listbox" id={listboxId} className={CLASS.listbox}>
          {options.map((option, index) => (
            <li
              key={option.id}
              id={optionId(option)}
              role="option"
              aria-selected={index === activeIndex}
              className={`${CLASS.option} ${index === activeIndex ? CLASS.optionActive : ""}`}
              // Selecting with the mouse must not blur the input (the ARIA
              // 1.2 pattern keeps real focus there throughout). A mousedown
              // on a non-focusable element still triggers the browser's
              // default "move focus to the nearest focusable ancestor, or
              // blur" behavior, which would fire handleBlur and close (thus
              // unmount) this <li> before its click ever arrives -
              // preventing mousedown's default is the standard fix.
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => pick(option)}
            >
              {renderOption ? renderOption(option) : option.label}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
