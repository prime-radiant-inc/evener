// StatusRow: the footer chrome's compact glanceable strip - state dot, model
// chip, reasoning-effort switcher, work-time clock, context gauge, usage.
// Promotes work-time and cost/usage into this ONE compact row rather than
// replicating the legacy split (compact strip vs. a separate details panel,
// parity-m5-composer.md finding #4) - a decision the wave plan already made
// explicitly (design doc lines 94-97 list them together under "status row").
//
// Dollar cost is deliberately NOT shown: appwire.EstimateCost (cmd/serf-hub/
// web_format.go:61, appwire/cost.go) is a Go-side computation over a
// pricing table that never crosses the wire - ThreadModel carries raw
// SerfUsage token counts only, no cost field, and Turn.cost (protocol/
// model.ts's TurnModel.cost) is a PER-TURN formatted string covering only
// whatever turns are currently loaded client-side (the last N via
// thread/read's turnLimit, older pages fetched on demand) - summing those
// would silently under-count a session's real total. Token counts (usage)
// are real wire truth at the thread level and are shown instead.
import type { ChangeEvent } from "react";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore } from "../../../stores/threads";
import { Chip, Meter, Select, StatusDot, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { cadenceStateForStatus } from "../liveness";
import { formatTokenCount } from "../transcript/messages/format";
import { formatWorkDuration, modelLabel, totalWorkMillis } from "./statusFormat";
import styles from "./statusrow.module.css";

export interface StatusRowProps {
  sessionRef: string;
  model: ThreadModel;
  now: number;
}

const CLASS = {
  row: requireClass(styles.row, "statusrow.module.css", "row"),
  item: requireClass(styles.item, "statusrow.module.css", "item"),
  contextNumbers: requireClass(styles.contextNumbers, "statusrow.module.css", "contextNumbers"),
  meter: requireClass(styles.meter, "statusrow.module.css", "meter"),
  srOnly: requireClass(styles.srOnly, "statusrow.module.css", "srOnly"),
};

// contextTone escalates the gauge's tone as pressure climbs, mirroring the
// legacy compact strip's own single warn threshold (parity-m5-composer.md
// §I: "⚠ warning glyph + context-warn class ... once ContextPercent >= 80")
// with an added danger tier near the ceiling - beyond-parity license, not a
// parity requirement.
function contextTone(pressure: number): "neutral" | "attention" | "danger" {
  if (pressure >= 0.95) return "danger";
  if (pressure >= 0.8) return "attention";
  return "neutral";
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

// ReasoningEffortControl renders an interactive Select when the model
// offers a real ladder to choose from; falls back to plain text for the
// current value when the model supports reasoning but the ladder is empty
// (ThreadModel.supportsReasoning is always a concrete boolean here, never
// the wire's "unknown" case - see model.ts - so there is no ambiguous
// third state to guess a hardcoded ladder for, unlike the legacy picker's
// own DEFAULT_EFFORT_LEVELS fallback); renders nothing at all when there is
// neither a ladder nor a current value to show.
function ReasoningEffortControl({ sessionRef, model }: { sessionRef: string; model: ThreadModel }) {
  const toasts = useToasts();

  async function handleChange(event: ChangeEvent<HTMLSelectElement>) {
    const level = event.target.value;
    try {
      await threadsStore.getState().setReasoningEffort(sessionRef, level);
    } catch (err) {
      toasts.push("error", `Couldn't change reasoning effort: ${errorMessage(err)}`);
    }
  }

  if (model.reasoningEffortLevels.length > 0) {
    return (
      <>
        {/* Select forwards only value/onChange/options/disabled/id/name (no
            rest-spread, no aria-label passthrough - widgets/select's own
            index.tsx) - a standard <label htmlFor> association is the only
            way left to name it accessibly. Visually hidden: the row is
            already compact, and the select's own current value IS the
            visible readout (matches the legacy chip's own bare-value
            display, parity-m5-composer.md §H). */}
        <label className={CLASS.srOnly} htmlFor="status-row-reasoning-effort">
          Reasoning effort
        </label>
        <Select
          id="status-row-reasoning-effort"
          value={model.reasoningEffort ?? ""}
          onChange={(e) => void handleChange(e)}
          options={model.reasoningEffortLevels.map((level) => ({ value: level, label: level }))}
        />
      </>
    );
  }
  if (model.reasoningEffort) {
    return <span>{model.reasoningEffort}</span>;
  }
  return null;
}

export function StatusRow({ sessionRef, model, now }: StatusRowProps) {
  const cadenceState = cadenceStateForStatus(model.status.type);
  const workMs = totalWorkMillis(model.workMillis, model.activeTurnStartedAt, now);
  const hasContext = model.contextWindow > 0;

  return (
    <div className={CLASS.row} data-testid="status-row">
      <span className={CLASS.item}>
        <StatusDot state={cadenceState} />
      </span>
      <Chip>{modelLabel(model.modelProvider, model.model)}</Chip>
      <ReasoningEffortControl sessionRef={sessionRef} model={model} />
      <span className={CLASS.item} data-testid="status-row-work-time">
        {formatWorkDuration(workMs)}
      </span>
      {hasContext && (
        <span className={CLASS.item}>
          <span className={CLASS.meter}>
            <Meter
              label={`Context: ${formatTokenCount(model.contextUsed)} of ${formatTokenCount(model.contextWindow)} tokens used, ${Math.round(model.contextPressure * 100)} percent`}
              value={model.contextUsed}
              max={model.contextWindow}
              tone={contextTone(model.contextPressure)}
            />
          </span>
          <span className={CLASS.contextNumbers}>
            {formatTokenCount(model.contextUsed)} / {formatTokenCount(model.contextWindow)}
          </span>
        </span>
      )}
      {model.usage && (
        <span className={CLASS.item} data-testid="status-row-usage">
          ↑{formatTokenCount(model.usage.inputTokens ?? 0)} ↓{formatTokenCount(model.usage.outputTokens ?? 0)}
        </span>
      )}
    </div>
  );
}
