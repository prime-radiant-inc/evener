/**
 * VoiceStatus — current voice status (listening/speaking/interrupted/error).
 *
 * Renders a status mark with a distinct glyph + label per status — color
 * reinforces but never carries meaning alone. The status text lives in a
 * polite live region so transitions are announced, but the region is
 * atomic and only changes when the status label changes (not on every
 * render), avoiding flood.
 */
import type { JSX } from "react";
import type { VoiceStatus as Status } from "../../state/voice";
import styles from "./VoiceStatus.module.css";

export interface VoiceStatusProps {
  readonly status: Status;
}

const MARKS: Record<Status, { glyph: string; label: string }> = {
  idle: { glyph: "·", label: "Idle" },
  listening: { glyph: "◉", label: "Listening" },
  speaking: { glyph: "▷", label: "Speaking" },
  interrupted: { glyph: "⏸", label: "Interrupted" },
  error: { glyph: "!", label: "Error" },
  disabled: { glyph: "⏹", label: "Voice disabled" },
};

export function VoiceStatus({ status }: VoiceStatusProps): JSX.Element {
  const mark = MARKS[status];
  return (
    <div
      className={styles.status}
      data-status={status}
      role="status"
      aria-live="polite"
      aria-atomic="true"
    >
      <span className={styles.glyph} aria-hidden="true">
        {mark.glyph}
      </span>
      <span className={styles.label} data-testid="voice-status-label">
        {mark.label}
      </span>
    </div>
  );
}
