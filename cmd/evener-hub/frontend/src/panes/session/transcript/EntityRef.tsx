import {
  activityDelegateState,
  delegateModel,
  type EntityView,
  entityOpenTarget,
  formatByteCount,
  formatClockTime,
  formatElapsed,
  isActivityFailure,
  parseConditionText,
  plainQuoteLine,
  sourceLabel,
  stableDelegateDisplayStatus,
} from "@evener/appwire-client";
import type { ReactNode } from "react";
import { useEntityViews } from "../../../transcriptDisplay/entityViews";
import { HoverCard } from "../../../widgets/hovercard";
import { requireClass } from "../../../widgets/internal/requireClass";
import { OpenButton } from "../../../widgets/openbutton";
import { formatQuietAge, formatUsagePair, jobStatusDisplay } from "../chrome/activityFormat";
import styles from "./entityref.module.css";
import { openTranscript } from "./openTranscript";
import { classifyJobStatus } from "./tools/subagentModuleStore";
import { watchTriggerPhrases } from "./watchConditionPhrase";

export interface EntityRefProps {
  view?: EntityView;
  id: string;
  display?: string;
  triggerOnly?: boolean;
  embedded?: boolean;
}

const CLASS = {
  group: requireClass(styles.group, "entityref.module.css", "group"),
  trigger: requireClass(styles.trigger, "entityref.module.css", "trigger"),
  card: requireClass(styles.card, "entityref.module.css", "card"),
  head: requireClass(styles.head, "entityref.module.css", "head"),
  kind: requireClass(styles.kind, "entityref.module.css", "kind"),
  status: requireClass(styles.status, "entityref.module.css", "status"),
  caption: requireClass(styles.caption, "entityref.module.css", "caption"),
  summary: requireClass(styles.summary, "entityref.module.css", "summary"),
  rows: requireClass(styles.rows, "entityref.module.css", "rows"),
  row: requireClass(styles.row, "entityref.module.css", "row"),
  key: requireClass(styles.key, "entityref.module.css", "key"),
  value: requireClass(styles.value, "entityref.module.css", "value"),
  mono: requireClass(styles.mono, "entityref.module.css", "mono"),
};

function CardRow({ label, value, mono = false }: { label: string; value: ReactNode; mono?: boolean }) {
  if (value === undefined || value === null || value === "") return null;
  return (
    <div className={CLASS.row}>
      <dt className={CLASS.key}>{label}</dt>
      <dd className={`${CLASS.value}${mono ? ` ${CLASS.mono}` : ""}`}>{value}</dd>
    </div>
  );
}

function ViewCaptions({ view }: { view: EntityView }) {
  if (!view.stale && !view.ended) return null;
  return <span className={CLASS.caption}>{view.ended ? "ended" : "stale"}</span>;
}

function elapsedBetween(startedAt: string | undefined, endedAt: string | undefined): string | undefined {
  if (!startedAt || !endedAt) return undefined;
  const start = Date.parse(startedAt);
  const end = Date.parse(endedAt);
  if (Number.isNaN(start) || Number.isNaN(end)) return undefined;
  return formatElapsed(end - start);
}

function usageText(usage: { inputTokens?: number; outputTokens?: number; cacheReadTokens?: number } | undefined) {
  if (usage?.inputTokens === undefined || usage.outputTokens === undefined) return null;
  return formatUsagePair({
    inputTokens: usage.inputTokens,
    outputTokens: usage.outputTokens,
    cacheReadTokens: usage.cacheReadTokens,
  });
}

function JobCard({ view, live }: { view: Extract<EntityView, { kind: "job" }>; live: boolean }) {
  const { job } = view.row;
  const duration = elapsedBetween(job.startedAt, job.endedAt);
  const started = duration === undefined ? formatClockTime(job.startedAt) : undefined;
  const detail = job.description || job.task || job.command;
  const command = job.command && job.command !== detail ? job.command : undefined;
  const statusKind = classifyJobStatus(job.status);
  const failed = isActivityFailure(job.outcome, job.status);
  // The card states the same display word every other surface uses, so a
  // command-outcome run reads "Command failed", never the collapsed or raw
  // machine status.
  const display = jobStatusDisplay(job.status, job.reason);
  const status =
    live || failed || statusKind === "done" || statusKind === "failed" || statusKind === "stopped"
      ? display
      : undefined;
  return (
    <div className={CLASS.card} data-entity-kind="job">
      <div className={CLASS.head}>
        <strong className={CLASS.kind}>Job</strong>
        {status ? (
          <span className={CLASS.status} data-state={failed ? "failed" : statusKind}>
            {status}
          </span>
        ) : null}
        <ViewCaptions view={view} />
      </div>
      {detail ? <div className={CLASS.summary}>{detail}</div> : null}
      <dl className={CLASS.rows}>
        <CardRow label="Type" value={job.type} />
        <CardRow label="Command" value={command} mono />
        <CardRow label={duration ? "Duration" : "Started"} value={duration ?? started} />
        <CardRow label="Exit" value={job.exitCode === undefined ? undefined : `exit ${job.exitCode}`} />
        <CardRow label="Output" value={formatByteCount(job.outputBytes)} />
      </dl>
    </div>
  );
}

function DelegateCard({ view, live }: { view: Extract<EntityView, { kind: "delegate" }>; live: boolean }) {
  let status: string;
  let mandateSource: string | undefined;
  let agent: string | undefined;
  let model: string | undefined;
  let durationMs: number | null | undefined;
  let runningForMs: number | null | undefined;
  let quietForMs: number | null | undefined;
  if (view.row !== undefined) {
    const { delegate } = view.row;
    status = activityDelegateState(delegate).status;
    mandateSource = delegate.mandate ?? delegate.task ?? delegate.description;
    agent = delegate.agentType;
    model = delegateModel(delegate).model;
    durationMs = delegate.durationMs;
    runningForMs = delegate.runningForMs;
    quietForMs = delegate.quietForMs;
  } else {
    const { stable } = view;
    status = stableDelegateDisplayStatus(stable) ?? "unknown";
    mandateSource = stable.task ?? stable.description;
    agent = stable.agentType;
    model = delegateModel(stable).model;
    durationMs = stable.durationMs;
    runningForMs = stable.runningForMs;
    quietForMs = stable.quietForMs;
  }
  const statusKind = classifyJobStatus(status);
  const displayedStatus =
    live || statusKind === "done" || statusKind === "failed" || statusKind === "stopped" ? status : undefined;
  const mandate = mandateSource ? plainQuoteLine(mandateSource) : undefined;
  const duration = durationMs == null ? undefined : formatElapsed(durationMs);
  const running = live && duration === undefined && runningForMs != null ? formatElapsed(runningForMs) : undefined;
  const quiet = live && quietForMs != null ? formatQuietAge(quietForMs) : undefined;
  return (
    <div className={CLASS.card} data-entity-kind="delegate">
      <div className={CLASS.head}>
        <strong className={CLASS.kind}>Delegate</strong>
        {displayedStatus ? (
          <span className={CLASS.status} data-state={statusKind}>
            {displayedStatus}
          </span>
        ) : null}
        <ViewCaptions view={view} />
      </div>
      {mandate ? <div className={CLASS.summary}>{mandate}</div> : null}
      <dl className={CLASS.rows}>
        <CardRow label="Agent" value={agent} />
        <CardRow label="Model" value={model} mono />
        <CardRow label={duration ? "Duration" : "Running"} value={duration ?? running} />
        <CardRow label="Quiet" value={quiet} />
        <CardRow label="Usage" value={usageText(view.row?.delegate.usage ?? view.stable?.usage)} />
      </dl>
    </div>
  );
}

interface WatchConditionContent {
  note?: string;
  summary?: string;
}

function watchConditionContent(condition: string | undefined, note: string | undefined): WatchConditionContent {
  if (!condition) return { note };
  const parsed = parseConditionText(condition, note);
  // The trigger wording comes from the shared composer, so this card and the
  // watch list word the same condition identically: a pattern reads as “ready”
  // here exactly as it does in a list row, and the wildcard as "any event".
  const { timer, bits } = watchTriggerPhrases(parsed);
  const clauses = timer ? [timer, ...bits] : bits;
  return {
    note: note ?? parsed.note,
    summary: clauses.length > 0 ? clauses.join(" · ") : undefined,
  };
}

function WatchCard({ view }: { view: Extract<EntityView, { kind: "watch" }> }) {
  const { watch } = view;
  const condition = watchConditionContent(watch.condition, watch.note);
  const source =
    watch.source !== undefined || watch.state === "watching" || watch.state === "pending"
      ? sourceLabel(watch.source)
      : undefined;
  return (
    <div className={CLASS.card} data-entity-kind="watch">
      <div className={CLASS.head}>
        <strong className={CLASS.kind}>Watch</strong>
        <span className={CLASS.status}>{watch.state}</span>
        <span className={CLASS.caption}>last-known</span>
      </div>
      {condition.summary ? <div className={CLASS.summary}>{condition.summary}</div> : null}
      <dl className={CLASS.rows}>
        <CardRow label="Note" value={condition.note} />
        <CardRow
          label="Deliveries"
          value={
            watch.deliveries === undefined
              ? undefined
              : `${watch.deliveries} ${watch.deliveries === 1 ? "delivery" : "deliveries"}`
          }
        />
        <CardRow label="Source" value={source} mono />
        <CardRow label="Ended" value={watch.endReason} />
      </dl>
    </div>
  );
}

function EntityCard({ view }: { view: EntityView }) {
  const live = !view.stale && !view.ended;
  if (view.kind === "job") return <JobCard view={view} live={live} />;
  if (view.kind === "delegate") return <DelegateCard view={view} live={live} />;
  return <WatchCard view={view} />;
}

export function EntityRef({ view, id, display, triggerOnly, embedded }: EntityRefProps) {
  const entities = useEntityViews();
  const resolved = view ?? entities?.get(id);
  const text = display ?? id;
  if (!resolved) return <span>{text}</span>;

  const target = entityOpenTarget(resolved);
  return (
    <span className={CLASS.group}>
      <HoverCard label={<EntityCard view={resolved} />}>
        {({ describedBy }) => (
          <>
            <span
              data-testid="entity-trigger"
              tabIndex={embedded ? undefined : 0}
              className={CLASS.trigger}
              aria-describedby={describedBy}
            >
              {text}
            </span>
            {target && !triggerOnly ? (
              <OpenButton
                label={resolved.kind === "job" ? "Open job log" : "Open delegate transcript"}
                describedBy={describedBy}
                onClick={() => openTranscript(target.ref, target.parentRef)}
              />
            ) : null}
          </>
        )}
      </HoverCard>
    </span>
  );
}
