/**
 * VoiceLevel — decorative waveform driven by a bounded `level` prop (0-1).
 *
 * Pure CSS transforms scale a fixed row of bars — no canvas, no SVG animation.
 * The bar count is fixed so layout is stable; only `scaleY` changes with level.
 * Reduced-motion disables the transform animation (bars hold their rest state).
 *
 * Decorative: `aria-hidden="true"` — the live caption region is the accessible
 * surface, not the waveform.
 */
import type { JSX } from "react";
import styles from "./VoiceLevel.module.css";

export interface VoiceLevelProps {
  /** Audio level, bounded to [0,1]. Values outside the range are clamped. */
  readonly level: number;
  /** When true, suppress animation (reduced-motion / not listening). */
  readonly reducedMotion: boolean;
  /** When false, the waveform rests at its idle state. */
  readonly active: boolean;
}

// A fixed bar count keeps layout stable; only transforms respond to level.
const AMPLITUDES = [0.45, 0.7, 1.0, 0.85, 1.0, 0.7, 0.45] as const;

function clamp01(value: number): number {
  if (!Number.isFinite(value)) return 0;
  if (value < 0) return 0;
  if (value > 1) return 1;
  return value;
}

export function VoiceLevel({
  level,
  reducedMotion,
  active,
}: VoiceLevelProps): JSX.Element {
  const clamped = clamp01(level);
  return (
    <div
      className={styles.waveform}
      data-active={active ? "true" : "false"}
      data-reduced-motion={reducedMotion ? "true" : "false"}
      data-testid="voice-level"
      aria-hidden="true"
    >
      {AMPLITUDES.map((amp, i) => {
        const scale = active ? amp * clamped : 0.12;
        return (
          <span
            // biome-ignore lint/suspicious/noArrayIndexKey: fixed-length decorative bars that never reorder — index keys are stable.
            key={i}
            className={styles.bar}
            style={{ transform: `scaleY(${scale.toFixed(3)})` }}
          />
        );
      })}
    </div>
  );
}
