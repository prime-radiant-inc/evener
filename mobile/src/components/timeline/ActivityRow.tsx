import { type JSX, useState } from "react";
import type { ActivityDetail, ActivityState } from "../../conversation/model";
import { PlainTextBlock } from "./PlainTextBlock";

export interface ActivityRowProps {
  readonly id: string;
  readonly label: string;
  readonly state: ActivityState;
  readonly detail: ActivityDetail;
}

/**
 * One-line collapsed activity card. Tapping (or pressing Enter/Space) expands
 * an inline detail sheet showing the tool's arguments, output, and error — all
 * as escaped plain text, never raw HTML. The collapsed row shows only the
 * neutral label and a state glyph; raw protocol JSON (argumentsJson) is never
 * exposed in the collapsed view.
 */
export function ActivityRow({
  label,
  state,
  detail,
}: ActivityRowProps): JSX.Element {
  const [expanded, setExpanded] = useState(false);

  function toggle(): void {
    setExpanded((e) => !e);
  }

  return (
    <div className="evener-activity-row" data-state={state}>
      <button
        type="button"
        className="evener-activity-row__summary"
        aria-expanded={expanded}
        onClick={toggle}
      >
        <span className="evener-activity-row__glyph" aria-hidden="true">
          {glyphFor(state)}
        </span>
        <span className="evener-activity-row__label">{label}</span>
        <span className="evener-activity-row__chevron" aria-hidden="true">
          {expanded ? "▾" : "▸"}
        </span>
      </button>
      {expanded ? <ActivityDetailSheet detail={detail} /> : null}
    </div>
  );
}

function glyphFor(state: ActivityState): string {
  switch (state) {
    case "running":
      return "◐";
    case "completed":
      return "✓";
    case "failed":
      return "✕";
  }
}

function ActivityDetailSheet({
  detail,
}: {
  readonly detail: ActivityDetail;
}): JSX.Element {
  return (
    <div className="evener-activity-row__detail">
      {detail.arguments ? (
        <div className="evener-activity-row__section">
          <PlainTextBlock text={detail.arguments} />
        </div>
      ) : null}
      {detail.output ? (
        <div className="evener-activity-row__section">
          <PlainTextBlock text={detail.output} />
        </div>
      ) : null}
      {detail.error ? (
        <div className="evener-activity-row__section evener-activity-row__error">
          <PlainTextBlock text={detail.error} />
        </div>
      ) : null}
      {detail.exitCode !== undefined ? (
        <div className="evener-activity-row__meta">
          {`Exit ${detail.exitCode}`}
        </div>
      ) : null}
      {detail.durationMs !== undefined ? (
        <div className="evener-activity-row__meta">
          {`${formatDuration(detail.durationMs)}`}
        </div>
      ) : null}
    </div>
  );
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}
