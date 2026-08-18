// Structure adapted from Beautiful UI's Recommendation card
// (https://www.beautifului.dev), MIT License, Copyright (c) 2026 Shane
// Levine — see LICENSES/beautiful-ui.txt. Values and markup translated
// into serf's CSS-module + token system; nothing is copy-pasted.
import type { CSSProperties } from "react";
import { Button } from "../button";
import { requireClass } from "../internal/requireClass";
import styles from "./recommendationcard.module.css";

export interface RecommendationCardAlternative {
  label: string;
  onSelect: () => void;
}

export interface RecommendationCardProps {
  title: string;
  body: string;
  /** 0-1. Rendered as a rounded percentage caption plus an inline meter
   * bar. Omitted entirely (no readout) when not given. */
  confidence?: number;
  onAccept?: () => void;
  onReject?: () => void;
  alternatives?: RecommendationCardAlternative[];
}

const CLASS = {
  card: requireClass(styles.card, "recommendationcard.module.css", "card"),
  eyebrow: requireClass(styles.eyebrow, "recommendationcard.module.css", "eyebrow"),
  title: requireClass(styles.title, "recommendationcard.module.css", "title"),
  confidenceRow: requireClass(styles.confidenceRow, "recommendationcard.module.css", "confidenceRow"),
  confidenceLabel: requireClass(styles.confidenceLabel, "recommendationcard.module.css", "confidenceLabel"),
  meterTrack: requireClass(styles.meterTrack, "recommendationcard.module.css", "meterTrack"),
  meterFill: requireClass(styles.meterFill, "recommendationcard.module.css", "meterFill"),
  body: requireClass(styles.body, "recommendationcard.module.css", "body"),
  alternatives: requireClass(styles.alternatives, "recommendationcard.module.css", "alternatives"),
  footer: requireClass(styles.footer, "recommendationcard.module.css", "footer"),
};

/** Clamps into [0, 1] then rounds to the nearest whole percent, mirroring
 * Meter's own clamp so an out-of-range caller (a stale 1.4, a negative
 * delta) can't distort the readout or overflow the fill bar. */
function percentOf(confidence: number): number {
  const clamped = Math.min(1, Math.max(0, confidence));
  return Math.round(clamped * 100);
}

/**
 * A single actionable suggestion: an eyebrow, title, optional confidence
 * readout, body copy, optional alternatives, and an Accept/Dismiss footer.
 * The confidence meter is drawn inline rather than composing the Meter
 * widget - Meter's tone prop is on the attention/alive/danger allowlist and
 * this card isn't, so its fill is a plain --accent bar on --field, not a
 * semantic gauge.
 */
export function RecommendationCard({
  title,
  body,
  confidence,
  onAccept,
  onReject,
  alternatives,
}: RecommendationCardProps) {
  return (
    <div className={CLASS.card}>
      <p className={CLASS.eyebrow}>RECOMMENDATION</p>
      <h3 className={CLASS.title}>{title}</h3>
      {confidence !== undefined && (
        <div className={CLASS.confidenceRow}>
          <span className={CLASS.confidenceLabel}>{percentOf(confidence)}% confident</span>
          {/* Native <meter> can't be restyled cross-browser (see widgets/meter's
           * own rationale) - this div+role is the same deliberate escape hatch. */}
          {/* biome-ignore lint/a11y/useSemanticElements: div+role is deliberate, see above */}
          <div
            className={CLASS.meterTrack}
            role="meter"
            aria-label="Confidence"
            aria-valuenow={percentOf(confidence)}
            aria-valuemin={0}
            aria-valuemax={100}
          >
            <div className={CLASS.meterFill} style={{ "--fill": `${percentOf(confidence)}%` } as CSSProperties} />
          </div>
        </div>
      )}
      <p className={CLASS.body}>{body}</p>
      {alternatives !== undefined && alternatives.length > 0 && (
        <div className={CLASS.alternatives}>
          {alternatives.map((alt) => (
            <Button key={alt.label} variant="quiet" size="sm" onClick={alt.onSelect}>
              {alt.label}
            </Button>
          ))}
        </div>
      )}
      {(onAccept !== undefined || onReject !== undefined) && (
        <div className={CLASS.footer}>
          {onAccept !== undefined && (
            <Button variant="primary" size="sm" onClick={onAccept}>
              Accept
            </Button>
          )}
          {onReject !== undefined && (
            <Button variant="quiet" size="sm" onClick={onReject}>
              Dismiss
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
