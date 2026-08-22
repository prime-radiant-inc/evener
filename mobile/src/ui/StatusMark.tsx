import type { JSX } from "react";

export type StatusKind =
  | "reachable"
  | "reconnecting"
  | "offline"
  | "unknown"
  | "attention";

export interface StatusMarkProps {
  readonly status: StatusKind;
}

const LABELS: Record<StatusKind, string> = {
  reachable: "Connected",
  reconnecting: "Reconnecting",
  offline: "Offline",
  unknown: "Not checked",
  attention: "Needs attention",
};

/**
 * Status mark with a unique glyph + text label per status. Color reinforces
 * the glyph but never carries meaning alone — every variant has a distinct
 * character and word. "unknown" means reachability has not been checked — it
 * is never used to fabricate "Reconnecting" when no real health data exists.
 */
export function StatusMark({ status }: StatusMarkProps): JSX.Element {
  return (
    <span className="evener-status-mark" data-status={status}>
      <span className="evener-status-mark__glyph" aria-hidden="true">
        {glyphFor(status)}
      </span>
      <span className="evener-status-mark__label">{LABELS[status]}</span>
    </span>
  );
}

function glyphFor(status: StatusKind): string {
  switch (status) {
    case "reachable":
      return "✓";
    case "reconnecting":
      return "↻";
    case "offline":
      return "✕";
    case "unknown":
      return "?";
    case "attention":
      return "!";
  }
}
