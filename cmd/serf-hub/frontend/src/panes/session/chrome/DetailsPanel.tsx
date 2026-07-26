// DetailsPanel: a trigger + Sheet holding a session's full accounting - who it
// is (model, state, id), what it has spent (context, work time, tokens, cost),
// and where it runs (cwd, project, branch, when it began and was last written).
//
// The status row beside it is a glanceable strip: a context METER, a clock,
// arrows. This panel is where the same facts get room to be precise ("42%
// used · 42k / 100k · 58k left" rather than a 64px gauge), plus the facts the
// strip has no room for at all, so a reader deciding whether to compact, or
// reporting what a session cost, has the numbers instead of a shape.
//
// Rows are grouped (Session / Usage / Location) the way the panel this replaced
// grouped its own (cmd/serf-hub/web_workspace.go's detailsSections on the
// legacy renderer): a dozen flat rows do not scan.
//
// EVERY row is omitted when its value is absent. That is the panel's central
// rule, and the reason a session's whole usage section can vanish: the wire
// omits what was never measured, and a zero there is Go's unset zero value,
// not a measurement of zero. Rendering "~$0.00" or "0s" for an unmeasured
// session would be inventing data. A section left with no rows drops its
// heading too.
//
// Every value comes off the live ThreadModel the status row already reads - no
// fetch, no wire call, nothing to load, so there is no loading or error state
// to render (unlike TasksPanel, whose rows are fetched on open).
//
// Cost is SerfThread.Cost: the "~$X.XX" string appwire.EstimateCost derives
// server-side from the thread's cumulative usage at the model's catalog price.
// The pricing table never crosses the wire, so the string is shown verbatim -
// never re-formatted, and never derived client-side even when the token total
// beside it was. An absent cost (no token data, or an uncataloged model) is an
// honest unknown and renders no row at all.
import { forwardRef, useImperativeHandle, useState } from "react";
import type { ThreadModel } from "../../../protocol/model";
import { Button, Meter, Sheet } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { formatTokenCount } from "../transcript/messages/format";
import { formatTimestamp, sessionTokens } from "./detailsAccounting";
import styles from "./detailspanel.module.css";
import { contextTone, formatWorkDuration, modelLabel, totalWorkMillis } from "./statusFormat";

export interface DetailsPanelProps {
  model: ThreadModel;
  /** Current wall-clock ms, so the work-time figure keeps counting during an
   * in-flight turn (owned by SessionChrome's useNowTick, same as StatusRow). */
  now: number;
  // True once SessionChrome's own row has measured too narrow to show this
  // panel's own inline trigger beside Tasks and the "..." menu without
  // wrapping to a second row (kata vybn) - SessionChrome renders a "Details"
  // item in that menu instead, opening this SAME Sheet through the
  // imperative handle below. Omitted (the default) is every existing
  // caller/test, which never suppresses this trigger.
  hideTrigger?: boolean;
}

/** Lets SessionChrome open this panel's Sheet from a collapsed menu item,
 * without lifting `open` out of this component (which would touch every
 * existing render site in DetailsPanel.test.tsx for no behavioral gain -
 * see this task's report). */
export interface DetailsPanelHandle {
  open: () => void;
}

const CLASS = {
  section: requireClass(styles.section, "detailspanel.module.css", "section"),
  sectionTitle: requireClass(styles.sectionTitle, "detailspanel.module.css", "sectionTitle"),
  list: requireClass(styles.list, "detailspanel.module.css", "list"),
  row: requireClass(styles.row, "detailspanel.module.css", "row"),
  label: requireClass(styles.label, "detailspanel.module.css", "label"),
  value: requireClass(styles.value, "detailspanel.module.css", "value"),
  meter: requireClass(styles.meter, "detailspanel.module.css", "meter"),
  dim: requireClass(styles.dim, "detailspanel.module.css", "dim"),
  path: requireClass(styles.path, "detailspanel.module.css", "path"),
};

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

// Section renders a titled group, or nothing at all when every row inside it
// was omitted for want of data - a heading over an empty list would advertise
// facts the panel does not have. The caller's `value && <DetailRow/>` guards
// arrive here as falsy children, which is what gets filtered out.
function Section({ title, children }: { title: string; children: React.ReactNode }) {
  const rows = (Array.isArray(children) ? children : [children]).filter(Boolean);
  if (rows.length === 0) return null;
  return (
    <section className={CLASS.section}>
      <h3 className={CLASS.sectionTitle}>{title}</h3>
      <dl className={CLASS.list}>{rows}</dl>
    </section>
  );
}

export const DetailsPanel = forwardRef<DetailsPanelHandle, DetailsPanelProps>(function DetailsPanel(
  { model, now, hideTrigger = false },
  ref,
) {
  const [open, setOpen] = useState(false);
  useImperativeHandle(ref, () => ({ open: () => setOpen(true) }), []);

  const showContext = model.contextWindow > 0 && !isEndedStatus(model.status.type);
  // Remaining is the context window minus what is used, floored at zero - the
  // daemon's own definition of the figure (agent/schema/context_metrics.go's
  // ContextMetrics.Remaining), so deriving it here matches the number the
  // daemon would have sent rather than inventing a second meaning.
  const remaining = Math.max(0, model.contextWindow - model.contextUsed);
  const percent = Math.round(model.contextPressure * 100);

  const workMs = totalWorkMillis(model.workMillis, model.activeTurnStartedAt, now);
  const tokens = sessionTokens(model);
  // A derived sum over a windowed transcript covers only the turns in hand
  // (thread/read's turnLimit), so its label says exactly that rather than
  // passing a partial figure off as the session's total.
  const tokensLabel = tokens?.scope === "loaded" ? "tokens (loaded turns)" : "tokens";
  const createdAt = formatTimestamp(model.createdAt);
  const updatedAt = formatTimestamp(model.updatedAt);
  // A project path identical to the cwd is the common case (a session in its
  // own project root); repeating it under a second label says nothing.
  const projectPath = model.projectPath && model.projectPath !== model.cwd ? model.projectPath : undefined;

  return (
    <>
      {/* data-details-trigger lets the command palette's "Toggle session
          details" (/status) synthesize a click here (shell/palette/commands.ts)
          - without it that command is inert. Button forwards data-* through.
          Omitted while hideTrigger is set (the row collapsed this into the
          "..." menu instead - see SessionChrome): clickTrigger is already
          documented no-op-safe when its selector finds nothing, so /status
          goes quiet rather than throwing while collapsed (kata vybn's report
          - a follow-up kata tracks reaching this trigger through the menu
          too). */}
      {!hideTrigger && (
        <Button variant="quiet" size="sm" onClick={() => setOpen(true)} data-details-trigger="">
          Details
        </Button>
      )}
      <Sheet open={open} onClose={() => setOpen(false)} title="Session details">
        <Section title="Session">
          <DetailRow label="model" testId="session-details-model">
            {modelLabel(model.modelProvider, model.model)}
          </DetailRow>
          <DetailRow label="state" testId="session-details-status">
            {model.status.type}
          </DetailRow>
          <DetailRow label="session id" testId="session-details-session-id">
            {model.threadId}
          </DetailRow>
        </Section>
        <Section title="Usage">
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
          {workMs > 0 && (
            <DetailRow label="work time" testId="session-details-work-time">
              {formatWorkDuration(workMs)}
            </DetailRow>
          )}
          {tokens && (
            <DetailRow label={tokensLabel} testId="session-details-tokens">
              <span>↑{formatTokenCount(tokens.inputTokens)}</span>
              <span>↓{formatTokenCount(tokens.outputTokens)}</span>
            </DetailRow>
          )}
          {model.cost && (
            <DetailRow label="cost" testId="session-details-cost">
              {model.cost}
            </DetailRow>
          )}
        </Section>
        <Section title="Location">
          {model.cwd && (
            <DetailRow label="working dir" testId="session-details-cwd">
              <span className={CLASS.path}>{model.cwd}</span>
            </DetailRow>
          )}
          {projectPath && (
            <DetailRow label="project" testId="session-details-project">
              <span className={CLASS.path}>{projectPath}</span>
            </DetailRow>
          )}
          {model.gitBranch && (
            <DetailRow label="branch" testId="session-details-branch">
              {model.gitBranch}
            </DetailRow>
          )}
          {createdAt && (
            <DetailRow label="started" testId="session-details-created">
              {createdAt}
            </DetailRow>
          )}
          {updatedAt && (
            <DetailRow label="last activity" testId="session-details-updated">
              {updatedAt}
            </DetailRow>
          )}
        </Section>
      </Sheet>
    </>
  );
});
