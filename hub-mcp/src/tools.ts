// The twelve tools, as plain handler functions over (ctx, args) so tests
// drive them against a scripted fake hub port. index.ts registers each with
// the MCP server; the schemas live here so the description an agent reads in
// tools/list is authored beside the code that fulfills it.

import { randomUUID } from "node:crypto";
import type { Thread } from "@evener/appwire-client";
import { parseTaskListData, WireError } from "@evener/appwire-client";
import { z } from "zod";

import type { HubConfig } from "./config.js";
import type { ActivityWatcher, EventGroup } from "./events.js";
import { EVENT_GROUPS } from "./events.js";
import type { HubPort } from "./hub.js";
import { fmtAge, truncate } from "./render.js";
import { normalizeRef, sessionDetail, sessionRow } from "./sessions.js";
import { readTranscriptWindow, renderTranscript } from "./transcript.js";

export interface ToolContext {
  port: HubPort;
  watcher: ActivityWatcher;
  config: HubConfig;
  /** Aborted when the MCP client disconnects; long-running tools stop on it. */
  shutdown: AbortController;
}

export interface ToolSpec {
  description: string;
  schema: z.ZodRawShape;
  /** signal is the MCP client's cancellation, when the call carries one. */
  run: (ctx: ToolContext, args: Record<string, unknown>, signal?: AbortSignal) => Promise<string>;
}

// ---- read tools ----

const hubOverview: ToolSpec = {
  description:
    "Orient yourself on the hub: its version and connection state, the daemon fleet (one line per live session daemon, with health), and the recent project directories you can start sessions in. " +
    "Call this first when you take over supervision, before list_sessions or start_session.",
  schema: {},
  run: async (ctx) => {
    const lines: string[] = [];
    const fleet = await ctx.port.request("evener/daemon/list", {});
    const info = ctx.port.info();
    lines.push(
      info
        ? `hub: ${info.name} ${info.version} (source ${info.sourceId}) at ${ctx.port.url()} — connected`
        : `hub at ${ctx.port.url()} — connected (identity unavailable)`,
    );
    if (fleet.daemons.length === 0) {
      lines.push("daemons: none running (no live sessions)");
    } else {
      lines.push(`daemons (${fleet.daemons.length}):`);
      for (const d of fleet.daemons) {
        const health = d.probeState ? `, probe ${d.probeState}` : "";
        lines.push(`  - ${d.name}${d.archived ? " [archived]" : ""}${health}${d.canRetire ? "" : " (retiring)"}`);
      }
    }
    const projects = await ctx.port.request("evener/projects/recent", { limit: 12 });
    if (projects.data.length > 0) {
      lines.push(`recent project directories (usable as cwd for start_session):`);
      for (const project of projects.data) lines.push(`  - ${project}`);
    }
    lines.push(`activity_cursor: ${ctx.watcher.cursor()}`);
    return lines.join("\n");
  },
};

const listSessions: ToolSpec = {
  description:
    "List the sessions this hub knows, newest activity first: one line per session with its ref, state, name, project, model, and last-activity age. " +
    "This is your fleet view — start here for standups and sweeps. Filter by status (idle, active, awaiting, warning, errored, ended) or a search term over names/previews. " +
    "Refs look like local:<id>; pass them unchanged to get_session, read_transcript, send_message, and the other session tools. Subagent sessions are hidden unless you ask for them.",
  schema: {
    status: z
      .string()
      .optional()
      .describe("only sessions in this state: idle, active, awaiting, warning, errored, or ended"),
    search: z.string().optional().describe("substring matched against session names and previews"),
    limit: z.number().int().min(1).max(200).optional().describe("maximum rows to return (default 50)"),
    include_subagents: z
      .boolean()
      .optional()
      .describe("include subagent/delegate sessions in the rows (default false)"),
  },
  run: async (ctx, args) => {
    const status = typeof args.status === "string" ? [args.status] : undefined;
    const response = await ctx.port.request("thread/list", {
      limit: typeof args.limit === "number" ? args.limit : 50,
      statuses: status,
      searchTerm: typeof args.search === "string" ? args.search : undefined,
      includeSubagents: args.include_subagents === true,
    });
    const now = Date.now();
    const rows = response.data.map((t) => sessionRow(t, now));
    if (rows.length === 0) {
      return `no sessions match${typeof args.search === "string" ? ` the search "${args.search}"` : ""}. If you expected sessions, check that they are not archived, or use search_sessions to look in past sessions by content.`;
    }
    const more = response.nextCursor ? `\n(more rows exist; narrow with status/search filters)` : "";
    return `${rows.join("\n")}${more}\n\nactivity_cursor: ${ctx.watcher.cursor()}`;
  },
};

const getSession: ToolSpec = {
  description:
    "Read one session in depth: state, model, context pressure, token use and cost, task progress, goal, queued input, and — most important — what you are currently allowed to do to it (send, steer, queue, interrupt, stop…). " +
    "Call this before acting on a session, and whenever wait_for_activity shows something you need to judge. Also shows background jobs, delegates, and the session's MCP server health.",
  schema: {
    ref: z.string().describe("the session ref from list_sessions or start_session, e.g. local:01ABC…"),
  },
  run: async (ctx, args) => {
    const ref = normalizeRef(String(args.ref));
    const thread = await ctx.watcher.readAndSubscribe(ref);
    return `${sessionDetail(thread)}\n\nactivity_cursor: ${ctx.watcher.cursor()}`;
  },
};

const readTranscript: ToolSpec = {
  description:
    "Read what a session actually said and did, newest turns first, as readable text: user and assistant messages, tool calls with their outcome, errors. " +
    "This is how you review a session's work without reading raw JSON. Default detail is 'outline' (messages in full, each tool call one line); 'full' also includes tool output. " +
    "Pass the returned next_cursor as cursor to page further back in time. The first call after you start or notice a session is usually turns: 5, detail: outline.",
  schema: {
    ref: z.string().describe("the session ref"),
    turns: z
      .number()
      .int()
      .min(1)
      .max(40)
      .optional()
      .describe(
        "how many of the newest turns to render (default 5; the window may be smaller or larger depending on item limits)",
      ),
    cursor: z
      .string()
      .optional()
      .describe("the next_cursor from a previous read_transcript call, to page older history"),
    detail: z
      .enum(["outline", "full"])
      .optional()
      .describe("outline: messages plus one line per tool call; full: also tool output (default outline)"),
  },
  run: async (ctx, args) => {
    const ref = normalizeRef(String(args.ref));
    const detail = args.detail === "full" ? "full" : "outline";
    const window = await readTranscriptWindow(ctx.port, ref, {
      cursor: typeof args.cursor === "string" ? args.cursor : undefined,
    });
    const wanted = typeof args.turns === "number" ? args.turns : 5;
    const turns = window.turns.slice(-wanted);
    const dropped = window.turns.length - turns.length;
    const body = renderTranscript(turns, detail);
    const parts: string[] = [];
    if (turns.length === 0) {
      parts.push(
        `no turns recorded yet for ${ref}; a session spawns its first turn from its initial prompt, so a session you just started with no prompt may have none.`,
      );
    } else {
      parts.push(body);
    }
    if (dropped > 0)
      parts.push(`(${dropped} newer-in-window turns not shown; they fit the page but you asked for ${wanted})`);
    if (window.hasMore && window.nextCursor)
      parts.push(`older history remains — pass next_cursor: ${window.nextCursor}`);
    parts.push(`activity_cursor: ${ctx.watcher.cursor()}`);
    return parts.join("\n\n");
  },
};

const listTasks: ToolSpec = {
  description:
    "Read a session's task list: id, description, status, and dependencies, in execution order. Use it to check progress on a plan you delegated without reading the whole transcript. " +
    "The session's own summary line (get_session) gives you the aggregate; this tool gives you the rows.",
  schema: {
    ref: z.string().describe("the session ref"),
  },
  run: async (ctx, args) => {
    const ref = normalizeRef(String(args.ref));
    const response = await ctx.port.request("evener/tasks/list", { ref });
    const rows = parseTaskListData(response.data);
    if (!rows) return `${ref} has no readable task list (the session may not use one).`;
    if (rows.length === 0) return `${ref} has an empty task list.`;
    const lines = rows.map((task) => {
      const deps = task.dependsOn?.length ? ` (after ${task.dependsOn.join(", ")})` : "";
      const firstNote = task.notes?.[0];
      const notes = firstNote ? ` — ${truncate(firstNote, 80)}` : "";
      return `${task.id}. [${task.status}] ${truncate(task.description, 120)}${deps}${notes}`;
    });
    const done = rows.filter((r) => r.status === "done").length;
    return `${ref} — ${done}/${rows.length} done\n${lines.join("\n")}`;
  },
};

const searchSessions: ToolSpec = {
  description:
    "Find sessions by content across live sessions and saved past ones — the way to answer 'which session dealt with the parser rewrite?' or 'where did we fix this before?'. " +
    "Results are split live/past with refs you can read with get_session/read_transcript (live) or read_transcript alone (past).",
  schema: {
    query: z.string().describe("search text matched against session content and titles"),
  },
  run: async (ctx, args) => {
    const query = String(args.query ?? "").trim();
    if (!query) return "pass a non-empty query.";
    const response = await ctx.port.request("evener/search", { query });
    const render = (rows: typeof response.live, label: string) => {
      if (rows.length === 0) return `${label}: none`;
      return rows
        .map(
          (r) =>
            `${label}: ${r.ref} | ${r.state} | ${truncate(r.title, 60)} | ${truncate(r.project, 40)} | ${r.age}${r.askPending ? " | waiting on a human answer" : ""}${r.approvalPending ? " | waiting on sandbox approval" : ""}`,
        )
        .join("\n");
    };
    return [render(response.live, "live"), render(response.past, "past")].join("\n");
  },
};

export const READ_TOOLS = {
  hub_overview: hubOverview,
  list_sessions: listSessions,
  get_session: getSession,
  read_transcript: readTranscript,
  list_tasks: listTasks,
  search_sessions: searchSessions,
};

// ---- mutation tools ----

function mutationIds(thread: Thread): { clientMutationId: string; expectedInstanceId: string } {
  return {
    clientMutationId: randomUUID(),
    expectedInstanceId: thread.evener.instanceId ?? thread.id,
  };
}

function isConflict(err: unknown): boolean {
  return err instanceof WireError && err.evenerErrorInfo === "conflict";
}

function isOutcomeUnknown(err: unknown): boolean {
  return err instanceof WireError && err.evenerErrorInfo === "mutationOutcomeUnknown";
}

export { isConflict };

const startSession: ToolSpec = {
  description:
    "Start a new agent session on the hub — this is how you delegate a unit of work. Give it a working directory (cwd) and the full work order as the prompt; the session runs it to completion on its own. " +
    "Optional: a name (so list_sessions reads clearly), a model like anthropic/claude-…, a reasoning effort, and max_subagent_depth to bound how deeply it may spawn its own subagents (0 makes it a leaf; default is the hub's). " +
    "Returns the new session's ref. Follow up with wait_for_activity on that ref, then read_transcript to review the work, then send_message for follow-ups.",
  schema: {
    cwd: z.string().describe("absolute working directory the session runs in"),
    prompt: z
      .string()
      .optional()
      .describe(
        "the full work order: context, task, definition of done. An empty prompt starts a dormant session that runs nothing until you send a message.",
      ),
    name: z.string().optional().describe("a short name for the session, shown in list_sessions"),
    model: z
      .string()
      .optional()
      .describe("model as instance/model, e.g. anthropic/claude-sonnet-…; omit for the hub default"),
    reasoning_effort: z.string().optional().describe("reasoning effort for the session (low, medium, high)"),
    max_subagent_depth: z
      .number()
      .int()
      .min(0)
      .max(4)
      .optional()
      .describe("how many levels of subagents the new session may spawn (0 = none)"),
    non_interactive: z
      .boolean()
      .optional()
      .describe("true: the session never waits on human input (auto-answers stick to defaults)"),
  },
  run: async (ctx, args) => {
    const cwd = String(args.cwd ?? "");
    if (!cwd) return "start_session requires cwd.";
    const prompt = typeof args.prompt === "string" ? args.prompt : "";
    const overrides: Record<string, unknown> = {};
    if (typeof args.max_subagent_depth === "number") overrides.maxSubagentDepth = args.max_subagent_depth;
    const response = await ctx.port.request("thread/start", {
      harness: "evener",
      cwd,
      input: prompt ? [{ type: "text", text: prompt }] : [],
      model: typeof args.model === "string" && args.model ? args.model : undefined,
      reasoningEffort:
        typeof args.reasoning_effort === "string" && args.reasoning_effort ? args.reasoning_effort : undefined,
      nonInteractive: args.non_interactive === true ? true : undefined,
      launchOverrides: Object.keys(overrides).length > 0 ? overrides : undefined,
    });
    const thread = response.thread;
    const ref = thread.evener.ref || thread.id;
    await ctx.watcher.readAndSubscribe(ref).catch(() => undefined);
    if (typeof args.name === "string" && args.name && thread.evener.capabilities.rename) {
      await ctx.port.request("evener/thread/name/set", { ref, name: args.name }).catch((err: unknown) => {
        throw new Error(`session started as ${ref} but naming it "${args.name}" failed: ${errText(err)}`);
      });
    }
    const started = response.turn
      ? " and started its first turn"
      : " (dormant: no prompt, so nothing runs until you send_message)";
    return `started ${ref}${started}.\nnext: wait_for_activity(refs=["${ref}"]) to follow it, or get_session for its current state.\n\n${sessionRow(thread)}\nactivity_cursor: ${ctx.watcher.cursor()}`;
  },
};

type SendMode = "auto" | "start" | "steer" | "queue";

const sendMessage: ToolSpec = {
  description:
    "Send a message to a session, handling the busy/idle state machine for you. In the default 'auto' mode: the session is idle → the message starts a new turn; the session is mid-turn → the message steers the running turn (it reaches the model on its next round, without interrupting its work). " +
    "Explicit modes: 'start' (only when idle), 'steer' (only mid-turn), 'queue' (mid-turn, wait for the current turn to finish first). " +
    "Use this to hand a session its next instruction, ask for a status report, or correct course mid-flight.",
  schema: {
    ref: z.string().describe("the session ref"),
    text: z.string().describe("the message body"),
    mode: z
      .enum(["auto", "start", "steer", "queue"])
      .optional()
      .describe(
        "auto (default): start when idle, steer when busy. start/steer/queue force one behavior and error if the state does not allow it.",
      ),
  },
  run: async (ctx, args) => {
    const ref = normalizeRef(String(args.ref));
    const text = String(args.text ?? "");
    if (!text.trim()) return "send_message requires non-empty text.";
    const mode: SendMode = args.mode === "start" || args.mode === "steer" || args.mode === "queue" ? args.mode : "auto";
    let outcome: string;
    try {
      outcome = await attemptSend(ctx, ref, text, mode);
    } catch (err) {
      if (!isConflict(err)) throw err;
      // The session restarted between our read and the write. Re-read once
      // and retry against the new instance id; a second conflict is a real
      // race and surfaces as an error.
      outcome = await attemptSend(ctx, ref, text, mode);
    }
    return `${outcome}\nactivity_cursor: ${ctx.watcher.cursor()}`;
  },
};

async function attemptSend(ctx: ToolContext, ref: string, text: string, mode: SendMode): Promise<string> {
  const thread = await ctx.watcher.readAndSubscribe(ref);
  const caps = thread.evener.capabilities;
  const active = thread.status.type === "active";
  const input = [{ type: "text", text }];
  const ids = mutationIds(thread);
  let chosen: SendMode;
  if (mode !== "auto") {
    chosen = mode;
  } else if (active) {
    chosen = caps.steer ? "steer" : caps.queue ? "queue" : "start";
  } else {
    chosen = "start";
  }
  if (chosen === "steer" && !active) {
    throw new Error(
      `${ref} is ${thread.status.type}, not mid-turn; there is nothing to steer — use mode "start" (or auto).`,
    );
  }
  if (chosen === "start" && !caps.send) {
    throw new Error(
      `${ref} does not accept new turns right now (state ${thread.status.type}${thread.evener.resumeRequired ? ", resume required" : ""}). ` +
        `get_session lists what you can do.`,
    );
  }
  if (chosen === "queue" && !caps.queue) {
    throw new Error(`${ref} does not support queuing while busy (state ${thread.status.type}); steer instead.`);
  }
  try {
    if (chosen === "start") {
      const response = await ctx.port.request("turn/start", {
        ref,
        clientMutationId: ids.clientMutationId,
        expectedInstanceId: ids.expectedInstanceId,
        input,
      });
      return `started turn ${response.turn.id} on ${ref}`;
    }
    if (chosen === "steer") {
      await ctx.port.request("turn/steer", {
        ref,
        clientMutationId: ids.clientMutationId,
        expectedInstanceId: ids.expectedInstanceId,
        input,
      });
      return `steered the running turn on ${ref}; the model sees your message on its next round`;
    }
    await ctx.port.request("turn/queue", {
      ref,
      clientMutationId: ids.clientMutationId,
      expectedInstanceId: ids.expectedInstanceId,
      input,
    });
    const depth = (thread.evener.queue?.depth ?? 0) + 1;
    return `queued on ${ref} (position ${depth}); it runs when the current turn finishes`;
  } catch (err) {
    if (isOutcomeUnknown(err)) {
      throw new Error(
        `${ref}'s hub lost the connection mid-send, so the message's fate is unknown: it may or may not have been delivered. ` +
          `Re-read the session (get_session) and its transcript before deciding to send again — do not blindly resend.`,
      );
    }
    throw err;
  }
}

const interruptSession: ToolSpec = {
  description:
    "Stop a session's running turn without ending the session: the model stops mid-flight, the partial work stays in the transcript, and the session goes idle awaiting your next message. " +
    "Use it when a session is going the wrong way and you want to take control: interrupt, then send_message with the correction. Gentler than stop_session; the session can keep working afterwards.",
  schema: {
    ref: z.string().describe("the session ref"),
  },
  run: async (ctx, args) => {
    const ref = normalizeRef(String(args.ref));
    const thread = await ctx.watcher.readAndSubscribe(ref);
    if (!thread.evener.capabilities.interrupt) {
      throw new Error(
        `${ref} cannot be interrupted right now (state ${thread.status.type}); interrupt acts on a running turn.`,
      );
    }
    const ids = mutationIds(thread);
    await ctx.port.request("turn/interrupt", {
      ref,
      clientMutationId: ids.clientMutationId,
      expectedInstanceId: ids.expectedInstanceId,
    });
    return `interrupted the running turn on ${ref}; it is idle now. Check read_transcript for how far it got, then send_message for the next instruction.`;
  },
};

const stopSession: ToolSpec = {
  description:
    "Stop a session and let its daemon exit. Its transcript stays saved and readable. Use stop_session for finished or hopeless sessions; use interrupt_session when you want it alive but paused. " +
    "force=true escalates to a hard stop for a session that will not shut down (use after a graceful stop did not land); it skips the settle window, so prefer force=false first.",
  schema: {
    ref: z.string().describe("the session ref"),
    force: z.boolean().optional().describe("hard-stop a stuck session instead of a graceful shutdown (default false)"),
  },
  run: async (ctx, args) => {
    const ref = normalizeRef(String(args.ref));
    if (args.force === true) {
      await ctx.port.request("evener/thread/forceStop", { ref });
      return `force-stopped ${ref}. Its transcript remains readable with read_transcript.`;
    }
    const thread = await ctx.watcher.readAndSubscribe(ref);
    if (!thread.evener.capabilities.shutdown) {
      throw new Error(
        `${ref} cannot be shut down from here (state ${thread.status.type}). If it is stuck, retry with force=true.`,
      );
    }
    await ctx.port.request("thread/shutdown", { ref });
    return `stopped ${ref}. Its transcript remains readable with read_transcript.`;
  },
};

const clearQueue: ToolSpec = {
  description:
    "Drop everything queued but not yet running on a session — messages waiting for the current turn to finish. The running turn is untouched. " +
    "Use it when you queued instructions that the session no longer needs (e.g. after a plan change or an interrupt).",
  schema: {
    ref: z.string().describe("the session ref"),
  },
  run: async (ctx, args) => {
    const ref = normalizeRef(String(args.ref));
    const thread = await ctx.watcher.readAndSubscribe(ref);
    if (!thread.evener.capabilities.clear) {
      throw new Error(`${ref} does not support clearing its queue right now (state ${thread.status.type}).`);
    }
    const ids = mutationIds(thread);
    await ctx.port.request("thread/clear", {
      ref,
      clientMutationId: ids.clientMutationId,
      expectedInstanceId: ids.expectedInstanceId,
    });
    const depth = thread.evener.queue?.depth ?? 0;
    return depth > 0
      ? `cleared ${depth} queued message${depth === 1 ? "" : "s"} on ${ref}; the running turn continues.`
      : `queue on ${ref} is already empty; nothing to clear.`;
  },
};

const waitForActivity: ToolSpec = {
  description:
    "Wait for something to happen on the sessions you supervise, instead of polling: returns immediately with buffered activity, or blocks until new matching events arrive (default 30s, max 120s). " +
    "Every session you have listed, read, or started is already watched; name refs to watch new ones. Events come back one per line with a group tag (turns, status, tasks, jobs, delegates, attention, errors) — filter with the events parameter. " +
    "Always pass back the returned activity_cursor as since on the next call so you only see new events. Typical loop: send or start, then wait_for_activity, then read_transcript or get_session for whatever it reports.",
  schema: {
    refs: z
      .array(z.string())
      .optional()
      .describe(
        "refs to wait on (default: every session you have touched this session); new refs are subscribed automatically",
      ),
    events: z
      .array(z.enum(EVENT_GROUPS))
      .optional()
      .describe("groups to wait for: turns, status, tasks, jobs, delegates, attention, errors (default all)"),
    since: z
      .number()
      .optional()
      .describe("the activity_cursor from a previous call; only events after it are returned"),
    timeout_seconds: z
      .number()
      .int()
      .min(1)
      .max(120)
      .optional()
      .describe("how long to wait when nothing matches yet (default 30)"),
    limit: z.number().int().min(1).max(200).optional().describe("maximum events returned (default 50)"),
  },
  run: async (ctx, args, signal) => {
    const refs = Array.isArray(args.refs) ? (args.refs as unknown[]).map((r) => normalizeRef(String(r))) : undefined;
    if (refs && refs.length > 0) {
      await Promise.all(refs.map((r) => ctx.watcher.readAndSubscribe(r).catch(() => undefined)));
    }
    const groups = Array.isArray(args.events) ? (args.events as EventGroup[]) : undefined;
    const timeoutMs = Math.min(
      Math.max(typeof args.timeout_seconds === "number" ? args.timeout_seconds * 1000 : 30_000, 1000),
      120_000,
    );
    // AbortSignal.any merges the client's own cancellation (an interrupt in
    // the calling session) with the server's shutdown, so a disconnect can
    // never leave a wait holding the process open.
    const waitSignal = AbortSignal.any([signal ?? new AbortController().signal, ctx.shutdown.signal]);
    const result = await ctx.watcher.wait({
      refs,
      groups,
      since: typeof args.since === "number" ? args.since : undefined,
      limit: typeof args.limit === "number" ? args.limit : 50,
      timeoutMs,
      signal: waitSignal,
    });
    if (result.events.length === 0) {
      const scope =
        refs && refs.length > 0
          ? `the ${refs.length} named session${refs.length === 1 ? "" : "s"}`
          : "the sessions you are watching";
      return `no matching activity on ${scope} in the last ${Math.round(timeoutMs / 1000)}s — nothing is wrong; work simply has not produced events. Waiting longer? raise timeout_seconds. Still nothing after a long wait? get_session shows whether the session is idle, errored, or waiting on a human.\nactivity_cursor: ${result.cursor}`;
    }
    const lines = result.events.map((e) => `[${e.group}] ${e.ref}: ${e.summary} (${fmtAge(e.atMs)})`);
    return `${lines.join("\n")}\n\nactivity_cursor: ${result.cursor}${result.timedOut ? " (timeout reached; there may be more)" : ""}`;
  },
};

function errText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/** TOOLS is the full registry index.ts installs on the MCP server. */
export const TOOLS = {
  hub_overview: hubOverview,
  list_sessions: listSessions,
  get_session: getSession,
  read_transcript: readTranscript,
  list_tasks: listTasks,
  search_sessions: searchSessions,
  start_session: startSession,
  send_message: sendMessage,
  interrupt_session: interruptSession,
  stop_session: stopSession,
  clear_queue: clearQueue,
  wait_for_activity: waitForActivity,
} as const satisfies Record<string, ToolSpec>;
