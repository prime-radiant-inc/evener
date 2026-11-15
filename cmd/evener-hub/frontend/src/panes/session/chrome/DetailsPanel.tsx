// DetailsPanel: a trigger + Sheet holding a session's full accounting - who it
// is (model, state, id), what it has spent (context, work time, tokens, cost,
// failed tool calls), what it was started with and cannot change (sandbox,
// network, plugins), and where it runs (host, cwd, project, branch, when it
// began and was last written).
//
// The status row beside it is a glanceable strip: a context METER, a clock,
// arrows. This panel is where the same facts get room to be precise ("42%
// used · 42K / 100K · 58K left" rather than a 64px gauge), plus the facts the
// strip has no room for at all, so a reader deciding whether to compact, or
// reporting what a session cost, has the numbers instead of a shape.
//
// Rows are grouped (Session / Usage / Set at launch / Location) the way the
// panel this replaced grouped its own (cmd/evener-hub/web_workspace.go's
// detailsSections on the legacy renderer): a dozen flat rows do not scan. The
// launch group's title carries the one thing its rows share - they were chosen
// when the session started and hold for its lifetime - so no row repeats it.
//
// EVERY row is omitted when its value is absent. That is the panel's central
// rule, and the reason a session's whole usage section can vanish: the wire
// omits what was never measured, and a zero there is Go's unset zero value,
// not a measurement of zero. Rendering "~$0.00" or "0s" for an unmeasured
// session would be inventing data. A section left with no rows drops its
// heading too.
//
// Every value comes off the live ThreadModel the status row already reads - no
// fetch for accounting. Manager is the session's own start-time property.
// The host row is named from the navigation manifest the shell already holds
// (useSessionHost), and appears only on a hub that has more than one host.
// The branch row is the one caller-supplied exception: nothing populates the
// wire's thread.gitInfo on the hub's local read paths, so the About tab and the
// desktop session Details pane pass a branch resolved from the session's cwd at
// view time (resolvedBranch) - a display lookup the caller owns, never a fetch
// this panel makes.
//
// Cost is EvenerThread.Cost: the "~$X.XX" string appwire.EstimateCost derives
// server-side from the thread's cumulative usage at the model's catalog price.
// The pricing table never crosses the wire, so the string is shown verbatim -
// never re-formatted, and never derived client-side even when the token total
// beside it was. An absent cost (no token data, or an uncataloged model) is an
// honest unknown and renders no row at all.

import type { ThreadModel } from "@evener/appwire-client";
import { accessFacts, formatTokenCount, loadedPluginNames } from "@evener/appwire-client";
import { forwardRef, type ReactNode, useImperativeHandle, useState } from "react";
import { Button, Meter, PropertyList, Sheet, type StackedPropertyListRow } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { formatTimestamp, sessionTokens, tokenUnitLabel } from "./detailsAccounting";
import styles from "./detailspanel.module.css";
import {
  assumedWindowMark,
  contextTone,
  effectiveContextStatus,
  formatWorkDuration,
  modelLabel,
  totalWorkMillis,
} from "./statusFormat";
import { useSessionHost } from "./useSessionHost";

export interface DetailsPanelProps {
  model: ThreadModel;
  /** Current wall-clock ms, so the work-time figure keeps counting during an
   * in-flight turn (owned by SessionChrome's useNowTick, same as StatusRow). */
  now: number;
  /** Hides the standalone Sheet's inline trigger. Callers can still open
   * this Sheet through DetailsPanelHandle, independently of Overview. */
  hideTrigger?: boolean;
}

export interface DetailsPanelBodyProps {
  model: ThreadModel;
  now: number;
  /** Branch resolved at view time from the session's cwd on this hub - the
   * About tab's and the desktop Details pane's fallback for local sessions,
   * whose read paths never populated the wire's gitInfo. The model's own
   * gitBranch wins when present; empty means "looked, nothing to show" and
   * renders no row. */
  resolvedBranch?: string;
}

/** Opens the standalone Details Sheet while keeping its open state local. */
export interface DetailsPanelHandle {
  open: () => void;
}

const CLASS = {
  section: requireClass(styles.section, "detailspanel.module.css", "section"),
  sectionTitle: requireClass(styles.sectionTitle, "detailspanel.module.css", "sectionTitle"),
  value: requireClass(styles.value, "detailspanel.module.css", "value"),
  meter: requireClass(styles.meter, "detailspanel.module.css", "meter"),
  dim: requireClass(styles.dim, "detailspanel.module.css", "dim"),
};

// A finished session has no live context occupancy to report: the hub builds
// an exited session's thread from its persisted SessionMeta, which carries
// work time, cumulative usage, and cost but no context figures at all
// (cmd/evener-hub/app_threadread.go's pastEntryThread). The wire's zeroes there
// mean "not measured", so the row is omitted rather than shown reading empty.
// Matches the TUI drawer's own ContextPressure > 0 guard.
function isEndedStatus(type: string): boolean {
  return type === "ended" || type === "closed";
}

// detailRow builds one PropertyList row whose value carries the row's own
// testid, so a caller can find a fact without matching its label text.
// `scope` says what the row's figure counts, when that can vary (the token
// total).
function detailRow(
  label: string,
  testId: string,
  value: ReactNode,
  mono = false,
  scope?: string,
): StackedPropertyListRow {
  return {
    label,
    mono,
    value: (
      <span className={CLASS.value} data-testid={testId} data-scope={scope}>
        {value}
      </span>
    ),
  };
}

// Section renders a titled group, or nothing at all when every row inside it
// was omitted for want of data - a heading over an empty list would advertise
// facts the panel does not have. The caller's `value && detailRow(...)`
// guards arrive here as falsy entries, which is what gets filtered out.
function Section({ title, rows }: { title: string; rows: (StackedPropertyListRow | false | null | undefined)[] }) {
  const present = rows.filter((row): row is StackedPropertyListRow => Boolean(row));
  if (present.length === 0) return null;
  return (
    <section className={CLASS.section}>
      <h3 className={CLASS.sectionTitle}>{title}</h3>
      <PropertyList layout="stacked" rows={present} />
    </section>
  );
}

/** Shared details body used by the mobile Sheet and desktop pane. */
export function DetailsPanelBody({ model, now, resolvedBranch = "" }: DetailsPanelBodyProps) {
  const status = effectiveContextStatus(model);
  const showContext = status.ceiling > 0 && !isEndedStatus(model.status.type);
  const remaining = status.remaining;
  const percent = Math.round(status.pressure * 100);
  const capacityMark = status.hasCustomThreshold ? "" : assumedWindowMark(model.contextAssumed);
  const customThresholdWindowNote = model.contextAssumed
    ? " (window unknown)"
    : ` (${formatTokenCount(model.contextWindow)} window)`;
  const windowSuffix = status.hasCustomThreshold ? customThresholdWindowNote : "";

  const workMs = totalWorkMillis(model.workMillis, model.activeTurnStartedAt, now);
  const tokens = sessionTokens(model);
  // A derived sum over a windowed transcript covers only the turns in hand
  // (thread/read's itemLimit), so its label says exactly that rather than
  // passing a partial figure off as the session's total (tokenUnitLabel,
  // shared with the native transcript footer).
  const tokensLabel = tokenUnitLabel(tokens?.scope);
  const createdAt = formatTimestamp(model.createdAt);
  const updatedAt = formatTimestamp(model.updatedAt);
  // A project path identical to the cwd is the common case (a session in its
  // own project root); repeating it under a second label says nothing.
  const projectPath = model.projectPath && model.projectPath !== model.cwd ? model.projectPath : undefined;
  // The branch row's value: the wire's gitBranch when the thread carries one,
  // else the caller's view-time resolution. Both absent renders no row, like
  // every absent fact in this panel.
  const branch = model.gitBranch || resolvedBranch;
  // Absent and zero both render nothing: absent is "nobody counted", and a
  // counted zero is not news (model.ts, failedToolCalls).
  const failedToolCalls = model.failedToolCalls ?? 0;
  const access = accessFacts(model);
  const plugins = loadedPluginNames(model);
  const host = useSessionHost(model.ref);

  return (
    <>
      <Section
        title="Session"
        rows={[
          detailRow("model", "session-details-model", modelLabel(model.modelProvider, model.model), true),
          detailRow("state", "session-details-state", model.status.type),
          detailRow("session id", "session-details-id", model.threadId, true),
          model.manager && detailRow("role", "session-details-manager", "Manager"),
        ]}
      />
      <Section
        title="Usage"
        rows={[
          showContext &&
            detailRow(
              "context",
              "session-details-context",
              <>
                <span className={CLASS.meter}>
                  <Meter
                    label={`Context: ${formatTokenCount(model.contextUsed)} of ${capacityMark}${formatTokenCount(status.ceiling)} tokens used, ${percent} percent`}
                    value={model.contextUsed}
                    max={status.ceiling}
                    tone={contextTone(status.pressure)}
                  />
                </span>
                <span>{percent}% used</span>
                <span className={CLASS.dim}>
                  {formatTokenCount(model.contextUsed)} / {capacityMark}
                  {formatTokenCount(status.ceiling)}
                  {windowSuffix}
                </span>
                <span className={CLASS.dim}>
                  {capacityMark}
                  {formatTokenCount(remaining)} left
                </span>
              </>,
            ),
          workMs > 0 && detailRow("work time", "session-details-work-time", formatWorkDuration(workMs)),
          tokens &&
            detailRow(
              tokensLabel,
              "session-details-tokens",
              <>
                <span>↑{formatTokenCount(tokens.inputTokens)}</span>
                <span>↓{formatTokenCount(tokens.outputTokens)}</span>
              </>,
              false,
              tokens.scope,
            ),
          !!model.cost && detailRow("cost", "session-details-cost", model.cost),
          failedToolCalls > 0 &&
            detailRow("failed tool calls", "session-details-failed-tool-calls", String(failedToolCalls)),
        ]}
      />
      <Section
        title="Set at launch"
        rows={[
          access && detailRow("sandbox", "session-details-sandbox", access.mode),
          access && detailRow("network", "session-details-network", access.network),
          plugins &&
            detailRow(
              "plugins",
              "session-details-plugins",
              // An authoritative empty inventory is a fact worth a row: it
              // answers "why is my plugin's command missing here".
              plugins.length > 0 ? plugins.join(", ") : <span className={CLASS.dim}>None</span>,
            ),
        ]}
      />
      <Section
        title="Location"
        rows={[
          host &&
            detailRow(
              "host",
              "session-details-host",
              <>
                <span>{host.label}</span>
                {/* Stated in words in both directions: the panel is where a
                    reader checks, so an online host says so instead of leaving
                    it to the absence of a warning. Unknown says nothing. */}
                {host.online !== null && (
                  <span className={CLASS.dim} data-testid="session-details-host-state" data-online={host.online}>
                    {host.online ? "online" : "offline"}
                  </span>
                )}
              </>,
            ),
          !!model.cwd && detailRow("working dir", "session-details-cwd", model.cwd, true),
          !!projectPath && detailRow("project", "session-details-project", projectPath, true),
          !!branch && detailRow("branch", "session-details-branch", branch, true),
          !!createdAt && detailRow("started", "session-details-created", createdAt),
          !!updatedAt && detailRow("last activity", "session-details-updated", updatedAt),
        ]}
      />
    </>
  );
}

export const DetailsPanel = forwardRef<DetailsPanelHandle, DetailsPanelProps>(function DetailsPanel(
  { model, now, hideTrigger = false },
  ref,
) {
  const [open, setOpen] = useState(false);
  useImperativeHandle(ref, () => ({ open: () => setOpen(true) }), []);

  return (
    <>
      {/* This optional Sheet trigger is separate from Overview and the
          standalone Details workspace pane. */}
      {!hideTrigger && (
        <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
          Details
        </Button>
      )}
      <Sheet open={open} onClose={() => setOpen(false)} title="Session details">
        {open ? <DetailsPanelBody model={model} now={now} /> : null}
      </Sheet>
    </>
  );
});
