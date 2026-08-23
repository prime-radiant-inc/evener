/**
 * VoiceControls — End and Keyboard buttons for the voice screen.
 *
 * Both controls are explicit 44px tap targets. The Keyboard fallback ends
 * native voice before navigating to typed input — the parent supplies the
 * `onKeyboard` handler which must stop native voice first. The End button
 * ends voice mode and returns to typed conversation.
 */
import type { JSX } from "react";
import styles from "./VoiceControls.module.css";

export interface VoiceControlsProps {
  /** End voice mode and return to typed conversation. */
  readonly onEnd: () => void;
  /** Switch to typed input. Must end native voice before navigation. */
  readonly onKeyboard: () => void;
  /** Disable the controls while a transition is in flight. */
  readonly disabled: boolean;
}

export function VoiceControls({
  onEnd,
  onKeyboard,
  disabled,
}: VoiceControlsProps): JSX.Element {
  return (
    <div className={styles.controls}>
      <button
        type="button"
        className={styles.keyboard}
        onClick={onKeyboard}
        disabled={disabled}
        aria-label="Switch to keyboard"
      >
        <span aria-hidden="true">⌨</span>
        <span className={styles.label}>Keyboard</span>
      </button>
      <button
        type="button"
        className={styles.end}
        onClick={onEnd}
        disabled={disabled}
        aria-label="End voice mode"
      >
        <span aria-hidden="true">■</span>
        <span className={styles.label}>End</span>
      </button>
    </div>
  );
}
