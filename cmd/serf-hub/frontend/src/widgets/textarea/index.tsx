import { type ChangeEvent, useLayoutEffect, useRef } from "react";
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
}

const MIN_ROWS = 2;
// Matches the legacy composer's clamp fraction exactly (see autoGrow's own
// doc comment above for the cited line).
export const MAX_HEIGHT_VIEWPORT_FRACTION = 0.5;

const BASE_CLASS = {
  textarea: requireClass(styles.textarea, "textarea.module.css", "textarea"),
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

/** A multi-line text field. Controlled only, mirroring Input. */
export function Textarea({ value, onChange, placeholder, disabled = false, autoGrow, rows, id, name }: TextareaProps) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: value is a deliberate trigger-only dep - resizeToFitContent reads the DOM element's own (React-already-updated) value/scrollHeight, never the `value` variable directly, but the effect must still re-run on every keystroke to remeasure
  useLayoutEffect(() => {
    if (!autoGrow) return;
    const el = textareaRef.current;
    if (el) resizeToFitContent(el);
  }, [autoGrow, value]);

  return (
    <textarea
      ref={textareaRef}
      id={id}
      name={name}
      className={BASE_CLASS.textarea}
      value={value}
      onChange={onChange}
      placeholder={placeholder}
      disabled={disabled}
      rows={autoGrow ? MIN_ROWS : (rows ?? MIN_ROWS)}
    />
  );
}
