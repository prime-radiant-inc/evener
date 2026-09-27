// The activity stream: hub notifications become bounded, readable events a
// supervising agent can wait on. Two halves:
//
//  - classify() renders one notification into at most a few one-line events,
//    grouped by what a supervisor does with them. Token-delta notifications
//    are never events: a PM wants turn boundaries and outcomes, not streams.
//  - ActivityWatcher owns the hub subscriptions that make notifications flow
//    (the hub relays per-ref only to clients that subscribed, via
//    thread/read's subscribe flag), the ring buffer of recent events, and
//    wait() — the blocking primitive wait_for_activity exposes.

import { randomUUID } from "node:crypto";

import type { AnyNotification, Thread } from "@evener/appwire-client";

import type { HubPort } from "./hub.js";
import { firstLine, fmtDuration, truncate } from "./render.js";
import { normalizeRef } from "./sessions.js";

export const EVENT_GROUPS = ["turns", "status", "tasks", "jobs", "delegates", "attention", "errors"] as const;
export type EventGroup = (typeof EVENT_GROUPS)[number];

export interface ActivityEvent {
  /** Monotonic per-process sequence number; cursor() renders it with the process epoch. */
  seq: number;
  atMs: number;
  ref: string;
  group: EventGroup;
  method: string;
  summary: string;
}

interface Classified {
  ref: string;
  group: EventGroup;
  method: string;
  summary: string;
}

// Status vocabulary: hubcore.NormalizeState fixes the set a client sees.
// activeFlags ride along on the status notification.
function statusSummary(status: { type?: string; activeFlags?: string[] }): string {
  const flags = status.activeFlags?.length ? ` [${status.activeFlags.join(", ")}]` : "";
  return `status: ${status.type ?? "unknown"}${flags}`;
}

function itemSummary(
  kind: "started" | "completed",
  item: {
    type?: string;
    toolName?: string;
    text?: string;
    description?: string;
    error?: string;
    exitCode?: number;
    status?: string;
  },
): string {
  const type = item.type ?? "item";
  const label = item.toolName ? `${type} ${item.toolName}` : type;
  if (kind === "started") {
    const what = item.description
      ? `: ${truncate(item.description, 100)}`
      : item.text
        ? `: ${truncate(item.text, 100)}`
        : "";
    return `${label} started${what}`;
  }
  const bits: string[] = [];
  if (item.error) bits.push(`error: ${truncate(item.error, 160)}`);
  if (typeof item.exitCode === "number") bits.push(`exit ${item.exitCode}`);
  if (item.text && type === "agentMessage") bits.push(truncate(item.text, 160));
  const tail = bits.length > 0 ? ` — ${bits.join("; ")}` : "";
  return `${label} completed${tail}`;
}

/**
 * classify renders one hub notification into zero or more events. Unlisted
 * methods (deltas, hub-settings churn) are not events. The mapping is
 * exhaustive over NOTIFICATION_NAMES' PM-relevant subset; the default case
 * skips rather than guesses.
 */
export function classify(n: AnyNotification): Classified[] {
  const p = n.params as Record<string, unknown>;
  const ref = typeof p.ref === "string" ? p.ref : typeof p.threadId === "string" ? p.threadId : "";
  const method = n.method;
  switch (method) {
    case "turn/started":
      return [{ ref, group: "turns", method, summary: "turn started" }];
    case "turn/completed": {
      const turn = p.turn as
        | { status?: string; error?: { message?: string }; durationMs?: number; cost?: string }
        | undefined;
      const bits: string[] = [];
      if (turn?.status) bits.push(turn.status);
      const dur = fmtDuration(turn?.durationMs);
      if (dur) bits.push(dur);
      if (turn?.cost) bits.push(turn.cost);
      const failed = turn?.error?.message ? ` — failed: ${truncate(turn.error.message, 160)}` : "";
      return [{ ref, group: "turns", method, summary: `turn completed (${bits.join(", ")})${failed}` }];
    }
    case "item/started": {
      const item = p.item as Parameters<typeof itemSummary>[1];
      return [{ ref, group: "turns", method, summary: itemSummary("started", item ?? {}) }];
    }
    case "item/completed": {
      const item = p.item as Parameters<typeof itemSummary>[1];
      return [{ ref, group: "turns", method, summary: itemSummary("completed", item ?? {}) }];
    }
    case "evener/steering/injected": {
      const kind = typeof p.kind === "string" ? p.kind : "steering";
      const text = firstLine(typeof p.text === "string" ? p.text : undefined, 120);
      return [{ ref, group: "turns", method, summary: `steering injected (${kind})${text ? `: ${text}` : ""}` }];
    }
    case "thread/status/changed": {
      const status = p.status as { type?: string; activeFlags?: string[] } | undefined;
      return [{ ref, group: "status", method, summary: statusSummary(status ?? { type: "unknown" }) }];
    }
    case "thread/closed": {
      const reason = typeof p.reason === "string" && p.reason ? ` (${truncate(p.reason, 120)})` : "";
      return [{ ref, group: "status", method, summary: `session closed${reason}` }];
    }
    case "thread/queueChanged": {
      const queue = p.queue as { depth?: number; preview?: string[] } | undefined;
      const depth = queue?.depth ?? 0;
      const first = queue?.preview?.[0] ? ` — next: ${truncate(queue.preview[0], 100)}` : "";
      return [{ ref, group: "status", method, summary: `queue changed: depth ${depth}${first}` }];
    }
    case "evener/thread/name/changed":
      return [{ ref, group: "status", method, summary: `renamed to "${typeof p.name === "string" ? p.name : "?"}"` }];
    case "thread/model/changed": {
      const provider = typeof p.modelProvider === "string" ? p.modelProvider : "?";
      const model = typeof p.model === "string" ? p.model : "?";
      return [{ ref, group: "status", method, summary: `model changed to ${provider}/${model}` }];
    }
    case "evener/thread/resync":
      return [{ ref, group: "status", method, summary: "hub asked readers to re-read this session (resync)" }];
    case "evener/task/updated": {
      const done = typeof p.done === "number" ? p.done : 0;
      const total = typeof p.total === "number" ? p.total : 0;
      const current = (p.current as { description?: string } | undefined)?.description;
      return [
        {
          ref,
          group: "tasks",
          method,
          summary: `tasks: ${done}/${total} done${current ? ` — current: ${truncate(current, 100)}` : ""}`,
        },
      ];
    }
    case "evener/goal/updated": {
      const goal = p.goal as { objective?: string; status?: string; iterations?: number } | null | undefined;
      if (!goal) return [{ ref, group: "tasks", method, summary: "goal cleared" }];
      const objective = goal.objective ? `: ${truncate(goal.objective, 100)}` : "";
      return [
        {
          ref,
          group: "tasks",
          method,
          summary: `goal ${goal.status ?? "updated"} (iteration ${goal.iterations ?? 0})${objective}`,
        },
      ];
    }
    case "evener/job/started":
    case "evener/job/finished": {
      const job = p.job as { jobId?: string; jobType?: string; status?: string; exitCode?: number } | undefined;
      const what = job ? `${job.jobType ?? "job"} ${job.jobId ?? ""}`.trim() : "job";
      const outcome =
        method === "evener/job/finished"
          ? ` (${job?.status ?? "?"}${typeof job?.exitCode === "number" ? `, exit ${job.exitCode}` : ""})`
          : "";
      return [
        {
          ref,
          group: "jobs",
          method,
          summary: `${what} ${method === "evener/job/started" ? "started" : `finished${outcome}`}`,
        },
      ];
    }
    case "evener/jobs/treeUpdated":
      return [{ ref, group: "jobs", method, summary: "background job tree changed" }];
    case "evener/delegate/updated": {
      const d = p.delegate as
        | { childSessionId?: string; lifecycle?: string; phase?: string; status?: string; outcome?: string }
        | undefined;
      const who = d?.childSessionId ?? "delegate";
      const state = [d?.lifecycle, d?.phase, d?.status].filter(Boolean).join("/");
      const outcome = d?.outcome ? ` → ${d.outcome}` : "";
      return [{ ref, group: "delegates", method, summary: `delegate ${who}: ${state}${outcome}` }];
    }
    case "evener/attention/changed": {
      const changed =
        (p.changed as Array<{
          threadId?: string;
          title?: string;
          project?: string;
          level?: string;
          prevLevel?: string;
          askPending?: boolean;
          approvalPending?: boolean;
        }>) ?? [];
      return changed.map((c) => ({
        // AttentionEntry.ID is the BARE session id on the wire, without the
        // "local:" prefix every other notification's ref carries — normalize
        // it or no wait keyed on local:<id> can ever match this group.
        ref: c.threadId ? normalizeRef(c.threadId) : "",
        group: "attention" as EventGroup,
        method,
        summary: `attention ${c.prevLevel ?? "?"} → ${c.level ?? "?"}: ${c.title ?? c.project ?? "session"}${c.askPending ? " (waiting on a human answer)" : ""}${c.approvalPending ? " (waiting on sandbox approval)" : ""}`,
      }));
    }
    case "evener/sandbox/escalation/requested":
    case "evener/sandbox/escalation/resolved": {
      const verb = method === "evener/sandbox/escalation/requested" ? "requested" : "resolved";
      return [{ ref, group: "attention", method, summary: `sandbox escalation ${verb}` }];
    }
    case "warning": {
      const message =
        typeof p.message === "string" ? p.message : typeof p.title === "string" ? p.title : "unnamed warning";
      return [{ ref, group: "errors", method, summary: `warning: ${truncate(message, 160)}` }];
    }
    case "evener/thread/modelRetry": {
      const attempt = typeof p.attempt === "number" ? p.attempt : "?";
      const max = typeof p.maxAttempts === "number" ? p.maxAttempts : "?";
      const delay = fmtDuration(typeof p.delayMs === "number" ? p.delayMs : undefined);
      const reason = typeof p.message === "string" ? `: ${truncate(p.message, 120)}` : "";
      return [
        {
          ref,
          group: "errors",
          method,
          summary: `model call retrying (${attempt}/${max}, next in ${delay ?? "?"})${reason}`,
        },
      ];
    }
    default:
      // Deltas (item/agentMessage/delta, item/toolOutput/delta, reasoning
      // summary deltas, reset) and hub-wide churn (auth, launch, navigation,
      // marketplace, plugin, settings, notes, urls) are deliberately not
      // events: nothing a supervisor waits on changes at that grain.
      return [];
  }
}

export interface WaitQuery {
  refs?: string[];
  groups?: EventGroup[];
  /**
   * The activity_cursor ("<epoch>:<seq>") from a previous call. A cursor from
   * another epoch — a restart of this server process, or the bare-number
   * format an older process printed — means those events were lost: the wait
   * starts from now and the result says the stream restarted.
   */
  since?: string;
  limit?: number;
}

export interface WaitResult {
  events: ActivityEvent[];
  /** The position to pass back as `since`: "<epoch>:<seq>". */
  cursor: string;
  timedOut: boolean;
  /** Matching events dropped because they did not fit `limit`. */
  skipped?: number;
  /** True when `since` came from another stream epoch: the wait started from now. */
  restarted?: boolean;
  /** True when same-epoch events since the cursor may have been evicted from the buffer. */
  evicted?: boolean;
}

/** The resolved form of WaitQuery the matcher works against. */
interface ResolvedQuery {
  refs?: string[];
  groups?: EventGroup[];
  sinceSeq: number;
  limit?: number;
}

/** One wait's cursor resolution: a seq position, plus what the cursor claimed. */
interface SinceMeta {
  seq: number;
  restarted: boolean;
  /** True only when the cursor named this process's own epoch. */
  sameEpoch: boolean;
}

interface Waiter {
  query: ResolvedQuery;
  meta: SinceMeta;
  resolve: (result: WaitResult) => void;
  timer: ReturnType<typeof setTimeout> | null;
  settled: boolean;
}

const DEFAULT_CAPACITY = 1000;

/**
 * ActivityWatcher turns a HubPort into the event stream. start() must be
 * called once; onReady re-subscribes every tracked ref after a reconnect
 * (subscriptions live on the connection that made them, so a reconnect drops
 * them — the same recovery the web apps' stores do).
 */
export class ActivityWatcher {
  private readonly port: HubPort;
  private readonly capacity: number;
  /** Per-process epoch: a restart is a new process, so seq alone cannot name a position across one. */
  private readonly epoch = randomUUID().slice(0, 8);
  private readonly buffer: ActivityEvent[] = [];
  private readonly subscribed = new Set<string>();
  private readonly waiters = new Set<Waiter>();
  private seq = 0;
  private everEvicted = false;
  private started = false;
  private readonly diagnostic: (message: string) => void;

  constructor(port: HubPort, opts?: { capacity?: number; diagnostic?: (message: string) => void }) {
    this.port = port;
    this.capacity = opts?.capacity ?? DEFAULT_CAPACITY;
    this.diagnostic = opts?.diagnostic ?? (() => {});
  }

  start(): void {
    if (this.started) return;
    this.started = true;
    this.port.onNotification((n) => this.handle(n));
    this.port.onReady(() => {
      this.resubscribeAll();
    });
  }

  private handle(n: AnyNotification): void {
    for (const c of classify(n)) {
      const event: ActivityEvent = {
        seq: ++this.seq,
        atMs: Date.now(),
        ref: c.ref,
        group: c.group,
        method: c.method,
        summary: c.summary,
      };
      this.buffer.push(event);
      if (this.buffer.length > this.capacity) {
        this.buffer.splice(0, this.buffer.length - this.capacity);
        this.everEvicted = true;
      }
      this.pokeWaiters(event);
    }
  }

  private pokeWaiters(event: ActivityEvent): void {
    for (const waiter of this.waiters) {
      if (waiter.settled) continue;
      if (this.matches(waiter.query, event)) {
        this.settle(waiter, this.buildResult(waiter.query, waiter.meta, false));
      }
    }
  }

  private settle(waiter: Waiter, result: WaitResult): void {
    waiter.settled = true;
    if (waiter.timer) clearTimeout(waiter.timer);
    this.waiters.delete(waiter);
    waiter.resolve(result);
  }

  private matches(query: ResolvedQuery, event: ActivityEvent): boolean {
    if (event.seq <= query.sinceSeq) return false;
    if (query.refs && query.refs.length > 0 && !query.refs.includes(event.ref)) return false;
    if (query.groups && query.groups.length > 0 && !query.groups.includes(event.group)) return false;
    return true;
  }

  /**
   * collect gathers every match in the buffer, keeping only the newest
   * `limit` and counting the rest as skipped — silently dropping older
   * matches would answer "all quiet" over events that did happen.
   */
  private collect(query: ResolvedQuery): { events: ActivityEvent[]; skipped: number } {
    const limit = query.limit ?? 50;
    const matching: ActivityEvent[] = [];
    for (let i = this.buffer.length - 1; i >= 0; i--) {
      const event = this.buffer[i];
      if (event && this.matches(query, event)) matching.unshift(event);
    }
    const skipped = Math.max(0, matching.length - limit);
    return { events: skipped > 0 ? matching.slice(matching.length - limit) : matching, skipped };
  }

  /**
   * resolveSince turns a cursor string into a seq position. Cursors from
   * another epoch — including the bare-number format an older process
   * printed — cannot be honored: those events were lost with the old
   * process, so the wait starts from now and says the stream restarted.
   */
  private resolveSince(since: string | undefined): SinceMeta {
    if (since === undefined) return { seq: 0, restarted: false, sameEpoch: false };
    const match = /^([^:]+):(\d+)$/.exec(since);
    if (match && match[1] === this.epoch) return { seq: Number(match[2]), restarted: false, sameEpoch: true };
    return { seq: this.seq, restarted: true, sameEpoch: false };
  }

  /** evictedSince reports whether matches between the cursor and the buffer's oldest event may have been dropped. */
  private evictedSince(meta: SinceMeta): boolean {
    if (!meta.sameEpoch || !this.everEvicted) return false;
    const oldest = this.buffer[0]?.seq;
    return oldest === undefined || meta.seq < oldest - 1;
  }

  private buildResult(query: ResolvedQuery, meta: SinceMeta, timedOut: boolean): WaitResult {
    const { events, skipped } = this.collect(query);
    return {
      events,
      cursor: this.cursor(),
      timedOut,
      skipped,
      restarted: meta.restarted,
      evicted: this.evictedSince(meta),
    };
  }

  /** cursor returns the newest event's position as "<epoch>:<seq>". */
  cursor(): string {
    return `${this.epoch}:${this.seq}`;
  }

  /** subscribedRefs snapshots the refs this watcher receives events for. */
  subscribedRefs(): string[] {
    return [...this.subscribed];
  }

  /**
   * readAndSubscribe reads a thread and subscribes to its notifications in
   * one call — the same thread/read the web apps send, with subscribe on.
   * Every tool that names a ref goes through here, so anything the PM has
   * looked at is waitable without an extra step.
   */
  async readAndSubscribe(ref: string): Promise<Thread> {
    const response = await this.port.request("thread/read", { ref, includeTurns: false, subscribe: true });
    this.subscribed.add(ref);
    return response.thread;
  }

  private async subscribeQuietly(ref: string): Promise<void> {
    try {
      await this.port.request("thread/read", { ref, includeTurns: false, subscribe: true });
    } catch (err) {
      this.diagnostic(`resubscribe ${ref} failed: ${err instanceof Error ? err.message : String(err)}`);
    }
  }

  private resubscribeAll(): void {
    for (const ref of this.subscribed) {
      void this.subscribeQuietly(ref);
    }
  }

  /**
   * wait returns matching buffered events after the `since` cursor
   * immediately; with none it blocks until one arrives or timeoutMs passes.
   * A `since` from another epoch starts from now. An aborted signal (the MCP
   * client cancelling the call) resolves with whatever is known.
   */
  wait(query: WaitQuery & { timeoutMs: number; signal?: AbortSignal }): Promise<WaitResult> {
    const meta = this.resolveSince(query.since);
    const full: ResolvedQuery = {
      refs: query.refs && query.refs.length > 0 ? query.refs : this.subscribedRefs(),
      groups: query.groups,
      sinceSeq: meta.seq,
      limit: query.limit,
    };
    const immediate = this.collect(full);
    if (immediate.events.length > 0 || query.timeoutMs <= 0) {
      return Promise.resolve({
        events: immediate.events,
        cursor: this.cursor(),
        timedOut: immediate.events.length === 0,
        skipped: immediate.skipped,
        restarted: meta.restarted,
        evicted: this.evictedSince(meta),
      });
    }
    return new Promise<WaitResult>((resolve) => {
      const waiter: Waiter = {
        query: full,
        meta,
        resolve,
        timer: null,
        settled: false,
      };
      const finish = (result: WaitResult) => {
        if (waiter.settled) return;
        this.settle(waiter, result);
      };
      waiter.timer = setTimeout(() => {
        finish(this.buildResult(full, meta, true));
      }, query.timeoutMs);
      if (query.signal) {
        query.signal.addEventListener(
          "abort",
          () => {
            finish(this.buildResult(full, meta, false));
          },
          { once: true },
        );
      }
      this.waiters.add(waiter);
    });
  }
}
