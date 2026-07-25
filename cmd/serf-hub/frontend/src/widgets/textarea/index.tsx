import {
  type ChangeEvent,
  type ClipboardEvent,
  type CSSProperties,
  forwardRef,
  type KeyboardEvent,
  useLayoutEffect,
  useRef,
} from "react";
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
  /** Set the field's resting size, in text lines, instead of MIN_ROWS - for a
   * field whose job asks for more room than the default before anything is
   * typed (the spawn form's prompt, the page's primary input) or for less (a
   * finished session's collapsed one-line follow-up invitation).
   *
   * Drives BOTH the `--textarea-min-lines` custom property the stylesheet's
   * min-height floor reads AND the native `rows` attribute. Both are needed:
   * `rows` is what an autoGrow field's first real measurement measures, so a
   * floor raised without it gets immediately overwritten by a MIN_ROWS-tall
   * measurement (verified in Chrome - a 1-line field stayed at 2 lines until
   * `rows` followed). autoGrow still grows past this for real content. */
  minLines?: number;
  id?: string;
  name?: string;
  /** Native keydown passthrough - e.g. a composer's Enter-to-send/steer
   * routing (checking key/modifiers and optionally calling
   * preventDefault()). Fires before onChange for the same keystroke. */
  onKeyDown?: (event: KeyboardEvent<HTMLTextAreaElement>) => void;
  /** Native focus/blur passthrough - e.g. a collapsed follow-up field that
   * opens to a taller writing surface only while it has focus. */
  onFocus?: () => void;
  onBlur?: () => void;
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

/** The field's smallest comfortable target, in text lines. Mirrored by
 * textarea.module.css's own --textarea-min-lines floor (CSS cannot read a TS
 * constant, so the test suite pins the two together). */
export const MIN_ROWS = 2;
// Matches the legacy composer's clamp fraction exactly (see autoGrow's own
// doc comment above for the cited line).
export const MAX_HEIGHT_VIEWPORT_FRACTION = 0.5;

const BASE_CLASS = {
  textarea: requireClass(styles.textarea, "textarea.module.css", "textarea"),
  seamless: requireClass(styles.seamless, "textarea.module.css", "seamless"),
};

// resizeToFitContent mirrors the legacy composer's grow() (cmd/serf-hub/
// assets/renderer.js:6307-6314): reset to "auto" FIRST so a shrinking value
// (e.g. a cleared draft) reflects its smaller natural height instead of the
// still-large explicit height left over from before, then measure
// scrollHeight and clamp it.
//
// UNMEASURABLE MOUNTS: a field with no layout box - detached from the
// document, or under a display:none ancestor - reports scrollHeight 0.
// dockview builds a panel's content element detached and mounts the React
// tree into it before attaching (dockview-react's ReactPanelContentPart),
// so the pane that is not the boot-active one measures 0 on its first
// layout effect. Writing that 0 as an explicit height pins the field
// invisible and unclickable forever, since nothing else re-measures.
// Restoring whatever height was there instead (on a first mount, none at
// all, so `rows` + the CSS min-height floor size the field) leaves the
// element measurable later, which the ResizeObserver below re-measures the
// moment the field gains a box.
function resizeToFitContent(el: HTMLTextAreaElement): void {
  const before = el.style.height;
  el.style.height = "auto";
  const measured = el.scrollHeight;
  if (measured === 0) {
    el.style.height = before;
    return;
  }
  const maxHeight = window.innerHeight * MAX_HEIGHT_VIEWPORT_FRACTION;
  el.style.height = `${Math.min(measured, maxHeight)}px`;
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
    minLines,
    id,
    name,
    onKeyDown,
    onPaste,
    onFocus,
    onBlur,
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

  // Re-measures on the layout changes the value-keyed effect above cannot
  // see, from two complementary sources:
  //
  // ResizeObserver, on the field's OWN box: a pane revealed (or attached)
  // after its first mount, a pane the workspace switched back to, and a width
  // change that rewraps the same text into a different number of visual
  // lines. Guarded because jsdom has no ResizeObserver.
  //
  // A window resize listener, for the clamp: MAX_HEIGHT_VIEWPORT_FRACTION is
  // a function of window.innerHeight, not of the field's box, so a value tall
  // enough to be clamped keeps its old, now-stale ceiling when the window
  // grows - the element's box never changed, so the observer alone never
  // fires.
  useLayoutEffect(() => {
    if (!autoGrow) return;
    const el = textareaRef.current;
    if (!el) return;
    const remeasure = () => resizeToFitContent(el);
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(remeasure);
    observer?.observe(el);
    window.addEventListener("resize", remeasure);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", remeasure);
    };
  }, [autoGrow]);

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
      onFocus={onFocus}
      onBlur={onBlur}
      placeholder={placeholder}
      aria-label={ariaLabel}
      disabled={disabled}
      // An inline custom property, not an inline height: the stylesheet's
      // min-height calc stays the single definition of the floor, and
      // autoGrow's measured height still wins above it.
      style={minLines === undefined ? undefined : ({ "--textarea-min-lines": minLines } as CSSProperties)}
      // An autoGrow field's `rows` is what its FIRST measurement measures (the
      // CSS floor only backstops an unmeasurable mount), so a raised minLines
      // has to reach `rows` too - otherwise autoGrow writes a 2-line height
      // over the taller floor and the floor never shows. Verified in Chrome: a
      // minLines={1} field stayed 39px until this followed.
      rows={autoGrow ? (minLines ?? MIN_ROWS) : (rows ?? MIN_ROWS)}
    />
  );
});
