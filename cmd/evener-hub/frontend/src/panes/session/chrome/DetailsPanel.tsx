// DetailsPanel: a trigger + Sheet and the shared About body holding a
// session's goal, live accounting, workspace, identity, and collapsible launch
// settings.
//
// Five calm, pill-free groups separated by hairlines:
// 1. Goal (inline-editable everywhere when model.capabilities.goal is true,
//    using the same seamless Textarea node at rest and in edit so text never
//    shifts; explicit Save/Clear/Cancel so idle sessions are never kicked by
//    accident on blur).
// 2. Usage (context readout + 2x2 metric grid).
// 3. Workspace (Directory with CopyButton, Project, Branch, Host — showing
//    host state only when offline so healthy sessions keep one status dot).
// 4. Session (THE single StatusDot on State, Model + inline effort, Session ID
//    with CopyButton, Role, Started, Updated).
// 5. Launch settings (collapsed Disclosure for Sandbox, Network, Plugins).
//
// EVERY accounting and location row is omitted when its value is absent: the
// wire omits what was never measured, and rendering "~$0.00" or "0s" for an
// unmeasured session would be inventing data. An empty group is dropped.

import type { ThreadModel } from "@evener/appwire-client";
import {
  accessFacts,
  effortReadout,
  formatTokenCount,
  loadedPluginNames,
  sessionActionError,
  sessionEffortLevels,
} from "@evener/appwire-client";
import {
  forwardRef,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { animate, spatialTransition, useReducedMotion } from "../../../motion";
import { cadenceStateFor } from "../../../shell/sessionStatus";
import { threadsStore } from "../../../stores/threads";
import {
  Button,
  Chevron,
  CopyButton,
  Disclosure,
  Meter,
  PropertyList,
  type PropertyListRow,
  Sheet,
  StatusDot,
  Textarea,
  useToasts,
} from "../../../widgets";
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
  /** Overrides goal persistence (defaults to threadsStore.setGoal). */
  onSetGoal?: (objective: string) => Promise<unknown>;
}

/** Opens the standalone Details Sheet while keeping its open state local. */
export interface DetailsPanelHandle {
  open: () => void;
}

const CLASS = {
  root: requireClass(styles.root, "detailspanel.module.css", "root"),
  group: requireClass(styles.group, "detailspanel.module.css", "group"),
  goalHead: requireClass(styles.goalHead, "detailspanel.module.css", "goalHead"),
  goalLabel: requireClass(styles.goalLabel, "detailspanel.module.css", "goalLabel"),
  goalBlocked: requireClass(styles.goalBlocked, "detailspanel.module.css", "goalBlocked"),
  goalNote: requireClass(styles.goalNote, "detailspanel.module.css", "goalNote"),
  goalBody: requireClass(styles.goalBody, "detailspanel.module.css", "goalBody"),
  goalField: requireClass(styles.goalField, "detailspanel.module.css", "goalField"),
  goalReadOnly: requireClass(styles.goalReadOnly, "detailspanel.module.css", "goalReadOnly"),
  goalActions: requireClass(styles.goalActions, "detailspanel.module.css", "goalActions"),
  goalActionEnd: requireClass(styles.goalActionEnd, "detailspanel.module.css", "goalActionEnd"),
  contextRow: requireClass(styles.contextRow, "detailspanel.module.css", "contextRow"),
  contextLead: requireClass(styles.contextLead, "detailspanel.module.css", "contextLead"),
  contextGauge: requireClass(styles.contextGauge, "detailspanel.module.css", "contextGauge"),
  contextPercent: requireClass(styles.contextPercent, "detailspanel.module.css", "contextPercent"),
  contextRemaining: requireClass(styles.contextRemaining, "detailspanel.module.css", "contextRemaining"),
  metricGrid: requireClass(styles.metricGrid, "detailspanel.module.css", "metricGrid"),
  metricCell: requireClass(styles.metricCell, "detailspanel.module.css", "metricCell"),
  metricLabel: requireClass(styles.metricLabel, "detailspanel.module.css", "metricLabel"),
  metricValue: requireClass(styles.metricValue, "detailspanel.module.css", "metricValue"),
  value: requireClass(styles.value, "detailspanel.module.css", "value"),
  dim: requireClass(styles.dim, "detailspanel.module.css", "dim"),
  effortInline: requireClass(styles.effortInline, "detailspanel.module.css", "effortInline"),
  hostOffline: requireClass(styles.hostOffline, "detailspanel.module.css", "hostOffline"),
  launchWrap: requireClass(styles.launchWrap, "detailspanel.module.css", "launchWrap"),
  launchTitle: requireClass(styles.launchTitle, "detailspanel.module.css", "launchTitle"),
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

function detailRow(
  label: string,
  testId: string,
  value: ReactNode,
  options: { mono?: boolean; wrap?: boolean; actions?: ReactNode; title?: string } = {},
): PropertyListRow {
  const { mono = false, wrap = true, actions, title } = options;
  return {
    label,
    mono,
    wrap,
    actions,
    value: (
      <span className={CLASS.value} data-testid={testId} title={title}>
        {value}
      </span>
    ),
  };
}

function GoalSection({
  sessionRef,
  objective,
  goalStatus,
  canEdit,
  onSetGoal,
}: {
  sessionRef: string;
  objective: string;
  goalStatus: string;
  canEdit: boolean;
  onSetGoal?: (objective: string) => Promise<unknown>;
}) {
  const toasts = useToasts();
  const reducedMotion = useReducedMotion();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(objective);
  const [saving, setSaving] = useState(false);
  const groupRef = useRef<HTMLElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const priorHeightRef = useRef<number | null>(null);
  const userTransitionRef = useRef(false);

  // Sync incoming goal changes (such as /goal from the composer) while at rest.
  useEffect(() => {
    if (!editing) setDraft(objective);
  }, [objective, editing]);

  // Reset inline edit state when switching sessions.
  // biome-ignore lint/correctness/useExhaustiveDependencies: sessionRef is the identity switch that drops an abandoned local edit
  useEffect(() => {
    userTransitionRef.current = false;
    setEditing(false);
    setSaving(false);
    setDraft(objective);
  }, [sessionRef]);

  const setEditingWithMotion = useCallback((next: boolean) => {
    priorHeightRef.current = bodyRef.current?.offsetHeight ?? null;
    userTransitionRef.current = true;
    setEditing(next);
  }, []);

  // Spatial height morph (docs/web-ui/motion.md §5): mirrors PromptCard's
  // fold/unfold so only user-initiated conversion to/from edit animates.
  // biome-ignore lint/correctness/useExhaustiveDependencies: editing is the deliberate trigger that measures and animates the new height
  useLayoutEffect(() => {
    const body = bodyRef.current;
    const shouldAnimate = userTransitionRef.current;
    userTransitionRef.current = false;
    if (!body) return undefined;
    body.style.removeProperty("height");
    body.style.removeProperty("overflow");
    const from = priorHeightRef.current;
    priorHeightRef.current = null;
    if (!shouldAnimate || reducedMotion || from === null) return undefined;
    const to = body.offsetHeight;
    if (Math.abs(to - from) < 1) return undefined;
    body.style.height = `${from}px`;
    body.style.overflow = "hidden";
    const animation = animate(body, { height: `${to}px` }, spatialTransition());
    animation.finished
      .then(() => {
        body.style.removeProperty("height");
        body.style.removeProperty("overflow");
      })
      .catch(() => {
        // A newer toggle interrupted this animation and owns the height now.
      });
    return () => {
      animation.stop();
      body.style.removeProperty("height");
      body.style.removeProperty("overflow");
    };
  }, [editing, reducedMotion]);

  const cancelEdit = useCallback(() => {
    if (saving) return;
    setDraft(objective);
    setEditingWithMotion(false);
    textareaRef.current?.blur();
  }, [objective, saving, setEditingWithMotion]);

  // Never auto-save on outside click: setting a non-empty goal kicks an idle
  // session immediately, so clicks outside the group cancel cleanly instead.
  useEffect(() => {
    if (!editing) return undefined;
    const doc = groupRef.current?.ownerDocument ?? document;
    const onPointerDown = (event: PointerEvent) => {
      if (saving) return;
      const target = event.target;
      if (target && typeof target === "object" && "nodeType" in target && groupRef.current?.contains(target as Node)) {
        return;
      }
      cancelEdit();
    };
    doc.addEventListener("pointerdown", onPointerDown);
    return () => doc.removeEventListener("pointerdown", onPointerDown);
  }, [editing, saving, cancelEdit]);

  const hasGoal = objective.length > 0;
  const statusKey = goalStatus.toLowerCase();
  // Keep one status indicator in About: suppress the default "active" echo and
  // show only exception/terminal goal states (blocked, complete, etc.).
  const showExceptionStatus = Boolean(hasGoal && goalStatus && statusKey !== "active");

  if (!canEdit && !hasGoal) return null;

  const trimmedDraft = draft.trim();
  const unchangedActive = hasGoal && (statusKey === "active" || statusKey === "") && trimmedDraft === objective;
  const saveDisabled = saving || trimmedDraft === "" || unchangedActive;

  async function commitGoal(nextObjective: string) {
    if (saving) return;
    setSaving(true);
    try {
      const save = onSetGoal ?? ((next: string) => threadsStore.getState().setGoal(sessionRef, next));
      await save(nextObjective);
      setDraft(nextObjective);
      setEditingWithMotion(false);
      textareaRef.current?.blur();
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't set goal", err));
    } finally {
      setSaving(false);
    }
  }

  function handleSave() {
    if (saveDisabled) return;
    void commitGoal(trimmedDraft);
  }

  function handleClear() {
    if (saving || !hasGoal) return;
    void commitGoal("");
  }

  function handleKeyDown(event: ReactKeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === "Escape") {
      // Claim Escape so Overview itself stays open while closing only the editor.
      event.preventDefault();
      event.stopPropagation();
      cancelEdit();
      return;
    }
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
      event.preventDefault();
      handleSave();
    }
  }

  return (
    <section ref={groupRef} className={CLASS.group} aria-label="Goal">
      <div className={CLASS.goalHead}>
        <span className={CLASS.goalLabel}>Goal</span>
        {showExceptionStatus ? (
          <span
            className={statusKey === "blocked" ? CLASS.goalBlocked : CLASS.goalNote}
            data-testid="session-details-goal-status"
            data-state={statusKey}
          >
            {goalStatus}
          </span>
        ) : null}
      </div>

      {canEdit ? (
        <div ref={bodyRef} className={CLASS.goalBody}>
          <div
            className={CLASS.goalField}
            data-testid="session-details-goal"
            data-editing={editing ? "true" : undefined}
          >
            <Textarea
              ref={textareaRef}
              seamless
              fill
              autoGrow
              minLines={1}
              value={editing ? draft : objective}
              placeholder="Add a goal…"
              aria-label={hasGoal ? "Session goal" : "Add a session goal"}
              disabled={saving}
              onFocus={() => {
                if (!editing) {
                  setDraft(objective);
                  setEditingWithMotion(true);
                }
              }}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={handleKeyDown}
            />
          </div>
          {editing ? (
            <div className={CLASS.goalActions}>
              {hasGoal ? (
                <Button variant="quiet" size="xs" onClick={handleClear} disabled={saving}>
                  Clear
                </Button>
              ) : null}
              <div className={CLASS.goalActionEnd}>
                <Button variant="quiet" size="xs" onClick={cancelEdit} disabled={saving}>
                  Cancel
                </Button>
                <Button variant="primary" size="xs" onClick={handleSave} disabled={saveDisabled}>
                  {hasGoal ? "Save" : "Set goal"}
                </Button>
              </div>
            </div>
          ) : null}
        </div>
      ) : (
        <p className={CLASS.goalReadOnly} data-testid="session-details-goal">
          {objective}
        </p>
      )}
    </section>
  );
}

/** Shared details body used by the Overview About tab, mobile Sheet, and
 * standalone Details pane. */
export function DetailsPanelBody({ model, now, resolvedBranch = "", onSetGoal }: DetailsPanelBodyProps) {
  const [launchOpen, setLaunchOpen] = useState(false);
  // biome-ignore lint/correctness/useExhaustiveDependencies: model.ref resets the launch disclosure when switching sessions
  useEffect(() => {
    setLaunchOpen(false);
  }, [model.ref]);
  const status = effectiveContextStatus(model);
  const objective = model.goal?.objective?.trim() ?? "";
  const goalStatus = model.goal?.status?.trim() ?? "";
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
  const branch = model.gitBranch || resolvedBranch;
  // Absent and zero both render nothing: absent is "nobody counted", and a
  // counted zero is not news (model.ts, failedToolCalls).
  const failedToolCalls = model.failedToolCalls ?? 0;
  const access = accessFacts(model);
  const plugins = loadedPluginNames(model);
  const host = useSessionHost(model.ref);
  const effortLevels = sessionEffortLevels(model.reasoningEffortLevels, model.supportsReasoning);
  const effort =
    effortLevels.length > 0
      ? effortReadout(model.reasoningEffort ?? "", effortLevels, model.defaultReasoningEffort)
      : null;
  const queueDepth = model.queue?.depth ?? 0;

  const usageMetrics = [
    model.cost
      ? {
          key: "cost",
          label: "Cost",
          testId: "session-details-cost",
          value: model.cost,
        }
      : null,
    workMs > 0
      ? {
          key: "work",
          label: "Work time",
          testId: "session-details-work-time",
          value: formatWorkDuration(workMs),
        }
      : null,
    tokens
      ? {
          key: "tokens",
          label: tokensLabel,
          testId: "session-details-tokens",
          scope: tokens.scope,
          value: (
            <>
              <span>↑{formatTokenCount(tokens.inputTokens)}</span>
              <span>↓{formatTokenCount(tokens.outputTokens)}</span>
            </>
          ),
        }
      : null,
    failedToolCalls > 0
      ? {
          key: "failed",
          label: "Failed tools",
          testId: "session-details-failed-tool-calls",
          value: String(failedToolCalls),
        }
      : null,
    queueDepth > 0
      ? {
          key: "queue",
          label: "Queue",
          testId: "session-details-queue",
          value: `${queueDepth} queued`,
        }
      : null,
  ].filter((item): item is NonNullable<typeof item> => item !== null);

  const showUsage = showContext || usageMetrics.length > 0;

  const workspaceRows = [
    model.cwd
      ? detailRow("Directory", "session-details-cwd", model.cwd, {
          mono: true,
          wrap: false,
          title: model.cwd,
          actions: <CopyButton text={model.cwd} label="Copy working directory" size="xs" />,
        })
      : null,
    projectPath ? detailRow("Project", "session-details-project", projectPath, { mono: true, wrap: true }) : null,
    branch ? detailRow("Branch", "session-details-branch", branch, { mono: true, wrap: true }) : null,
    host
      ? detailRow(
          "Host",
          "session-details-host",
          <>
            <span>{host.label}</span>
            {host.online === false ? (
              <span className={CLASS.hostOffline} data-testid="session-details-host-state" data-online="false">
                offline
              </span>
            ) : null}
          </>,
        )
      : null,
  ].filter((row): row is PropertyListRow => row !== null);

  const sessionRows = [
    detailRow(
      "State",
      "session-details-state",
      <>
        <StatusDot state={cadenceStateFor(model.status.type, { askPending: model.askPending })} decorative />
        <span>{model.status.type}</span>
      </>,
    ),
    detailRow(
      "Model",
      "session-details-model",
      <>
        <span>{modelLabel(model.modelProvider, model.model)}</span>
        {effort ? (
          <span className={CLASS.effortInline} data-testid="session-details-effort">
            {effort}
          </span>
        ) : null}
      </>,
      { mono: true, wrap: true },
    ),
    detailRow("Session ID", "session-details-id", model.threadId, {
      mono: true,
      wrap: false,
      title: model.threadId,
      actions: <CopyButton text={model.threadId} label="Copy session ID" size="xs" />,
    }),
    model.manager ? detailRow("Role", "session-details-manager", "Manager") : null,
    createdAt ? detailRow("Started", "session-details-created", createdAt) : null,
    updatedAt ? detailRow("Updated", "session-details-updated", updatedAt) : null,
  ].filter((row): row is PropertyListRow => row !== null);

  const launchRows = [
    access ? detailRow("Sandbox", "session-details-sandbox", access.mode) : null,
    access ? detailRow("Network", "session-details-network", access.network) : null,
    plugins
      ? detailRow(
          "Plugins",
          "session-details-plugins",
          plugins.length > 0 ? plugins.join(", ") : <span className={CLASS.dim}>None</span>,
        )
      : null,
  ].filter((row): row is PropertyListRow => row !== null);

  return (
    <div className={CLASS.root}>
      <GoalSection
        sessionRef={model.ref}
        objective={objective}
        goalStatus={goalStatus}
        canEdit={model.capabilities.goal}
        onSetGoal={onSetGoal}
      />

      {showUsage ? (
        <section className={CLASS.group} aria-label="Usage">
          {showContext ? (
            <div className={CLASS.contextRow} data-testid="session-details-context">
              <div className={CLASS.contextLead}>
                <span className={CLASS.contextGauge}>
                  <Meter
                    label={`Context: ${formatTokenCount(model.contextUsed)} of ${capacityMark}${formatTokenCount(status.ceiling)} tokens used, ${percent} percent`}
                    value={model.contextUsed}
                    max={status.ceiling}
                    tone={contextTone(status.pressure)}
                  />
                </span>
                <span className={CLASS.contextPercent}>{percent}%</span>
                <span className={CLASS.dim}>
                  {formatTokenCount(model.contextUsed)} / {capacityMark}
                  {formatTokenCount(status.ceiling)}
                  {windowSuffix}
                </span>
              </div>
              <span className={CLASS.contextRemaining}>
                {capacityMark}
                {formatTokenCount(remaining)} left
              </span>
            </div>
          ) : null}

          {usageMetrics.length > 0 ? (
            <dl className={CLASS.metricGrid}>
              {usageMetrics.map((item) => (
                <div key={item.key} className={CLASS.metricCell}>
                  <dt className={CLASS.metricLabel}>{item.label}</dt>
                  <dd className={CLASS.metricValue} data-testid={item.testId} data-scope={item.scope}>
                    {item.value}
                  </dd>
                </div>
              ))}
            </dl>
          ) : null}
        </section>
      ) : null}

      {workspaceRows.length > 0 ? (
        <section className={CLASS.group} aria-label="Workspace">
          <PropertyList layout="inline" rows={workspaceRows} />
        </section>
      ) : null}

      {sessionRows.length > 0 ? (
        <section className={CLASS.group} aria-label="Session">
          <PropertyList layout="inline" rows={sessionRows} />
        </section>
      ) : null}

      {launchRows.length > 0 ? (
        <section className={CLASS.group} aria-label="Launch settings">
          <div className={CLASS.launchWrap}>
            <Disclosure
              open={launchOpen}
              onOpenChange={setLaunchOpen}
              variant="quiet"
              summary={<span className={CLASS.launchTitle}>Launch settings</span>}
              leading={(open) => <Chevron open={open} />}
            >
              <PropertyList layout="inline" rows={launchRows} />
            </Disclosure>
          </div>
        </section>
      ) : null}
    </div>
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
