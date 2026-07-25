// DetailsPanel: a trigger + Sheet holding the session's exact accounting
// figures - context occupancy, work time, cumulative tokens, dollar cost.
//
// The status row next to it is a glanceable strip: a context METER, a clock,
// arrows. This panel is where the same facts get room to be precise ("42%
// used · 42k / 100k · 58k left" rather than a 64px gauge), so a reader
// deciding whether to compact, or reporting what a session cost, has the
// numbers instead of a shape.
//
// Every value here comes off the live ThreadModel the status row already
// reads - no fetch, no wire call, nothing to load, so there is no loading or
// error state to render (unlike TasksPanel, whose rows are fetched on open).
//
// Cost is SerfThread.Cost: the "~$X.XX" string appwire.EstimateCost derives
// server-side from the thread's cumulative usage at the model's catalog
// price. The pricing table never crosses the wire, so the string is shown
// verbatim - never re-formatted - and an absent cost (no token data, or an
// uncataloged model) renders NO cost row at all rather than a false "~$0.00".
import { useState } from "react";
import type { ThreadModel } from "../../../protocol/model";
import { Button, Meter, Sheet } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { formatTokenCount } from "../transcript/messages/format";
import styles from "./detailspanel.module.css";
import { formatWorkDuration, totalWorkMillis } from "./statusFormat";

export interface DetailsPanelProps {
  model: ThreadModel;
  /** Current wall-clock ms, so the work-time figure keeps counting during an
   * in-flight turn (owned by SessionChrome's useNowTick, same as StatusRow). */
  now: number;
}

const CLASS = {
  list: requireClass(styles.list, "detailspanel.module.css", "list"),
  row: requireClass(styles.row, "detailspanel.module.css", "row"),
  label: requireClass(styles.label, "detailspanel.module.css", "label"),
  value: requireClass(styles.value, "detailspanel.module.css", "value"),
  meter: requireClass(styles.meter, "detailspanel.module.css", "meter"),
  dim: requireClass(styles.dim, "detailspanel.module.css", "dim"),
};

// contextTone escalates with pressure exactly as the status row's gauge does,
// so the same session never reads as two different severities on two surfaces.
function contextTone(pressure: number): "neutral" | "attention" | "danger" {
  if (pressure >= 0.95) return "danger";
  if (pressure >= 0.8) return "attention";
  return "neutral";
}

// A finished session has no live context occupancy to report: the hub builds
// an exited session's thread from its persisted SessionMeta, which carries
// work time, cumulative usage, and cost but no context figures at all
// (cmd/serf-hub/app_threadread.go's pastEntryThread). The wire's zeroes there
// mean "not measured", so the row is omitted rather than shown reading empty.
// Matches the TUI drawer's own ContextPressure > 0 guard.
function isEndedStatus(type: string): boolean {
  return type === "ended" || type === "closed";
}

function DetailRow({ label, testId, children }: { label: string; testId: string; children: React.ReactNode }) {
  return (
    <div className={CLASS.row} data-testid={testId}>
      <dt className={CLASS.label}>{label}</dt>
      <dd className={CLASS.value}>{children}</dd>
    </div>
  );
}

export function DetailsPanel({ model, now }: DetailsPanelProps) {
  const [open, setOpen] = useState(false);

  const showContext = model.contextWindow > 0 && !isEndedStatus(model.status.type);
  // Remaining is the context window minus what is used, floored at zero - the
  // daemon's own definition of the figure (agent/schema/context_metrics.go's
  // ContextMetrics.Remaining), so deriving it here matches the number the
  // daemon would have sent rather than inventing a second meaning.
  const remaining = Math.max(0, model.contextWindow - model.contextUsed);
  const percent = Math.round(model.contextPressure * 100);

  return (
    <>
      {/* data-details-trigger lets the command palette's "Toggle session
          details" (/status) synthesize a click here (shell/palette/commands.ts)
          - without it that command is inert. Button forwards data-* through. */}
      <Button variant="quiet" size="sm" onClick={() => setOpen(true)} data-details-trigger="">
        Details
      </Button>
      <Sheet open={open} onClose={() => setOpen(false)} title="Session details">
        <dl className={CLASS.list}>
          {showContext && (
            <DetailRow label="context" testId="session-details-context">
              <span className={CLASS.meter}>
                <Meter
                  label={`Context: ${formatTokenCount(model.contextUsed)} of ${formatTokenCount(model.contextWindow)} tokens used, ${percent} percent`}
                  value={model.contextUsed}
                  max={model.contextWindow}
                  tone={contextTone(model.contextPressure)}
                />
              </span>
              <span>{percent}% used</span>
              <span className={CLASS.dim}>
                {formatTokenCount(model.contextUsed)} / {formatTokenCount(model.contextWindow)}
              </span>
              <span className={CLASS.dim}>{formatTokenCount(remaining)} left</span>
            </DetailRow>
          )}
          <DetailRow label="work time" testId="session-details-work-time">
            {formatWorkDuration(totalWorkMillis(model.workMillis, model.activeTurnStartedAt, now))}
          </DetailRow>
          {model.usage && (
            <DetailRow label="tokens" testId="session-details-tokens">
              <span>↑{formatTokenCount(model.usage.inputTokens ?? 0)}</span>
              <span>↓{formatTokenCount(model.usage.outputTokens ?? 0)}</span>
            </DetailRow>
          )}
          {model.cost && (
            <DetailRow label="cost" testId="session-details-cost">
              {model.cost}
            </DetailRow>
          )}
        </dl>
      </Sheet>
    </>
  );
}
