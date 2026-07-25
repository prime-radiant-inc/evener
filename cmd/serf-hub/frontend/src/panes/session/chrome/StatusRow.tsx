// StatusRow: the session footer's ONE quiet line of glance-level facts -
// model · effort · context meter · clock · cost, with queue depth riding the
// far right when there is any. Everything on it is something that could make
// you act in the next minute; everything exact lives one click away in
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
//   - raw ↑/↓ token counts. Cost is the glanceable form of the same fact, and
//     the details sheet carries the exact figures.
//
// A finished session replaces the whole strip with one summary line
// (model · N worked · cost) in --ink-mid: its work and cost are settled, so it
// reads as an epitaph rather than a cockpit with dead instruments. The model is
// the one thing on it that is still LIVE - a finished session can be sent to
// again, so the model its next turn will run on is still a choice; see
// EndedSummary.
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

import { sessionActionError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore } from "../../../stores/threads";
import { FailureGlyph, Meter, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { formatTokenCount } from "../transcript/messages/format";
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
  item: requireClass(styles.item, "statusrow.module.css", "item"),
  mono: requireClass(styles.mono, "statusrow.module.css", "mono"),
  meter: requireClass(styles.meter, "statusrow.module.css", "meter"),
  queue: requireClass(styles.queue, "statusrow.module.css", "queue"),
  summary: requireClass(styles.summary, "statusrow.module.css", "summary"),
  separator: requireClass(styles.separator, "statusrow.module.css", "separator"),
  effortTrigger: requireClass(styles.effortTrigger, "statusrow.module.css", "effortTrigger"),
  effortValue: requireClass(styles.effortValue, "statusrow.module.css", "effortValue"),
  effortChevron: requireClass(styles.effortChevron, "statusrow.module.css", "effortChevron"),
  effortSelect: requireClass(styles.effortSelect, "statusrow.module.css", "effortSelect"),
  srOnly: requireClass(styles.srOnly, "statusrow.module.css", "srOnly"),
};

// The wire statuses that mean this session's story is over - the same set
// Composer.tsx's own ENDED_STATUSES names, for the same reason ("notLoaded" is
// how a cold exited serf session actually arrives; see that constant's comment
// for the wire receipts). Kept as its own local set rather than shared: these
// two modules answer different questions about the same statuses (what to
// render vs. whether a card is usable) and a shared constant would invite one
// to drift into gating the other.
const ENDED_STATUSES: ReadonlySet<string> = new Set(["ended", "closed", "notLoaded"]);

// Fallback effort ladder for a reasoning model whose own ladder the hub does
// not enumerate. Ported verbatim from the legacy live picker (cmd/serf-hub/
// assets/model-switch.js:30, itself from spawn.js:1605) so this surface and
// the spawn form agree; the daemon clamps a request to what the model actually
// accepts, so an over-broad list is safe.
const DEFAULT_EFFORT_LEVELS = ["minimal", "low", "medium", "high"];

// The label an unset effort reads as - see the none-vs-(default) rule below.
const DEFAULT_EFFORT_LABEL = "(default)";

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
// none-vs-(default): an unset effort - and serf's "none", which clears the
// effort to the provider default (llm/types.go:670, providercfg/load.go:76) -
// both mean "no explicit level, the model decides". They read as "(default)",
// a real leading option (value ""), never the first ladder level (which a
// bare value-"" select would display) and never a literal "none" the user
// appears to have chosen. Mirrors the legacy palette's own option list
// (search.js:415: a "(default)" head, "none" omitted as redundant with it).
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

  const current = model.reasoningEffort && model.reasoningEffort !== "none" ? model.reasoningEffort : "";
  const options = ["", ...levels.filter((level) => level !== "none")];

  return (
    <span className={CLASS.effortTrigger} data-testid="status-row-effort">
      {/* The visible readout, and the only thing that takes up space here: the
          <select> over it is transparent, so this text is what a reader sees
          and the native control is what they operate. aria-hidden because the
          select already speaks its own value - without it the value would be
          announced twice. */}
      <span className={CLASS.effortValue} data-testid="status-row-effort-value" aria-hidden="true">
        {current === "" ? DEFAULT_EFFORT_LABEL : current}
      </span>
      <span className={CLASS.effortChevron} aria-hidden="true">
        ▾
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
            {level === "" ? DEFAULT_EFFORT_LABEL : level}
          </option>
        ))}
      </select>
    </span>
  );
}

// FailureCount is the session's answer to "did anything in here go wrong",
// stated where it can be read without scrolling.
//
// It exists because the transcript could not answer it. A long session measures
// about fourteen screens; only ~47% of that document hydrates at load, and eight
// of those screens carry no failure marker at all - so a reader who stopped
// partway concluded the run was clean because they had not yet reached a
// failure, twice reported as a real harm (kata hw2n). Row-level marking is not
// the gap and is untouched: this is the SESSION-scale statement that was
// missing entirely (the cadence dot goes --danger only when the session itself
// crashed, and neither the strip nor the details sheet carried a failure fact).
//
// It sits on this strip rather than in Session details because a fact behind a
// click cannot tell anyone there is something left to find, and the misreading
// forms right here - the measured session's last screen reads "Verified: 25
// tests pass" directly above "12m worked · ~$0.97", with six failures above it
// and nothing contradicting them.
//
// ZERO AND UNKNOWN BOTH RENDER NOTHING, for different reasons. Zero is a real
// server-side measurement over the whole transcript and is simply not news; the
// strip should only speak when there is something to act on, the same rule that
// keeps "0 queued" off it. Undefined means nobody counted (an unreadable
// transcript, or a producer that does not derive the figure), and a strip that
// said "0 failed" there would vouch for a session it never read. The count is
// NEVER derived from the loaded turns: thread/read windows them, so a client
// sum would print exactly the false all-clear this exists to prevent.
//
// The glyph is aria-hidden and the sentence is visually hidden, so the item
// announces once, in full ("6 failed tool calls"), rather than as the glyph's
// own "Failed" followed by the terse readout beside it.
//
// `separator` is the ended strip's own "·" punctuation, which the live strip
// does not use - passed in rather than inferred, so this component never has to
// know which row it is on.
function FailureCount({ count, separator }: { count: number | undefined; separator?: boolean }) {
  if (count === undefined || count <= 0) return null;
  const spoken = `${count} failed tool ${count === 1 ? "call" : "calls"}`;
  return (
    <span className={CLASS.item} data-testid="status-row-failures" title={spoken}>
      {separator && (
        <span className={CLASS.separator} aria-hidden="true">
          ·
        </span>
      )}
      <span aria-hidden="true">
        <FailureGlyph />
      </span>
      <span className={CLASS.mono} aria-hidden="true">
        {`${count} failed`}
      </span>
      <span className={CLASS.srOnly}>{spoken}</span>
    </span>
  );
}

// EndedSummary is a finished session's whole strip: what it was, what it spent,
// and the model its NEXT turn will run on. Work and cost are settled figures in
// --ink-mid; each is omitted when the wire never measured it - the same honesty
// rule DetailsPanel's own header states, and the reason an unmeasured work time
// shows nothing rather than a fabricated "1s" (formatWorkDuration clamps a real
// sub-second duration up to 1s, which is correct for a measurement and a lie for
// the absence of one).
//
// The model is the exception, and it stays a live ModelSwitch: this session can
// still be sent to (the hub advertises Send and ChangeModel for a cold exited
// thread and resumes it behind either call - cmd/serf-hub/app_threadread.go's
// pastEntryThread, app_model.go's setThreadModelWithResume), and Composer
// already renders a follow-up card here for that reason. A user picking the
// model for that follow-up should not have to first resume the session, or know
// that "running" is a state a session has.
function EndedSummary({ sessionRef, model, workMs }: { sessionRef: string; model: ThreadModel; workMs: number }) {
  const settled: Array<{ key: string; testId: string; text: string }> = [];
  if (workMs > 0) {
    settled.push({ key: "work", testId: "status-row-work-time", text: `${formatWorkDuration(workMs)} worked` });
  }
  if (model.cost) settled.push({ key: "cost", testId: "status-row-cost", text: model.cost });

  return (
    <div className={`${CLASS.row} ${CLASS.summary}`} data-testid="status-row">
      <ModelSwitch sessionRef={sessionRef} model={model} />
      {/* Ahead of the settled figures: work time and cost are an epitaph, and
          what went wrong is the one thing on this strip that could still change
          what the reader does next. */}
      <FailureCount count={model.failedToolCalls} separator />
      {settled.map((fact) => (
        <span key={fact.key} className={CLASS.item}>
          <span className={CLASS.separator} aria-hidden="true">
            ·
          </span>
          <span className={CLASS.mono} data-testid={fact.testId}>
            {fact.text}
          </span>
        </span>
      ))}
    </div>
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

  if (ENDED_STATUSES.has(model.status.type)) {
    return <EndedSummary sessionRef={sessionRef} model={model} workMs={workMs} />;
  }

  return (
    <div className={CLASS.row} data-testid="status-row">
      <ModelSwitch sessionRef={sessionRef} model={model} />
      {/* Driven purely by the wire figure, not by which strip is rendering.
          Today only a session the hub reads from disk carries one - a live
          session's transcript is still being written, so a disk-derived count
          would be a stale floor - but a producer that grows an honest live
          count needs no second render path here. */}
      <FailureCount count={model.failedToolCalls} />
      <ReasoningEffortControl sessionRef={sessionRef} model={model} />
      {/* The gauge is the whole readout: a used/window number pair beside it
          repeated what the fill already shows, in a row that has to stay one
          line. The exact counts are still available - spoken from the meter's
          own label, and on hover from this title, which follows the row's
          "key value" tooltip convention (status-row-cost). */}
      {hasContext && (
        <span
          className={CLASS.item}
          data-testid="status-row-context"
          title={`context ${formatTokenCount(model.contextUsed)} / ${formatTokenCount(model.contextWindow)}`}
        >
          <span className={CLASS.meter}>
            <Meter
              label={`Context: ${formatTokenCount(model.contextUsed)} of ${formatTokenCount(model.contextWindow)} tokens used, ${Math.round(model.contextPressure * 100)} percent`}
              value={model.contextUsed}
              max={model.contextWindow}
              tone={contextTone(model.contextPressure)}
            />
          </span>
        </span>
      )}
      {/* An unmeasured zero renders NOTHING, never formatWorkDuration's "1s":
          that clamp exists so a real sub-second duration doesn't read "0s", so
          feeding it an absence fabricates a measurement. Same gate
          DetailsPanel's own work-time row uses. */}
      {running && workMs > 0 && (
        <span className={`${CLASS.item} ${CLASS.mono}`} data-testid="status-row-work-time">
          {formatWorkDuration(workMs)}
        </span>
      )}
      {/* Session cost: the server-formatted SerfThread.Cost string shown
          verbatim (no client-side formatter — the pricing table is Go-side).
          A falsy cost (null/undefined/"" — the daemon's honest "unknown")
          renders nothing rather than a misleading "~$0.00". */}
      {model.cost && (
        <span
          className={`${CLASS.item} ${CLASS.mono}`}
          data-testid="status-row-cost"
          title={`session cost ${model.cost}`}
        >
          {model.cost}
        </span>
      )}
      {/* Queue depth rides the FAR RIGHT, so Send's effect on a running session
          is visible without a second row of chrome. Absent at zero: an empty
          queue is the normal case and "0 queued" would be noise on every
          session. */}
      {queueDepth > 0 && (
        <span className={`${CLASS.item} ${CLASS.mono} ${CLASS.queue}`} data-testid="status-row-queue">
          {`${queueDepth} queued`}
        </span>
      )}
    </div>
  );
}
