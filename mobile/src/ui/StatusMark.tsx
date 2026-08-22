import type { JSX } from "react";

export type StatusKind = "reachable" | "reconnecting" | "offline" | "attention";

export interface StatusMarkProps {
  readonly status: StatusKind;
}

const LABELS: Record<StatusKind, string> = {
  reachable: "Connected",
  reconnecting: "Reconnecting",
  offline: "Offline",
  attention: "Needs attention",
};

/**
 * Status mark with a unique glyph + text label per status. Color reinforces
 * the glyph but never carries meaning alone — every variant has a distinct
 * character and word.
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
    case "attention":
      return "!";
  }
}
