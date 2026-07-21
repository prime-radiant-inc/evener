import type { ChangeEvent } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./textarea.module.css";

export interface TextareaProps {
  value: string;
  onChange: (event: ChangeEvent<HTMLTextAreaElement>) => void;
  placeholder?: string;
  disabled?: boolean;
  /** Grow the visible row count to fit `value`'s line breaks, up to
   * MAX_ROWS - it counts "\n" occurrences, not rendered/wrapped lines, so a
   * long line that wraps visually without a literal line break does NOT
   * grow the box (see computeRows below for why). When false/omitted,
   * `rows` (or the 2-row default) is fixed. */
  autoGrow?: boolean;
  rows?: number;
  id?: string;
  name?: string;
}

const MIN_ROWS = 2;
const MAX_ROWS = 12;

const BASE_CLASS = {
  textarea: requireClass(styles.textarea, "textarea.module.css", "textarea"),
};

/**
 * autoGrow computes `rows` from the number of line breaks already IN
 * `value`, not by measuring the rendered element's scrollHeight. This is a
 * deliberate choice, not just a testability convenience: a scrollHeight-
 * based approach reads layout that a browser only settles after paint,
 * inherently either lagging a render behind or forcing a second
 * measure-and-set pass per keystroke (the "scrollHeight thrash" this
 * widget's requirements explicitly rule out). Counting line breaks is a
 * pure function of the prop already in hand, so it recomputes exactly
 * once per value change, synchronously, with no DOM read/write cycle and
 * no risk of measuring stale layout.
 */
function computeRows(value: string, autoGrow: boolean | undefined, rowsProp: number | undefined): number {
  if (autoGrow !== true) return rowsProp ?? MIN_ROWS;
  const lineCount = value === "" ? 1 : value.split("\n").length;
  return Math.min(MAX_ROWS, Math.max(MIN_ROWS, lineCount));
}

/** A multi-line text field. Controlled only, mirroring Input. */
export function Textarea({
  value,
  onChange,
  placeholder,
  disabled = false,
  autoGrow,
  rows,
  id,
  name,
}: TextareaProps) {
  return (
    <textarea
      id={id}
      name={name}
      className={BASE_CLASS.textarea}
      value={value}
      onChange={onChange}
      placeholder={placeholder}
      disabled={disabled}
      rows={computeRows(value, autoGrow, rows)}
    />
  );
}
