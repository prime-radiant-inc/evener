import { type JSX, useState } from "react";
import type {
  RedactedDiagnostic,
  WorkEntry,
  WorkTone,
} from "../../services/activity";
import { SectionHeader } from "./ActivitySheet";

export interface WorkSectionProps {
  readonly work: WorkEntry[];
}

const TONE_LABELS: Record<WorkTone, string> = {
  running: "Running",
  failed: "Failed",
  terminal: "Completed",
  idle: "Idle",
  unknown: "Unknown",
};

/**
 * Work section — delegates, shell jobs, and watches as a nested activity
 * list. The collapsed summary counts top-level entries; expanded shows each
 * entry's label and tone. Raw identifiers appear only inside a diagnostics
 * disclosure.
 */
export function WorkSection({ work }: WorkSectionProps): JSX.Element {
  const [expanded, setExpanded] = useState(false);

  const delegateCount = work.filter((w) => w.kind === "delegate").length;
  const jobCount = work.filter((w) => w.kind === "job").length;
  const watchCount = work.filter((w) => w.kind === "watch").length;

  const parts: string[] = [];
  if (delegateCount > 0)
    parts.push(`${delegateCount} delegate${delegateCount > 1 ? "s" : ""}`);
  if (jobCount > 0) parts.push(`${jobCount} job${jobCount > 1 ? "s" : ""}`);
  if (watchCount > 0)
    parts.push(`${watchCount} watch${watchCount > 1 ? "es" : ""}`);
  const summary = parts.length > 0 ? parts.join(", ") : "";

  return (
    <section className="evener-activity-section">
      <SectionHeader
        label="Work"
        expanded={expanded}
        onToggle={() => setExpanded((e) => !e)}
      >
        {summary}
      </SectionHeader>
      {expanded ? (
        <div className="evener-activity-section__detail">
          {work.length === 0 ? (
            <p className="evener-activity-section__empty">No work entries</p>
          ) : (
            <ul className="evener-activity-work-list">
              {work.map((entry, i) => (
                <WorkRow
                  key={`${entry.kind}-${entry.diagnostics?.rawId ?? i}`}
                  entry={entry}
                  showDiagnostics={true}
                />
              ))}
            </ul>
          )}
        </div>
      ) : null}
    </section>
  );
}

function WorkRow({
  entry,
  showDiagnostics = false,
}: {
  entry: WorkEntry;
  showDiagnostics?: boolean;
}): JSX.Element {
  return (
    <li className="evener-activity-work-list__item">
      <div className="evener-activity-work-list__row">
        <span className="evener-activity-work-list__label">{entry.label}</span>
        <span
          className="evener-activity-work-list__tone"
          data-tone={entry.tone}
        >
          {TONE_LABELS[entry.tone]}
        </span>
      </div>
      {entry.outputSummary !== undefined ? (
        <span className="evener-activity-work-list__output">
          {entry.outputSummary}
        </span>
      ) : null}
      {entry.durationMs !== undefined ? (
        <span className="evener-activity-work-list__duration">
          {formatDuration(entry.durationMs)}
        </span>
      ) : null}
      {entry.children && entry.children.length > 0 ? (
        <ul className="evener-activity-work-list__children">
          {entry.children.map((child, i) => (
            <WorkRow
              key={`${child.kind}-${child.diagnostics?.rawId ?? i}`}
              entry={child}
              showDiagnostics={false}
            />
          ))}
        </ul>
      ) : null}
      {entry.diagnostics !== undefined && showDiagnostics ? (
        <DiagnosticsDisclosure diagnostics={entry.diagnostics} />
      ) : null}
    </li>
  );
}

function DiagnosticsDisclosure({
  diagnostics,
}: {
  diagnostics: RedactedDiagnostic;
}): JSX.Element {
  const [open, setOpen] = useState(false);
  return (
    <div className="evener-activity-diagnostics">
      <button
        type="button"
        className="evener-activity-diagnostics__toggle"
        aria-expanded={open}
        aria-label="Diagnostics"
        onClick={() => setOpen((o) => !o)}
      >
        Diagnostics
      </button>
      {open ? (
        <dl className="evener-activity-diagnostics__detail">
          <dt>ID</dt>
          <dd>{diagnostics.rawId}</dd>
          <dt>Operation</dt>
          <dd>{diagnostics.operationName}</dd>
          <dt>Status</dt>
          <dd>{diagnostics.statusClass}</dd>
          {diagnostics.outputBytes !== undefined ? (
            <>
              <dt>Output bytes</dt>
              <dd>{diagnostics.outputBytes}</dd>
            </>
          ) : null}
          {diagnostics.startedAt !== undefined ? (
            <>
              <dt>Started</dt>
              <dd>{diagnostics.startedAt}</dd>
            </>
          ) : null}
          {diagnostics.endedAt !== undefined ? (
            <>
              <dt>Ended</dt>
              <dd>{diagnostics.endedAt}</dd>
            </>
          ) : null}
          {diagnostics.durationMs !== undefined ? (
            <>
              <dt>Duration</dt>
              <dd>{diagnostics.durationMs} ms</dd>
            </>
          ) : null}
          {diagnostics.exitCode !== undefined ? (
            <>
              <dt>Exit code</dt>
              <dd>{diagnostics.exitCode}</dd>
            </>
          ) : null}
          {diagnostics.profileId !== undefined ? (
            <>
              <dt>Profile</dt>
              <dd>{diagnostics.profileId}</dd>
            </>
          ) : null}
        </dl>
      ) : null}
    </div>
  );
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms % 60_000) / 1000);
  return `${m}m ${s}s`;
}
