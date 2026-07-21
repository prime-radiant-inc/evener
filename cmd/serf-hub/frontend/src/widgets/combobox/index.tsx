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
  /** Accessible name for the input, forwarded as-is. Prefer
   * `aria-labelledby` pointing at an external label element - see this
   * component's doc comment for why. */
  "aria-label"?: string;
  "aria-labelledby"?: string;
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
 * Accessible name: pass `aria-labelledby` (pointing at an external label
 * element's id) as the primary, recommended pattern - both it and
 * `aria-label` are forwarded to the input as-is. This keeps the
 * combobox's name fixed to just that label, never the popup's rendered
 * option text. Wrapping in a native <label> instead
 * (`<label>Model<Combobox .../></label>`) still works via descendant
 * containment regardless of the wrapper <div> in between, but is a
 * secondary option with a caveat: name-from-content computation walks the
 * label's full subtree, which - while the popup is open - includes the
 * rendered option text too. This may make the accessible name noisier
 * than intended while browsing (exact behavior depends on how a given
 * browser/AT's accname implementation treats the nested listbox; not
 * fully verified here).
 */
export function Combobox<T extends ComboboxOption = ComboboxOption>({
  options,
  onQuery,
  onPick,
  renderOption,
  "aria-label": ariaLabel,
  "aria-labelledby": ariaLabelledBy,
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
  // else, or nothing at all. This effect runs after commit, so it alone
  // isn't enough - the render that happens first, still with the old
  // activeIndex against the new (possibly shorter) options, must not
  // dereference out of bounds either. activeOption below is what actually
  // guards that; this effect just formalizes the reset for next render.
  useEffect(() => {
    setActiveIndex(-1);
  }, [options]);

  const showPopup = open && options.length > 0;
  // Bounds-checked instead of `options[activeIndex]!`: activeIndex can be
  // stale relative to a freshly-shrunk options array during the single
  // render between a debounced onQuery's shorter results arriving and the
  // [options] effect above resetting it - an out-of-bounds access there
  // used to throw during render (type ArrowDown to a high index, then a
  // shorter result set lands - exactly the flow this widget exists for).
  const activeOption = activeIndex >= 0 && activeIndex < options.length ? options[activeIndex] : undefined;

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
      case "Enter":
        if (activeOption) {
          event.preventDefault();
          pick(activeOption);
        }
        break;
      case "Escape":
        // Only consume Escape (and stop it reaching an enclosing overlay,
        // e.g. a Dialog this combobox is nested in) when there's actually
        // a popup here to close. Otherwise let it bubble - an idle
        // combobox has nothing of its own for Escape to do.
        if (open) {
          event.preventDefault();
          event.stopPropagation();
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
        aria-activedescendant={activeOption ? optionId(activeOption) : undefined}
        aria-label={ariaLabel}
        aria-labelledby={ariaLabelledBy}
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
