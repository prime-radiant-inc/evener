/**
 * VoiceCaptions — live recognition text with a throttled live region.
 *
 * Shows the current caption (partial or final). The visible text updates on
 * every change, but the *announced* text (the `aria-live` region) is throttled
 * so screen readers do not read out every partial update — that would flood
 * the user. Only the latest committed text is announced, at most once per
 * throttle window.
 *
 * Partial recognition renders in a dimmed style; final recognition in a clear
 * style. The `tone` prop selects the style so the parent (which knows whether
 * the caption is a partial or a final) controls presentation deterministically.
 */
import { type JSX, useEffect, useRef, useState } from "react";
import styles from "./VoiceCaptions.module.css";

export type CaptionTone = "partial" | "final" | "idle";

export interface VoiceCaptionsProps {
  /** The recognition text to display. */
  readonly text: string;
  /** Visual treatment: dimmed partial, clear final, or idle (empty). */
  readonly tone: CaptionTone;
  /** Minimum ms between live-region announcements. Default 1500. */
  readonly throttleMs?: number;
}

/**
 * Returns the text that should be announced, given the latest text and the
 * last announced value/time. Pure so it is unit-testable without timers.
 */
export function computeAnnouncement(
  text: string,
  lastAnnounced: string,
  now: number,
  lastAnnouncedAt: number,
  throttleMs: number,
): { announce: string; at: number } {
  if (text === lastAnnounced) {
    return { announce: lastAnnounced, at: lastAnnouncedAt };
  }
  if (text.length === 0) {
    return { announce: "", at: now };
  }
  // Throttle: if we announced very recently, hold off unless the window passed.
  if (now - lastAnnouncedAt < throttleMs && lastAnnouncedAt > 0) {
    return { announce: lastAnnounced, at: lastAnnouncedAt };
  }
  return { announce: text, at: now };
}

export function VoiceCaptions({
  text,
  tone,
  throttleMs = 1500,
}: VoiceCaptionsProps): JSX.Element {
  const [announced, setAnnounced] = useState("");
  const lastAnnouncedRef = useRef("");
  const lastAtRef = useRef(0);

  useEffect(() => {
    const now = Date.now();
    const result = computeAnnouncement(
      text,
      lastAnnouncedRef.current,
      now,
      lastAtRef.current,
      throttleMs,
    );
    if (result.announce !== lastAnnouncedRef.current) {
      lastAnnouncedRef.current = result.announce;
      lastAtRef.current = result.at;
      setAnnounced(result.announce);
    }
  }, [text, throttleMs]);

  // The visible caption always reflects the latest text.
  return (
    <div className={styles.captions} data-tone={tone}>
      <p className={styles.visible} data-testid="voice-caption-visible">
        {text}
      </p>
      {/* Throttled live region — separate from the visible text so the visible
          text can update freely while announcements are rate-limited. */}
      <span
        className={styles.announced}
        aria-live="polite"
        aria-atomic="true"
        data-testid="voice-caption-announced"
      >
        {announced}
      </span>
    </div>
  );
}
