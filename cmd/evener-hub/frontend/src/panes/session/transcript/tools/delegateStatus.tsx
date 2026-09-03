// DelegateStatusBody is the expanded body for a `job_status` tool call
// targeting a stable delegate (dlg_*). It parses the tool's structured state
// (item.raw, the stableDelegateStatusResult from
// agent/session_tools_jobs.go's projectStableDelegateStatus) and renders it
// as a structured card — not raw JSON — following the design system's
// information architecture: identity, lifecycle meta, mandate as markdown,
// environment, config, available tools, footer with transcript ref + copy.
//
// For a job_status call whose output isn't a delegate status (a shell job,
// or malformed JSON), it falls back to HeadClippedOutputBody — the previous
// behavior — so the renderer never regresses for non-delegate targets.
import type { ReactNode } from "react";
import { disclosureScopeForSession, useTranscriptRenderContext } from "../../../../transcriptDisplay/renderContext";
import { Chip, type ChipTone, CopyButton, Disclosure, Markdown } from "../../../../widgets";
import { scopedDisclosureId } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { formatClockTime, formatElapsed, splitMandate } from "../messages/format";
import { OpenTranscriptButton } from "../openTranscript";
import type { ToolRenderProps } from "../toolRenderers";
import { HeadClippedOutputBody } from "./bodies";
import styles from "./delegateStatus.module.css";
import { classifyJobStatus } from "./subagentModuleStore";

const CLASS = {
  card: requireClass(styles.card, "delegateStatus.module.css", "card"),
  head: requireClass(styles.head, "delegateStatus.module.css", "head"),
  headId: requireClass(styles.headId, "delegateStatus.module.css", "headId"),
  headSpacer: requireClass(styles.headSpacer, "delegateStatus.module.css", "headSpacer"),
  meta: requireClass(styles.meta, "delegateStatus.module.css", "meta"),
  metaSeg: requireClass(styles.metaSeg, "delegateStatus.module.css", "metaSeg"),
  metaSep: requireClass(styles.metaSep, "delegateStatus.module.css", "metaSep"),
  metaK: requireClass(styles.metaK, "delegateStatus.module.css", "metaK"),
  metaV: requireClass(styles.metaV, "delegateStatus.module.css", "metaV"),
  mandate: requireClass(styles.mandate, "delegateStatus.module.css", "mandate"),
  eyebrow: requireClass(styles.eyebrow, "delegateStatus.module.css", "eyebrow"),
  mandateBody: requireClass(styles.mandateBody, "delegateStatus.module.css", "mandateBody"),
  diagnostic: requireClass(styles.diagnostic, "delegateStatus.module.css", "diagnostic"),
  diagnosticBody: requireClass(styles.diagnosticBody, "delegateStatus.module.css", "diagnosticBody"),
  dangerText: requireClass(styles.dangerText, "delegateStatus.module.css", "dangerText"),
  env: requireClass(styles.env, "delegateStatus.module.css", "env"),
  envRows: requireClass(styles.envRows, "delegateStatus.module.css", "envRows"),
  envRow: requireClass(styles.envRow, "delegateStatus.module.css", "envRow"),
  envK: requireClass(styles.envK, "delegateStatus.module.css", "envK"),
  envV: requireClass(styles.envV, "delegateStatus.module.css", "envV"),
  envVmono: requireClass(styles.envVmono, "delegateStatus.module.css", "envVmono"),
  envVmuted: requireClass(styles.envVmuted, "delegateStatus.module.css", "envVmuted"),
  tools: requireClass(styles.tools, "delegateStatus.module.css", "tools"),
  toolsChips: requireClass(styles.toolsChips, "delegateStatus.module.css", "toolsChips"),
  toolChip: requireClass(styles.toolChip, "delegateStatus.module.css", "toolChip"),
  footer: requireClass(styles.footer, "delegateStatus.module.css", "footer"),
  footerSpacer: requireClass(styles.footerSpacer, "delegateStatus.module.css", "footerSpacer"),
};

// The shape of stableDelegateStatusResult (agent/session_tools_jobs.go).
// Fields are optional because item.raw may be undefined or partial on
// older stored transcripts predating a field's addition.
interface DelegateStatusState {
  id?: string;
  type?: string;
  status?: string;
  task?: string;
  description?: string;
  agent_type?: string;
  tools?: string[];
  model?: string;
  reasoning_effort?: string;
  resumable?: boolean;
  needs_attention?: boolean;
  not_resumable_reason?: string;
  transcript_ref?: string;
  run_started_at?: string;
  latest_activity_at?: string;
  running_for_ms?: number;
  quiet_for_ms?: number;
  duration_ms?: number;
  last_outcome?: { status?: string; ended_at?: string; reason?: string };
  cwd?: string;
  isolation?: string;
  sandbox_mode?: string;
  sandbox_network?: boolean;
}

// Coerce a value to a string if it's a string, or undefined if not. Fields
// rendered as React children must be strings — an object or number would
// render as "[object Object]" or throw. The raw originates from a typed Go
// struct, so non-string values are a legacy-transcript edge case.
function strOrUndef(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

function normalizeDelegateStatus(raw: Record<string, unknown>): DelegateStatusState {
  const lo = raw.last_outcome;
  // Spread the original last_outcome so backend-added fields (exhaustion_budget,
  // exhaustion_limit, etc.) survive into the copy-JSON output, then override
  // the rendered fields with coerced strings so they can't throw.
  const lastOutcome =
    typeof lo === "object" && lo !== null && !Array.isArray(lo)
      ? {
          ...(lo as Record<string, unknown>),
          status: strOrUndef((lo as Record<string, unknown>).status),
          ended_at: strOrUndef((lo as Record<string, unknown>).ended_at),
          reason: strOrUndef((lo as Record<string, unknown>).reason),
        }
      : undefined;
  return {
    id: strOrUndef(raw.id),
    type: strOrUndef(raw.type),
    status: strOrUndef(raw.status),
    task: strOrUndef(raw.task),
    description: strOrUndef(raw.description),
    agent_type: strOrUndef(raw.agent_type),
    tools: Array.isArray(raw.tools) ? raw.tools.filter((t): t is string => typeof t === "string") : undefined,
    model: strOrUndef(raw.model),
    reasoning_effort: strOrUndef(raw.reasoning_effort),
    resumable: typeof raw.resumable === "boolean" ? raw.resumable : undefined,
    needs_attention: typeof raw.needs_attention === "boolean" ? raw.needs_attention : undefined,
    not_resumable_reason: strOrUndef(raw.not_resumable_reason),
    transcript_ref: strOrUndef(raw.transcript_ref),
    run_started_at: strOrUndef(raw.run_started_at),
    latest_activity_at: strOrUndef(raw.latest_activity_at),
    running_for_ms: typeof raw.running_for_ms === "number" ? raw.running_for_ms : undefined,
    quiet_for_ms: typeof raw.quiet_for_ms === "number" ? raw.quiet_for_ms : undefined,
    duration_ms: typeof raw.duration_ms === "number" ? raw.duration_ms : undefined,
    last_outcome: lastOutcome,
    cwd: strOrUndef(raw.cwd),
    isolation: strOrUndef(raw.isolation),
    sandbox_mode: strOrUndef(raw.sandbox_mode),
    sandbox_network: typeof raw.sandbox_network === "boolean" ? raw.sandbox_network : undefined,
  };
}

function isDelegateStatus(raw: unknown): raw is DelegateStatusState {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) return false;
  const obj = raw as Record<string, unknown>;
  // The stableDelegateStatusResult always carries type "delegate", id, and
  // status. Require all three so a malformed or partial raw (e.g. a shell
  // job's state, or a stored transcript predating the struct) falls back
  // to HeadClippedOutputBody instead of crashing during rendering.
  if (obj.type !== "delegate") return false;
  if (typeof obj.id !== "string" || typeof obj.status !== "string") return false;
  return true;
}

// Map a delegate's status + needs_attention to a Chip tone, reusing
// classifyJobStatus's status vocabulary so failed→danger (not neutral).
// An idle delegate whose last run failed should still show danger —
// the lifecycle is idle, but the outcome is the most recent fact.
// A running delegate always shows alive regardless of last_outcome —
// the live lifecycle is the primary signal, not a past outcome.
//
// "exhausted" is NOT classified as "failed" here: a budget exhaustion is
// a soft stop, not a hard error. classifyJobStatus maps it to "failed"
// for the activity rail (where it's a terminal state), but the delegate
// status card distinguishes the two: exhausted→attention (amber, "ran
// out of budget"), failed→danger (red, "something went wrong").
function isExhausted(status: string | undefined): boolean {
  return status === "exhausted";
}

function statusToTone(state: DelegateStatusState): ChipTone {
  if (state.needs_attention) return "attention";
  if (state.status === "idle") {
    // Idle lifecycle: show the last run's outcome, not "running".
    if (state.last_outcome?.status) {
      if (isExhausted(state.last_outcome.status)) return "attention";
      const kind = classifyJobStatus(state.last_outcome.status);
      if (kind === "failed") return "danger";
    }
    return "neutral";
  }
  const kind = classifyJobStatus(state.status);
  if (kind === "running") return "alive";
  if (kind === "failed") return "danger";
  return "neutral";
}

function statusLabel(state: DelegateStatusState): string {
  if (state.needs_attention) return "Needs attention";
  return state.status ?? "unknown";
}

interface MetaSegment {
  key: string;
  value: ReactNode;
}

function lifecycleSegments(state: DelegateStatusState): MetaSegment[] {
  const segs: MetaSegment[] = [];
  if (state.running_for_ms !== undefined) {
    segs.push({ key: "running", value: formatElapsed(state.running_for_ms) });
  } else if (state.duration_ms !== undefined) {
    segs.push({ key: "ran for", value: formatElapsed(state.duration_ms) });
  }
  if (state.quiet_for_ms !== undefined) {
    segs.push({ key: "quiet", value: formatElapsed(state.quiet_for_ms) });
  }
  if (state.last_outcome?.status) {
    segs.push({ key: "last outcome", value: state.last_outcome.status });
  }
  const started = formatClockTime(state.run_started_at);
  if (started) segs.push({ key: "started", value: started });
  return segs;
}

function configSegments(state: DelegateStatusState): MetaSegment[] {
  const segs: MetaSegment[] = [];
  if (state.model) segs.push({ key: "model", value: state.model });
  if (state.agent_type) segs.push({ key: "agent", value: state.agent_type });
  if (state.reasoning_effort) segs.push({ key: "reasoning", value: state.reasoning_effort });
  if (state.resumable !== undefined) segs.push({ key: "resumable", value: state.resumable ? "✓" : "✗" });
  return segs;
}

interface EnvRow {
  key: string;
  value: string;
  mono?: boolean;
  muted?: boolean;
}

function environmentRows(state: DelegateStatusState): EnvRow[] {
  const rows: EnvRow[] = [];
  if (state.cwd) rows.push({ key: "cwd", value: state.cwd, mono: true });
  if (state.isolation) rows.push({ key: "isolation", value: state.isolation, muted: true });
  if (state.sandbox_mode) rows.push({ key: "sandbox", value: state.sandbox_mode, mono: true });
  if (state.sandbox_network !== undefined) {
    rows.push({ key: "network", value: state.sandbox_network ? "enabled" : "disabled" });
  }
  return rows;
}

function envValueClass(row: EnvRow): string {
  // Always include envV for the shared truncation rules (overflow, ellipsis,
  // nowrap); envVmono layers on the mono font/size, envVmuted the muted color.
  const classes = [CLASS.envV];
  if (row.mono) classes.push(CLASS.envVmono);
  if (row.muted) classes.push(CLASS.envVmuted);
  return classes.join(" ");
}

function MetaLine({ segments }: { segments: MetaSegment[] }) {
  if (segments.length === 0) return null;
  return (
    <div className={CLASS.meta}>
      {segments.map((seg, i) => (
        <span key={seg.key} className={CLASS.metaSeg}>
          {i > 0 && (
            <span className={CLASS.metaSep} aria-hidden="true">
              ·
            </span>
          )}
          <span className={CLASS.metaK}>{seg.key}</span> <span className={CLASS.metaV}>{seg.value}</span>
        </span>
      ))}
    </div>
  );
}

function EnvTable({ rows }: { rows: EnvRow[] }) {
  if (rows.length === 0) return null;
  return (
    <div className={CLASS.env}>
      <div className={CLASS.eyebrow}>Environment</div>
      <div className={CLASS.envRows}>
        {rows.map((row) => (
          <div key={row.key} className={CLASS.envRow}>
            <span className={CLASS.envK}>{row.key}</span>
            <span className={envValueClass(row)}>{row.value}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

export function DelegateStatusBody({ item, sessionRef }: ToolRenderProps) {
  // Session-scope the mandate disclosure ID so the same delegate's cards in
  // different sessions don't share disclosure state (same pattern as
  // ToolCallItem.tsx's disclosureKey). Called before the early return to
  // satisfy React's rules-of-hooks (hooks must be unconditional).
  const renderCtx = useTranscriptRenderContext();

  // item.raw carries the stableDelegateStatusResult struct (the State field
  // of tool.StateResult). Fall back to HeadClippedOutputBody when it isn't
  // a delegate status — a job_status call targeting a shell job, or a
  // stored transcript predating the structured state.
  if (!isDelegateStatus(item.raw)) {
    return <HeadClippedOutputBody item={item} live={false} />;
  }

  // Normalize all fields to their declared types so rendering can't throw on
  // malformed persisted data (e.g. model: {} from a legacy transcript).
  const state = normalizeDelegateStatus(item.raw as Record<string, unknown>);
  const tone = statusToTone(state);
  const label = statusLabel(state);
  const mandate = splitMandate(state.task);
  const lifecycle = lifecycleSegments(state);
  const config = configSegments(state);
  const envRows = environmentRows(state);
  const tools = state.tools ?? [];
  const transcriptRef = state.transcript_ref;
  // Copy the structured state, not item.output: the output text may carry
  // breaker/annotation text appended after the JSON, so re-serializing the
  // validated state gives the reader valid JSON on paste.
  const rawJson = JSON.stringify(state, null, 2);

  const disclosureScope = disclosureScopeForSession(renderCtx, sessionRef);
  const mandateDisclosureId = scopedDisclosureId(disclosureScope, `delegate-mandate:${item.id}`);

  return (
    <div className={CLASS.card} data-testid="delegate-status-body">
      {/* Header: delegate ID + status chip */}
      <div className={CLASS.head}>
        <span className={CLASS.headId}>{state.id}</span>
        <span className={CLASS.headSpacer} />
        <Chip tone={tone}>{label}</Chip>
      </div>

      {/* Lifecycle meta: inline ·-separated segments */}
      <MetaLine segments={lifecycle} />

      {/* Mandate: task as markdown, first paragraph visible, rest in disclosure */}
      {mandate && (
        <div className={CLASS.mandate}>
          <div className={CLASS.eyebrow}>Mandate</div>
          <div className={CLASS.mandateBody}>
            <Markdown source={mandate.first} />
          </div>
          {mandate.rest && (
            <Disclosure id={mandateDisclosureId} summary="Show full mandate">
              <Markdown source={mandate.rest} />
            </Disclosure>
          )}
        </div>
      )}

      {/* Environment: meta table rows */}
      <EnvTable rows={envRows} />

      {/* Config: one inline line */}
      {config.length > 0 && <MetaLine segments={config} />}

      {/* Diagnostics: not-resumable reason and/or last outcome reason.
          All non-success terminal outcomes render their reason: failed→danger,
          exhausted/cancelled/stopped→neutral (soft stops, not errors). */}
      {state.not_resumable_reason && (
        <div className={CLASS.diagnostic} data-testid="delegate-not-resumable">
          <div className={`${CLASS.diagnosticBody} ${CLASS.dangerText}`}>
            Not resumable: {state.not_resumable_reason}
          </div>
        </div>
      )}
      {state.last_outcome?.reason && isExhausted(state.last_outcome.status) && (
        <div className={CLASS.diagnostic} data-testid="delegate-outcome-reason">
          <div className={CLASS.diagnosticBody}>Last run exhausted: {state.last_outcome.reason}</div>
        </div>
      )}
      {state.last_outcome?.reason &&
        !isExhausted(state.last_outcome.status) &&
        classifyJobStatus(state.last_outcome.status) === "failed" && (
          <div className={CLASS.diagnostic} data-testid="delegate-outcome-reason">
            <div className={`${CLASS.diagnosticBody} ${CLASS.dangerText}`}>
              Last run failed: {state.last_outcome.reason}
            </div>
          </div>
        )}
      {state.last_outcome?.reason && classifyJobStatus(state.last_outcome.status) === "stopped" && (
        <div className={CLASS.diagnostic} data-testid="delegate-outcome-reason">
          <div className={CLASS.diagnosticBody}>
            Last run {state.last_outcome.status === "cancelled" ? "cancelled" : "stopped"}: {state.last_outcome.reason}
          </div>
        </div>
      )}

      {/* Available tools */}
      {tools.length > 0 && (
        <div className={CLASS.tools}>
          <div className={CLASS.eyebrow}>{`Available tools (${tools.length})`}</div>
          <div className={CLASS.toolsChips}>
            {tools.map((tool) => (
              <span key={tool} className={CLASS.toolChip}>
                {tool}
              </span>
            ))}
          </div>
        </div>
      )}

      {/* Footer: transcript ref + copy raw JSON */}
      <div className={CLASS.footer}>
        {transcriptRef && <OpenTranscriptButton transcriptRef={transcriptRef} parentRef={sessionRef} />}
        <span className={CLASS.footerSpacer} />
        <CopyButton text={rawJson} label="Copy raw JSON" />
      </div>
    </div>
  );
}
