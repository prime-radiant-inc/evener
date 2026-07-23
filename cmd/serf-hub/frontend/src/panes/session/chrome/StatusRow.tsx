// StatusRow: the footer chrome's compact glanceable strip - state dot, model
// chip, reasoning-effort switcher, work-time clock, context gauge, usage,
// session cost. Promotes work-time and cost/usage into this ONE compact row
// rather than replicating the legacy split (compact strip vs. a separate
// details panel, parity-m5-composer.md finding #4) - a decision the wave plan
// already made explicitly (design doc lines 94-97 list them together under
// "status row").
//
// Session dollar cost rides the wire as SerfThread.Cost (appwire/types.go) -
// the "~$X.XX" string appwire.EstimateCost derives SERVER-SIDE from the
// thread's authoritative full-session cumulative Usage at the model's catalog
// price. The pricing table never crosses the wire, so the string is computed
// once server-side and shown verbatim (no client-side re-formatting). This is
// the honest full-session total, NOT a client sum of the turns currently
// loaded (thread/read's turnLimit windows those, so summing them would
// silently under-count). Absent (no chip) when the daemon omits it: no token
// data, or an uncataloged model - an honest "unknown", never a bogus "~$0.00".
import type { ChangeEvent } from "react";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore } from "../../../stores/threads";
import { Meter, Select, StatusDot, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { cadenceStateForStatus } from "../liveness";
import { formatTokenCount } from "../transcript/messages/format";
import { LocationCluster } from "./LocationCluster";
import { ModelSwitch } from "./ModelSwitch";
import { formatWorkDuration, totalWorkMillis } from "./statusFormat";
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

// Fallback effort ladder for a reasoning model whose own ladder the hub does
// not enumerate. Ported verbatim from the legacy live picker (cmd/serf-hub/
// assets/model-switch.js:30, itself from spawn.js:1605) so this surface and
// the spawn form agree; the daemon clamps a request to what the model actually
// accepts, so an over-broad list is safe.
const DEFAULT_EFFORT_LEVELS = ["minimal", "low", "medium", "high"];

// ReasoningEffortControl renders an interactive Select for a reasoning model's
// effort. The effective ladder is the model's own named levels, or - when it
// reasons but names none - the DEFAULT_EFFORT_LEVELS fallback: the wire really
// can emit supportsReasoning:true with an empty ladder (the daemon's Profile
// sets p.reasoning and p.effortLevels from independent conditions,
// agent/provider/profile.go:454 vs :442; the reducer coerces the absent ladder
// to [], reducer.ts:263). A model that does not reason at all gets no control.
//
// none-vs-(default): an unset effort - and serf's "none", which clears the
// effort to the provider default (llm/types.go:670, providercfg/load.go:76) -
// both mean "no explicit level, the model decides". They read as "(default)",
// a real leading option (value ""), never the first ladder level (which a
// bare value-"" select would display) and never a literal "none" the user
// appears to have chosen. Mirrors the legacy palette's own option list
// (search.js:415: a "(default)" head, "none" omitted as redundant with it).
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

  const levels =
    model.reasoningEffortLevels.length > 0
      ? model.reasoningEffortLevels
      : model.supportsReasoning
        ? DEFAULT_EFFORT_LEVELS
        : [];
  if (levels.length === 0) return null;

  const current = model.reasoningEffort && model.reasoningEffort !== "none" ? model.reasoningEffort : "";
  const options = [
    { value: "", label: "(default)" },
    ...levels.filter((level) => level !== "none").map((level) => ({ value: level, label: level })),
  ];

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
        value={current}
        onChange={(e) => void handleChange(e)}
        options={options}
      />
    </>
  );
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
      <ModelSwitch sessionRef={sessionRef} model={model} />
      <ReasoningEffortControl sessionRef={sessionRef} model={model} />
      <span className={CLASS.item} data-testid="status-row-work-time">
        {formatWorkDuration(workMs)}
      </span>
      <LocationCluster model={model} />
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
      {/* Session cost: the server-formatted SerfThread.Cost string shown
          verbatim (no client-side formatter — the pricing table is Go-side).
          A falsy cost (null/undefined/"" — the daemon's honest "unknown")
          renders nothing rather than a misleading "~$0.00". The title follows
          the row's "key value" tooltip convention (LocationCluster). */}
      {model.cost && (
        <span className={CLASS.item} data-testid="status-row-cost" title={`session cost ${model.cost}`}>
          {model.cost}
        </span>
      )}
    </div>
  );
}
