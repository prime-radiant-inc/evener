import { type ChangeEvent, type ClipboardEvent, forwardRef, type KeyboardEvent, useLayoutEffect, useRef } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./textarea.module.css";

export interface TextareaProps {
  value: string;
  onChange: (event: ChangeEvent<HTMLTextAreaElement>) => void;
  placeholder?: string;
  disabled?: boolean;
  /** Grow the textarea's rendered height to fit `value`'s actual laid-out
   * content: after every value change, resets the inline height to "auto"
   * then measures the native scrollHeight and applies it, clamped to
   * MAX_HEIGHT_VIEWPORT_FRACTION of the viewport height (matches the legacy
   * composer's clamp - cmd/serf-hub/assets/renderer.js:6307-6314,
   * `Math.min(ta.scrollHeight, window.innerHeight * 0.5)`). Unlike counting
   * "\n" occurrences, this also grows for a long single logical line that
   * wraps across several visual lines with no literal line break - the case
   * a row-count heuristic can never detect, since it only ever sees the
   * string, never the rendered layout. When false/omitted, `rows` (or the
   * 2-row default) is fixed and no measurement occurs. */
  autoGrow?: boolean;
  rows?: number;
  id?: string;
  name?: string;
  /** Native keydown passthrough - e.g. a composer's Enter-to-send/steer
   * routing (checking key/modifiers and optionally calling
   * preventDefault()). Fires before onChange for the same keystroke. */
  onKeyDown?: (event: KeyboardEvent<HTMLTextAreaElement>) => void;
  /** Native paste passthrough - e.g. a composer intercepting a pasted image
   * off the clipboard while leaving a text-only paste to the browser's own
   * default insertion (never calling preventDefault() itself). */
  onPaste?: (event: ClipboardEvent<HTMLTextAreaElement>) => void;
  /** Accessible name when no visible `<label>` owns this field - e.g. a
   * composer whose only visual cue is a placeholder (which screen readers
   * must not rely on alone). */
  "aria-label"?: string;
  /** Drop the field's own box - border, background, radius, inset padding,
   * and focus ring - so an enclosing card that already draws one reads as a
   * single control rather than a box inside a box (panes/session/composer's
   * inputCard). The card must then own the focus affordance itself
   * (:focus-within), since a seamless field has no ring of its own. */
  seamless?: boolean;
}

const MIN_ROWS = 2;
// Matches the legacy composer's clamp fraction exactly (see autoGrow's own
// doc comment above for the cited line).
export const MAX_HEIGHT_VIEWPORT_FRACTION = 0.5;

const BASE_CLASS = {
  textarea: requireClass(styles.textarea, "textarea.module.css", "textarea"),
  seamless: requireClass(styles.seamless, "textarea.module.css", "seamless"),
};

// resizeToFitContent mirrors the legacy composer's grow() (cmd/serf-hub/
// assets/renderer.js:6307-6314) exactly: reset to "auto" FIRST so a
// shrinking value (e.g. a cleared draft) reflects its smaller natural
// height instead of the still-large explicit height left over from before,
// then measure scrollHeight and clamp it.
function resizeToFitContent(el: HTMLTextAreaElement): void {
  el.style.height = "auto";
  const maxHeight = window.innerHeight * MAX_HEIGHT_VIEWPORT_FRACTION;
  el.style.height = `${Math.min(el.scrollHeight, maxHeight)}px`;
}

/** A multi-line text field. Controlled only, mirroring Input. Forwards its
 * ref to the native element (a composer needs imperative focus()/
 * selectionStart access alongside the controlled value - see
 * panes/session/composer's attachment-marker helpers) while still keeping
 * its own internal ref for the autoGrow measurement effect below; the
 * inline ref callback sets both from the one native node. */
export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  {
    value,
    onChange,
    placeholder,
    disabled = false,
    autoGrow,
    rows,
    id,
    name,
    onKeyDown,
    onPaste,
    "aria-label": ariaLabel,
    seamless = false,
  },
  forwardedRef,
) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: value is a deliberate trigger-only dep - resizeToFitContent reads the DOM element's own (React-already-updated) value/scrollHeight, never the `value` variable directly, but the effect must still re-run on every keystroke to remeasure
  useLayoutEffect(() => {
    if (!autoGrow) return;
    const el = textareaRef.current;
    if (el) resizeToFitContent(el);
  }, [autoGrow, value]);

  return (
    <textarea
      ref={(node) => {
        textareaRef.current = node;
        if (typeof forwardedRef === "function") forwardedRef(node);
        else if (forwardedRef) forwardedRef.current = node;
      }}
      id={id}
      name={name}
      className={seamless ? `${BASE_CLASS.textarea} ${BASE_CLASS.seamless}` : BASE_CLASS.textarea}
      value={value}
      onChange={onChange}
      onKeyDown={onKeyDown}
      onPaste={onPaste}
      placeholder={placeholder}
      aria-label={ariaLabel}
      disabled={disabled}
      rows={autoGrow ? MIN_ROWS : (rows ?? MIN_ROWS)}
    />
  );
});
