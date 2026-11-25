import { LOCAL_HOST, type Source } from "@evener/appwire-client";
import { boardState, hostLabeler, whyLine } from "@evener/appwire-client/state/navigation";
import { cadenceStateFor, SIGNAL_STATES } from "../sessionStatus";
import { effectiveSessionState, type RailNode, type SessionRailNode } from "./railNodes";

export interface RailFilterContext {
  query: string;
  hostLabel: (hostId: string) => string;
  expanded: ReadonlyMap<string, boolean>;
  now: number;
  projectName?: string;
  hostName?: string;
}

export function createRailFilterContext(
  rawQuery: string,
  sources: readonly Source[],
  expanded: ReadonlyMap<string, boolean>,
  now = Date.now(),
): RailFilterContext | null {
  const query = rawQuery.trim().toLowerCase();
  return query.length > 0 ? { query, hostLabel: hostLabeler(sources), expanded, now } : null;
}

function hasMatch(query: string, ...values: readonly (string | undefined)[]): boolean {
  return values.some((value) => value !== undefined && value.length > 0 && value.toLowerCase().includes(query));
}

function matchesSession(node: SessionRailNode, ctx: RailFilterContext): boolean {
  const { session } = node;
  const { query } = ctx;
  const directMatch =
    hasMatch(query, session.title, session.project, ctx.projectName, node.liveHost?.label, ctx.hostName) ||
    hasMatch(query, session.question?.text, session.tasks?.current, session.last_message, session.failure?.title) ||
    hasMatch(query, session.running_job_intent, session.running_job_command, session.approval_tool) ||
    hasMatch(query, session.approval_target) ||
    (session.host_id !== LOCAL_HOST && hasMatch(query, session.host_id, ctx.hostLabel(session.host_id)));
  if (directMatch) return true;
  const effectiveState = effectiveSessionState(session);
  const flags = { askPending: session.ask_pending === true, approvalPending: session.approval_pending === true };
  const signalState = cadenceStateFor(effectiveState, flags);
  const state = node.live ?? boardState({ ...session, state: effectiveState }, false);
  const line = whyLine({ row: session, state }, undefined, 0, ctx.now);
  const whyText = line === null ? undefined : line.kind === "reason" ? line.word : line.text;
  const notStarted = session.dormant === true && !SIGNAL_STATES.has(signalState) ? "Not started" : undefined;
  return hasMatch(query, whyText, notStarted);
}

// Filters a rail tree in place while preserving matching ancestors and
// auto-expanding branches without mutating persisted expansion overrides.
// Overflow and loading placeholders drop out while filtering so archived
// auto-load observers cannot loop on empty pages, except on unloaded projects
// matched by name so the user can still expand and load them.
export function filterRailNodes(nodes: readonly RailNode[], ctx: RailFilterContext): RailNode[] {
  const result: RailNode[] = [];
  for (const node of nodes) {
    if (node.kind === "overflow" || node.kind === "loading") continue;
    if (node.kind === "session") {
      const children = node.children.length > 0 ? filterRailNodes(node.children, ctx) : [];
      if (children.length > 0) result.push({ ...node, expanded: ctx.expanded.get(node.id) ?? true, children });
      else if (matchesSession(node, ctx)) result.push(node.children.length === 0 ? node : { ...node, children: [] });
      continue;
    }
    const projectName = node.kind === "project" ? (node.displayName ?? node.project.name) : undefined;
    const nextCtx =
      projectName !== undefined
        ? { ...ctx, projectName }
        : node.kind === "host"
          ? { ...ctx, hostName: node.host.label }
          : ctx;
    const children = filterRailNodes(node.children, nextCtx);
    if (children.length > 0) {
      result.push({ ...node, expanded: ctx.expanded.get(node.id) ?? true, children });
    } else if (node.kind === "project" && hasMatch(ctx.query, projectName)) {
      const kept = node.children.filter((child) => child.kind !== "overflow");
      result.push({ ...node, expanded: ctx.expanded.get(node.id) ?? false, children: kept });
    }
  }
  return result;
}

export function countSessionNodes(nodes: readonly RailNode[]): number {
  let total = 0;
  for (const node of nodes) {
    if (node.kind === "session") total += 1;
    else if (node.kind === "project" && !node.project.loaded && node.children.every((c) => c.kind === "loading")) {
      total += node.project.session_count ?? 0;
      continue;
    }
    if (node.kind !== "overflow" && node.kind !== "loading") total += countSessionNodes(node.children);
  }
  return total;
}
