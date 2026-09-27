// Session rendering: the one-line row list_sessions prints and the detail
// get_session prints. Both are plain text built for an agent to read and act
// on: refs are canonical and copyable, states are the hub's own vocabulary,
// and numbers carry their units.

import path from "node:path";

import type { Thread } from "@evener/appwire-client";

import { fmtAge, fmtClock, fmtDuration, fmtTokens, truncate } from "./render.js";

/** normalizeRef accepts a bare session id by assuming the local source. */
export function normalizeRef(ref: string): string {
  const trimmed = ref.trim();
  return trimmed.includes(":") ? trimmed : `local:${trimmed}`;
}

function statusWord(t: Thread): string {
  const flags = t.status.activeFlags?.length ? ` [${t.status.activeFlags.join(", ")}]` : "";
  return `${t.status.type}${flags}`;
}

function projectWord(t: Thread): string {
  if (t.projectPath) return t.projectPath;
  return t.cwd;
}

/** pathInside reports whether candidate falls inside dir, after normalization. */
export function pathInside(dir: string, candidate: string): boolean {
  const resolved = path.resolve(candidate);
  return resolved === dir || resolved.startsWith(`${dir}${path.sep}`);
}

/**
 * threadInScope reports whether a thread belongs to the configured project
 * scope: every path the thread names must lie inside it — the working
 * directory always, and the hub-resolved projectPath when one was set,
 * since either can reach outside the scope. Empty projectPath means the
 * hub could not resolve a project (presentation-only on the wire), so only
 * the working directory decides.
 */
export function threadInScope(t: Thread, scope: string): boolean {
  if (!pathInside(scope, t.cwd)) return false;
  if (t.projectPath && !pathInside(scope, t.projectPath)) return false;
  return true;
}

// Thread.CreatedAt/UpdatedAt are epoch SECONDS on the wire (hubcore's
// UnixSeconds), unlike turn and item timestamps, which are milliseconds.
// Every renderer here works in milliseconds, so the thread fields convert
// once, at the boundary.
function threadMs(seconds: number): number {
  return seconds * 1000;
}

function taskWord(t: Thread): string {
  const tasks = t.evener.tasks;
  if (!tasks || tasks.total === 0) return "";
  return `, tasks ${tasks.done}/${tasks.total}`;
}

/** sessionRow renders one fleet line: ref, state, who, where, model, age. */
export function sessionRow(t: Thread, now: number = Date.now()): string {
  const name = t.name || truncate(t.preview, 48);
  const bits = [t.evener.ref || t.id, statusWord(t)];
  const queue = t.evener.queue?.depth ? `, queue ${t.evener.queue.depth}` : "";
  const askPending = t.evener.askPending ? ", ask pending" : "";
  bits.push(`${name}${askPending}${queue}${taskWord(t)}`);
  bits.push(projectWord(t));
  // The wire Thread carries the model in modelProvider (the Go comment: "the
  // model field"); there is no separate model name on the wire.
  bits.push(t.modelProvider);
  bits.push(fmtAge(threadMs(t.updatedAt), now));
  return bits.join(" | ");
}

function usageLine(t: Thread): string {
  const usage = t.evener.usage;
  if (!usage) return "";
  const parts: string[] = [];
  if (usage.inputTokens) parts.push(`in ${fmtTokens(usage.inputTokens)}`);
  if (usage.outputTokens) parts.push(`out ${fmtTokens(usage.outputTokens)}`);
  if (usage.cacheReadTokens) parts.push(`cache ${fmtTokens(usage.cacheReadTokens)}`);
  return parts.join(", ");
}

function contextLine(t: Thread): string {
  const e = t.evener;
  if (e.contextUsed === undefined || e.contextWindow === undefined) return "";
  const pct = e.contextWindow > 0 ? Math.round((e.contextUsed / e.contextWindow) * 100) : 0;
  return `context: ${fmtTokens(e.contextUsed)} of ${fmtTokens(e.contextWindow)} tokens (${pct}% used)`;
}

/** sessionDetail renders the full get_session report. */
export function sessionDetail(t: Thread, now: number = Date.now()): string {
  const lines: string[] = [];
  const ref = t.evener.ref || t.id;
  lines.push(`${ref} — ${t.name || truncate(t.preview, 120)}`);
  lines.push(
    `state: ${statusWord(t)}${t.evener.askPending ? " — waiting on a human answer" : ""}` +
      `${t.evener.resumeRequired ? " (resume required: resume it with resume_session)" : ""}`,
  );
  lines.push(`working in: ${t.cwd}${t.projectPath && t.projectPath !== t.cwd ? ` (project: ${t.projectPath})` : ""}`);
  lines.push(`model: ${t.modelProvider}${t.evener.profile ? `, profile ${t.evener.profile}` : ""}`);
  const activeTurn = t.evener.activeTurnId ? `running turn ${t.evener.activeTurnId}` : "no turn running";
  const work = t.evener.workMillis ? `, worked ${fmtDuration(t.evener.workMillis)}` : "";
  lines.push(`turn: ${activeTurn}${work}`);
  lines.push(`created: ${fmtClock(threadMs(t.createdAt), now)}`);
  lines.push(`updated: ${fmtClock(threadMs(t.updatedAt), now)}`);
  const turnCount = t.evener.turnCount !== undefined ? `turns completed: ${t.evener.turnCount}` : "";
  const usage = usageLine(t);
  lines.push(
    [turnCount, usage, t.evener.cost ? `cost ${t.evener.cost}` : ""].filter(Boolean).join(", ") ||
      "no usage recorded yet",
  );
  const context = contextLine(t);
  if (context) lines.push(context);
  // failedToolCalls is a pointer on the wire: nil means nobody counted (an
  // old daemon, an unreadable transcript) — render nothing, never a
  // fabricated 0. A real 0 is good news and says so.
  if (t.evener.failedToolCalls !== undefined) lines.push(`failed tool calls: ${t.evener.failedToolCalls}`);
  if (t.evener.tasks) lines.push(`tasks: ${t.evener.tasks.done}/${t.evener.tasks.total} done`);
  if (t.evener.goal) {
    lines.push(
      `goal (${t.evener.goal.status}, iteration ${t.evener.goal.iterations}): ${truncate(t.evener.goal.objective ?? "", 160)}`,
    );
  }
  const queueDepth = t.evener.queue?.depth ?? 0;
  if (queueDepth > 0 && t.evener.queue?.preview?.length) {
    lines.push(`queued input (${queueDepth}):`);
    for (const preview of t.evener.queue.preview.slice(0, 3)) {
      lines.push(`  - ${truncate(preview, 100)}`);
    }
  }
  const caps = t.evener.capabilities;
  lines.push(
    `you can: ${
      [
        // The wire has no resume capability flag: resumeRequired itself is
        // what makes resume_session applicable, so it gates the word here.
        t.evener.resumeRequired ? "resume" : null,
        caps.send ? "send" : null,
        caps.steer ? "steer" : null,
        caps.queue ? "queue" : null,
        caps.interrupt ? "interrupt" : null,
        caps.clear ? "clear-queue" : null,
        caps.shutdown ? "stop" : null,
        caps.rename ? "rename" : null,
      ]
        .filter(Boolean)
        .join(", ") || "nothing (session ended or daemon gone)"
    }`,
  );
  // These capabilities exist on the hub but this MCP exposes no tool for
  // them; advertising them here as if callable would be a lie by omission
  // of the means.
  const uiOnly = [
    caps.compact ? "compact" : null,
    caps.forkFromTurn ? "fork" : null,
    caps.changeModel ? "change-model" : null,
    caps.goal ? "set-goal" : null,
  ].filter(Boolean);
  if (uiOnly.length > 0) lines.push(`hub UI only: ${uiOnly.join(", ")}`);
  const diag = t.evener.diagnostics;
  if (diag) {
    if (diag.jobs?.length) {
      lines.push(
        `background jobs (${diag.jobs.length}): ${diag.jobs
          .map((j) => `${j.jobType}/${j.status}`)
          .slice(0, 6)
          .join(", ")}`,
      );
    }
    if (diag.delegates?.length) {
      lines.push(
        `delegates (${diag.delegates.length}): ${diag.delegates
          .map((d) => `${d.childSessionId}:${d.lifecycle}/${d.phase}`)
          .slice(0, 6)
          .join(", ")}`,
      );
    }
    if (diag.watches?.length) {
      lines.push(
        `watches (${diag.watches.length}): ${diag.watches
          .map((w) => w.id)
          .slice(0, 6)
          .join(", ")}`,
      );
    }
    if (diag.mcp?.length) {
      const bad = diag.mcp.filter((m) => m.status && m.status !== "connected");
      lines.push(
        `mcp servers (${diag.mcp.length}): ${diag.mcp
          .map((m) => `${m.name}=${m.status ?? "?"}`)
          .slice(0, 6)
          .join(", ")}` + (bad.length ? ` — check ${bad.map((m) => m.name).join(", ")}` : ""),
      );
    }
  }
  if (t.forkedFromId) lines.push(`forked from ${t.forkedFromId}`);
  if (t.gitInfo) lines.push(`git: ${truncate(JSON.stringify(t.gitInfo), 160)}`);
  return lines.join("\n");
}
