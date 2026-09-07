// StatusRow: the session footer's ONE quiet line of glance-level facts -
// model · effort · context meter · clock · queue depth, with the queue riding
// the far right when there is any. Everything on it is something that could
// make you act in the next minute; everything exact lives one click away in
// DetailsPanel (the same session's cwd, branch, project, token counts and
// precise figures), which is why this row carries a 64px gauge where that
// panel carries "42% used · 42k / 100k · 58k left".
//
// What deliberately is NOT here:
//   - a state dot. The pane header already renders Cadence for this session
//     (Session.tsx passes it to PaneScaffold), so a second dot two rows down
//     restated it.
//   - cwd / branch / project. None of them can change mid-session, so they are
//     reference material, not a status: they live in the details sheet.
//   - raw ↑/↓ token counts. The details sheet carries the exact figures.

import { sessionActionError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { effortLabel, effortOptionLevels } from "../../../shell/reasoningEffort";
import { threadsStore } from "../../../stores/threads";
import { Chevron, Meter, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ModelSwitch } from "./ModelSwitch";
import { contextTone, formatWorkDuration, totalWorkMillis } from "./statusFormat";
import styles from "./statusrow.module.css";

export interface StatusRowProps {
  sessionRef: string;
  model: ThreadModel;
  now: number;
}

const CLASS = {
  row: requireClass(styles.row, "statusrow.module.css", "row"),
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
  separator: requireClass(styles.separator, "statusrow.module.css", "separator"),
  effortTrigger: requireClass(styles.effortTrigger, "statusrow.module.css", "effortTrigger"),
  effortValue: requireClass(styles.effortValue, "statusrow.module.css", "effortValue"),
  effortChevron: requireClass(styles.effortChevron, "statusrow.module.css", "effortChevron"),
  effortSelect: requireClass(styles.effortSelect, "statusrow.module.css", "effortSelect"),
  srOnly: requireClass(styles.srOnly, "statusrow.module.css", "srOnly"),
};

// Fallback effort ladder for a reasoning model whose own ladder the hub does
// not enumerate. Ported verbatim from the legacy live picker (cmd/evener-hub/
// assets/model-switch.js:30, itself from spawn.js:1605) so this surface and
// the spawn form agree; the daemon clamps a request to what the model actually
// accepts, so an over-broad list is safe.
const DEFAULT_EFFORT_LEVELS = ["minimal", "low", "medium", "high"];

// ReasoningEffortControl renders the reasoning-effort switcher as a quiet
// trigger matching the model switcher beside it: the current value IS the
// visible control, no bordered <select> box competing with it in a row that has
// to stay one 12px line. It is still a REAL native <select> underneath, laid
// over the readout at zero opacity - so it keeps every behavior a box would
// have (tab order, arrow keys, type-ahead, the platform's own dropdown, a
// standard <label htmlFor> accessible name) rather than reimplementing a
// listbox to save a border.
//
// The effective ladder is the model's own named levels, or - when it reasons
// but names none - the DEFAULT_EFFORT_LEVELS fallback: the wire really can emit
// supportsReasoning:true with an empty ladder (the daemon's Profile sets
// p.reasoning and p.effortLevels from independent conditions,
// agent/provider/profile.go:454 vs :442; the reducer coerces the absent ladder
// to [], reducer.ts:263). A model that does not reason at all gets no control.
//
// none-vs-(default): an unset effort ("") means "the session default applies"
// and reads as "(default)", a real leading option, never the first ladder
// level a bare value-"" select would display. "none" is an explicit off the
// user chose (llm/types.go ReasoningEffortNone): it displays as "none (off)"
// and is offered as its own option when the model's ladder lists it (or when
// the session already runs at it).
function ReasoningEffortControl({ sessionRef, model }: { sessionRef: string; model: ThreadModel }) {
  const toasts = useToasts();

  async function handleChange(level: string) {
    try {
      await threadsStore.getState().setReasoningEffort(sessionRef, level);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't change reasoning effort", err));
    }
  }

  const levels =
    model.reasoningEffortLevels.length > 0
      ? model.reasoningEffortLevels
      : model.supportsReasoning
        ? DEFAULT_EFFORT_LEVELS
        : [];
  if (levels.length === 0) return null;

  const current = model.reasoningEffort ?? "";
  const options = effortOptionLevels(levels, current);

  return (
    <span className={CLASS.effortTrigger} data-testid="status-row-effort">
      <span className={CLASS.separator} aria-hidden="true">
        ·
      </span>
      {/* The visible readout, and the only thing that takes up space here: the
          <select> over it is transparent, so this text is what a reader sees
          and the native control is what they operate. aria-hidden because the
          select already speaks its own value - without it the value would be
          announced twice. */}
      <span className={CLASS.effortValue} data-testid="status-row-effort-value" aria-hidden="true">
        {effortLabel(current, levels)}
      </span>
      <span className={CLASS.effortChevron} aria-hidden="true">
        <Chevron direction="down" />
      </span>
      <label className={CLASS.srOnly} htmlFor="status-row-reasoning-effort">
        Reasoning effort
      </label>
      {/* A native <select>, not widgets/select: that widget's own restyle is
          the bordered 32px box this row is shedding, and it forwards no
          className for an overlay variant. Rendered raw so this row can own
          the presentation while keeping every native behavior. */}
      <select
        id="status-row-reasoning-effort"
        className={CLASS.effortSelect}
        value={current}
        onChange={(e) => void handleChange(e.target.value)}
      >
        {options.map((level) => (
          <option key={level} value={level}>
            {effortLabel(level, levels)}
          </option>
        ))}
      </select>
    </span>
  );
}

export function StatusRow({ sessionRef, model, now }: StatusRowProps) {
  const workMs = totalWorkMillis(model.workMillis, model.activeTurnStartedAt, now);
  const hasContext = model.contextWindow > 0;
  // The clock reports an in-flight turn's elapsed time, so it has nothing to
  // say when no turn is running - and a strip that keeps showing a frozen
  // number implies otherwise. The banked total is still one click away in
  // Session details.
  const running = model.activeTurnStartedAt !== undefined;
  const queueDepth = model.queue?.depth ?? 0;
  const contextPercent = Math.round(model.contextPressure * 100);
  const contextLabel = `Context: ${model.contextUsed} of ${model.contextWindow} tokens used, ${contextPercent} percent`;

  return (
    <div className={CLASS.row} data-testid="status-row">
      <span className={CLASS.identity} data-testid="status-row-identity">
        <ModelSwitch sessionRef={sessionRef} model={model} />
        <ReasoningEffortControl sessionRef={sessionRef} model={model} />
      </span>
      {hasContext && (
        <>
          <meter
            className={CLASS.srOnly}
            aria-label={contextLabel}
            min={0}
            value={Math.min(model.contextWindow, Math.max(0, model.contextUsed))}
            max={model.contextWindow}
          />
          <span className={CLASS.context} data-testid="status-row-context" aria-hidden="true" title={contextLabel}>
            <span className={CLASS.contextMeter} data-testid="status-row-context-meter">
              <Meter
                label={contextLabel}
                value={model.contextUsed}
                max={model.contextWindow}
                tone={contextTone(model.contextPressure)}
              />
            </span>
            <span className={`${CLASS.contextPercent} ${CLASS.figure}`} data-testid="status-row-context-percent">
              {`${contextPercent}%`}
            </span>
          </span>
        </>
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
