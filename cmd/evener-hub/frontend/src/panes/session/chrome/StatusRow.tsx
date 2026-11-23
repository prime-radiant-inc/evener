// StatusRow: the session footer's ONE quiet line of glance-level facts -
// model · effort · context meter · clock · queue depth, with the queue riding
// the far right when there is any. Everything on it is something that could
// make you act in the next minute; everything exact lives one click away in
// DetailsPanel (the same session's cwd, branch, project, token counts and
// precise figures), which is why this row carries a 64px gauge where that
// panel carries "42% used · 42K / 100K · 58K left".
//
// What deliberately is NOT here:
//   - a state dot. The pane header already renders Cadence for this session
//     (Session.tsx passes it to PaneScaffold), so a second dot two rows down
//     restated it.
//   - cwd / branch / project. None of them can change mid-session, so they are
//     reference material, not a status: they live in the details sheet.
//   - raw ↑/↓ token counts. The details sheet carries the exact figures.

import type { ThreadModel } from "@evener/appwire-client";
import { sessionActionError, sessionEffortLevels } from "@evener/appwire-client";
import { threadsStore } from "../../../stores/threads";
import { Meter, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ModelSwitch } from "./ModelSwitch";
import { ReasoningEffortControl } from "./ReasoningEffortControl";
import {
  assumedWindowMark,
  contextTone,
  effectiveContextStatus,
  formatWorkDuration,
  totalWorkMillis,
} from "./statusFormat";
import styles from "./statusrow.module.css";

export interface StatusRowProps {
  sessionRef: string;
  model: ThreadModel;
  now: number;
  /** Pack the facts against the row's far end instead of its leading edge.
   * The composer's inline mount uses it so the model pair sits beside the
   * verbs rather than immediately after the paperclip. */
  align?: "start" | "end";
}

const CLASS = {
  row: requireClass(styles.row, "statusrow.module.css", "row"),
  rowEnd: requireClass(styles.rowEnd, "statusrow.module.css", "rowEnd"),
  identity: requireClass(styles.identity, "statusrow.module.css", "identity"),
  item: requireClass(styles.item, "statusrow.module.css", "item"),
  figure: requireClass(styles.figure, "statusrow.module.css", "figure"),
  context: requireClass(styles.context, "statusrow.module.css", "context"),
  contextMeter: requireClass(styles.contextMeter, "statusrow.module.css", "contextMeter"),
  contextPercent: requireClass(styles.contextPercent, "statusrow.module.css", "contextPercent"),
  workTime: requireClass(styles.workTime, "statusrow.module.css", "workTime"),
  queue: requireClass(styles.queue, "statusrow.module.css", "queue"),
  queueFull: requireClass(styles.queueFull, "statusrow.module.css", "queueFull"),
  queueCompact: requireClass(styles.queueCompact, "statusrow.module.css", "queueCompact"),
};

// The session's effort picker: the model's own ladder, or the fallback when it
// reasons but names none. The wire really can emit supportsReasoning:true with
// an empty ladder (the daemon's Profile sets p.reasoning and p.effortLevels
// from independent conditions, agent/provider/profile.go:454 vs :442; the
// reducer coerces the absent ladder to [], reducer.ts:263). A model that does
// not reason at all gets no control.
function SessionEffortControl({ sessionRef, model }: { sessionRef: string; model: ThreadModel }) {
  const toasts = useToasts();

  async function handleChange(level: string) {
    try {
      await threadsStore.getState().setReasoningEffort(sessionRef, level);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't change reasoning effort", err));
    }
  }

  const levels = sessionEffortLevels(model.reasoningEffortLevels, model.supportsReasoning);
  if (levels.length === 0) return null;

  return (
    <ReasoningEffortControl
      levels={levels}
      current={model.reasoningEffort ?? ""}
      defaultLevel={model.defaultReasoningEffort}
      onChange={(level) => void handleChange(level)}
      testId="status-row-effort"
      id="status-row-reasoning-effort"
      ariaLabel="Reasoning effort"
    />
  );
}

export function StatusRow({ sessionRef, model, now, align = "start" }: StatusRowProps) {
  const workMs = totalWorkMillis(model.workMillis, model.activeTurnStartedAt, now);
  const status = effectiveContextStatus(model);
  const hasContext = status.ceiling > 0;
  // The clock reports an in-flight turn's elapsed time, so it has nothing to
  // say when no turn is running - and a strip that keeps showing a frozen
  // number implies otherwise. The banked total is still one click away in
  // Session details.
  const running = model.activeTurnStartedAt !== undefined;
  const queueDepth = model.queue?.depth ?? 0;
  const contextPercent = Math.round(status.pressure * 100);
  const capacityMark = status.hasCustomThreshold ? "" : assumedWindowMark(model.contextAssumed);
  const windowSuffix = status.hasCustomThreshold && !model.contextAssumed ? ` (${model.contextWindow} window)` : "";
  const contextLabel = `Context: ${model.contextUsed} of ${capacityMark}${status.ceiling} tokens used${windowSuffix}, ${contextPercent} percent`;

  return (
    <div className={align === "end" ? `${CLASS.row} ${CLASS.rowEnd}` : CLASS.row} data-testid="status-row">
      <span className={CLASS.identity} data-testid="status-row-identity">
        <ModelSwitch sessionRef={sessionRef} model={model} shrink />
        <SessionEffortControl sessionRef={sessionRef} model={model} />
      </span>
      {hasContext && (
        <span className={CLASS.context} data-testid="status-row-context" title={contextLabel}>
          <span className={CLASS.contextMeter} data-testid="status-row-context-meter">
            <Meter
              label={contextLabel}
              value={model.contextUsed}
              max={status.ceiling}
              tone={contextTone(status.pressure)}
            />
          </span>
          <span
            className={`${CLASS.contextPercent} ${CLASS.figure}`}
            data-testid="status-row-context-percent"
            aria-hidden="true"
          >
            {`${contextPercent}%`}
          </span>
        </span>
      )}
      {/* An unmeasured zero renders NOTHING, never formatWorkDuration's "1s":
          that clamp exists so a real sub-second duration doesn't read "0s", so
          feeding it an absence fabricates a measurement. Same gate
          DetailsPanel's own work-time row uses. */}
      {running && workMs > 0 && (
        <span className={`${CLASS.item} ${CLASS.figure} ${CLASS.workTime}`} data-testid="status-row-work-time">
          {formatWorkDuration(workMs)}
        </span>
      )}
      {/* Queue depth rides the FAR RIGHT, so Send's effect on a running session
          is visible without a second row of chrome. Absent at zero: an empty
          queue is the normal case and "0 queued" would be noise on every
          session. */}
      {queueDepth > 0 && (
        <span
          className={`${CLASS.item} ${CLASS.figure} ${CLASS.queue}`}
          data-testid="status-row-queue"
          role="status"
          aria-label={`${queueDepth} queued`}
          title={`${queueDepth} queued`}
        >
          <span className={CLASS.queueFull} data-testid="status-row-queue-full" aria-hidden="true">
            {`${queueDepth} queued`}
          </span>
          <span className={CLASS.queueCompact} data-testid="status-row-queue-compact" aria-hidden="true">
            {`Q${queueDepth}`}
          </span>
        </span>
      )}
    </div>
  );
}
