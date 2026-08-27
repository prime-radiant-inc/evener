// ActivityStore — Zustand state wrapping an ActivityService. Owns the current
// ActivityView projection (never the raw wire Thread) and exposes project()
// (re-project from a Thread), setView() (adopt a pre-projected view + identity
// from the read boundary), applyNotification() (patch from activity
// notifications, returning "applied" | "rehydrate" | "ignored"), and reset()
// (clear on conversation switch).
//
// Identity safety (CRITICAL): the store tracks an ActivityIdentity
// { threadId, ref, generation }. Every patch compares the supplied identity
// with the current identity. Wrong thread/ref/generation is ignored even when
// a new view exists. reset() bumps the generation so a late completion from
// the older conversation is dropped. setView() installs both the sanitized
// view and the identity atomically.
//
// The store owns NO signal-only coalescer. evener/jobs/treeUpdated returns
// "rehydrate" so the caller (conversation reslice A) can schedule the one
// authoritative reread. Notification payload labels use safe operation
// type/kind only — never description, task prompt, command, path, profile ID,
// ref, or transcript ID.

import { create } from "zustand";
import type {
  AnyNotification,
  Thread,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
  type ActivityView,
  createActivityService as defaultService,
  type RedactedDiagnostic,
  type WorkEntry,
  type WorkKind,
  type WorkTone,
} from "../services/activity";

export type ActivityStatus = "idle" | "open" | "error";

// The identity the store validates every patch against. A patch is applied
// only when the supplied identity matches the current identity on all three
// fields. reset() bumps the generation so stale frames are rejected.
export interface ActivityIdentity {
  readonly threadId: string;
  readonly ref: string;
  readonly generation: number;
}

// The minimal service surface the store depends on. Structurally compatible
// with ActivityService so tests can inject a scripted stub.
export interface ActivityServiceLike {
  projectActivity(thread: Thread): ActivityView;
}

export interface ActivityState {
  readonly view: ActivityView | null;
  readonly status: ActivityStatus;
  readonly error: string | null;

  project(service: ActivityServiceLike, thread: Thread): void;
  // Install a pre-projected view and (optionally) the identity atomically.
  // When identity is omitted the store reuses its last-known identity, so
  // the conversation store's read-boundary path (which supplies only a view)
  // remains compatible.
  setView(view: ActivityView, identity?: ActivityIdentity): void;
  // Patch the view from a notification. Returns "applied" when the view was
  // mutated, "rehydrate" when the caller should reread (jobs-tree, missing
  // parent, turn completion lacking authoritative usage), or "ignored" when
  // the identity did not match or there was no view to patch.
  applyNotification(
    n: AnyNotification,
    identity?: ActivityIdentity,
  ): "applied" | "rehydrate" | "ignored";
  reset(): void;

  /** @internal Generation counter for deterministic tests. */
  generationForTest(): number;
}

// Classify a wire status string into a display tone for live views.
function classifyTone(
  status: string,
  terminal: boolean | undefined,
  exitCode: number | undefined,
  outcome: string | undefined,
): WorkTone {
  if (exitCode !== undefined && exitCode !== 0) return "failed";
  if (terminal && outcome !== undefined && FAILED_OUTCOMES.has(outcome)) {
    return "failed";
  }
  if (FAILED_STATUSES.has(status)) return "failed";
  if (RUNNING_STATUSES.has(status)) return "running";
  if (IDLE_STATUSES.has(status)) return "idle";
  if (terminal || TERMINAL_STATUSES.has(status)) return "terminal";
  return "unknown";
}

const RUNNING_STATUSES = new Set([
  "running",
  "in_progress",
  "inProgress",
  "active",
]);
const IDLE_STATUSES = new Set(["idle", "waiting", "paused"]);
const TERMINAL_STATUSES = new Set([
  "completed",
  "done",
  "finished",
  "succeeded",
  "success",
]);
const FAILED_STATUSES = new Set([
  "failed",
  "error",
  "errored",
  "cancelled",
  "canceled",
  "exhausted",
  "stopped",
]);
const FAILED_OUTCOMES = new Set([
  "failed",
  "error",
  "errored",
  "cancelled",
  "canceled",
  "exhausted",
  "stopped",
]);

function formatOutputBytes(bytes: number): string {
  if (bytes === 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) {
    const kb = bytes / 1024;
    return `${kb < 10 ? kb.toFixed(1) : Math.round(kb)} KB`;
  }
  const mb = bytes / (1024 * 1024);
  return `${mb < 10 ? mb.toFixed(1) : Math.round(mb)} MB`;
}

// Project a wire EvenerJobInfo into a sanitized WorkEntry. The label uses
// jobType (the operation name), never the task/prompt/command text.
function projectJobEntry(job: {
  jobId: string;
  jobType: string;
  status: string;
  outputBytes: number;
  exitCode?: number;
  fromWatch?: boolean;
}): WorkEntry {
  const tone = classifyTone(job.status, undefined, job.exitCode, undefined);
  const kind: WorkKind = job.fromWatch === true ? "watch" : "job";
  return {
    kind,
    label: job.jobType,
    tone,
    outputSummary: formatOutputBytes(job.outputBytes),
    diagnostics: {
      rawId: job.jobId,
      operationName: job.jobType,
      statusClass: job.status,
      outputBytes: job.outputBytes,
      exitCode: job.exitCode,
    } as RedactedDiagnostic,
  };
}

// Project a wire EvenerDelegateInfo into a sanitized WorkEntry. The label
// uses the delegate type (the operation name) only — never description, task
// prompt, transcriptRef, or profile ID — to prevent leaking delegated task
// text or operational identifiers into the visible live view.
function projectDelegateEntry(dlg: {
  delegateId: string;
  type: string;
  status: string;
  terminal?: boolean;
  outcome?: string;
  resumable: boolean;
  durationMs?: number;
  resolvedProfileId?: string;
  runStartedAt?: string;
  runEndedAt?: string;
}): WorkEntry {
  const tone = classifyTone(dlg.status, dlg.terminal, undefined, dlg.outcome);
  const label = dlg.type && dlg.type.length > 0 ? dlg.type : "Delegate";
  return {
    kind: "delegate",
    label,
    tone,
    durationMs: dlg.durationMs,
    diagnostics: {
      rawId: dlg.delegateId,
      operationName: dlg.type,
      statusClass: dlg.status,
      startedAt: dlg.runStartedAt,
      endedAt: dlg.runEndedAt,
      durationMs: dlg.durationMs,
      profileId:
        dlg.resolvedProfileId !== undefined ? ("redacted" as const) : undefined,
    } as RedactedDiagnostic,
  };
}

// Find a work entry by rawId in the work tree, searching recursively.
function findEntryById(
  work: WorkEntry[],
  rawId: string,
): { entry: WorkEntry; index: number; parent: WorkEntry[] } | null {
  for (let i = 0; i < work.length; i++) {
    const entry = work[i];
    if (entry === undefined) continue;
    if (entry.diagnostics?.rawId === rawId) {
      return { entry, index: i, parent: work };
    }
    if (entry.children) {
      const found = findEntryById(entry.children, rawId);
      if (found) return found;
    }
  }
  return null;
}

// Replace a work entry at a specific position, returning a new array.
function replaceEntry(
  work: WorkEntry[],
  index: number,
  newEntry: WorkEntry,
): WorkEntry[] {
  return work.map((w, i) => (i === index ? newEntry : w));
}

// Helper: replace a child array within a parent entry by reference.
function replaceChildEntry(
  parent: WorkEntry,
  oldChildren: WorkEntry[],
  newChildren: WorkEntry[],
): WorkEntry {
  if (parent.children === oldChildren) {
    return { ...parent, children: newChildren };
  }
  if (parent.children) {
    return {
      ...parent,
      children: parent.children.map((c) =>
        replaceChildEntry(c, oldChildren, newChildren),
      ),
    };
  }
  return parent;
}

// Replace an entry that may be nested anywhere in the tree, rebuilding the
// full work array immutably.
function replaceInTree(
  work: WorkEntry[],
  found: { entry: WorkEntry; index: number; parent: WorkEntry[] },
  newEntry: WorkEntry,
): WorkEntry[] {
  const newParent = replaceEntry(found.parent, found.index, newEntry);
  if (found.parent === work) return newParent;
  return work.map((w) => replaceChildEntry(w, found.parent, newParent));
}

export function createActivityStore() {
  let generation = 0;
  let identity: ActivityIdentity | null = null;
  // When setView is called without an identity (the conversation store's
  // read-boundary path, which cannot be edited in this reslice), the store
  // enters an "unbound" mode: subsequent applyNotification calls without an
  // identity are accepted, because the conversation store has already
  // validated threadId/ref against the conversation. Supplying an explicit
  // identity exits unbound mode and enables strict per-patch validation.
  let unbound = false;

  // Compare a candidate identity with the current identity. Returns true
  // when all three fields match.
  function matchesCurrent(id: ActivityIdentity | null): boolean {
    if (identity === null) return false;
    if (id === null || id === undefined) return false;
    return (
      id.threadId === identity.threadId &&
      id.ref === identity.ref &&
      id.generation === identity.generation
    );
  }

  return create<ActivityState>((set, get) => ({
    view: null,
    status: "idle",
    error: null,

    project(service, thread) {
      // Bump the generation on every projection so a subsequent reset or
      // re-open of a different thread invalidates earlier frames. The
      // identity is established from the thread.
      generation += 1;
      identity = {
        threadId: thread.id,
        ref: thread.evener.ref,
        generation,
      };
      unbound = false;
      try {
        const view = service.projectActivity(thread);
        set({ view, status: "open", error: null });
      } catch (err) {
        set({
          view: null,
          status: "error",
          error: err instanceof Error ? err.message : String(err),
        });
      }
    },

    setView(view, id) {
      if (id !== undefined) {
        identity = id;
        unbound = false;
        if (id.generation > generation) generation = id.generation;
      } else if (identity === null) {
        // No identity established yet and none supplied — enter unbound mode
        // so the conversation store's notification routing still patches.
        unbound = true;
      }
      set({ view, status: "open", error: null });
    },

    applyNotification(n, id) {
      const state = get();
      if (state.view === null) return "ignored";
      const view = state.view;

      // Identity resolution: when the caller supplies an identity, validate
      // strictly. When omitted, accept in unbound mode (the conversation
      // store has already checked threadId/ref). When omitted and bound,
      // derive from notification params + current generation.
      if (id !== undefined) {
        if (!matchesCurrent(id)) return "ignored";
      } else if (!unbound) {
        if (identity === null) return "ignored";
        const params = n.params as Record<string, unknown> | undefined;
        if (params !== undefined && params !== null) {
          const nThreadId =
            typeof params.threadId === "string" ? params.threadId : undefined;
          const nRef = typeof params.ref === "string" ? params.ref : undefined;
          if (
            (nThreadId !== undefined && nThreadId !== identity.threadId) ||
            (nRef !== undefined && nRef !== identity.ref)
          ) {
            return "ignored";
          }
        }
      }

      switch (n.method) {
        case "evener/job/started":
        case "evener/job/finished": {
          const params = n.params as {
            job: {
              jobId: string;
              jobType: string;
              status: string;
              outputBytes: number;
              exitCode?: number;
              fromWatch?: boolean;
              parentDelegateId?: string;
            };
          };
          const entry = projectJobEntry(params.job);
          const found = findEntryById(view.work, params.job.jobId);
          if (found) {
            const newWork = replaceInTree(view.work, found, entry);
            set({ view: { ...view, work: newWork } });
            return "applied";
          }
          // Not found — check for a parent delegate to nest under.
          const parentDelegateId = params.job.parentDelegateId;
          if (parentDelegateId) {
            const delegateFound = findEntryById(view.work, parentDelegateId);
            if (delegateFound) {
              const newChildren = [
                ...(delegateFound.entry.children ?? []),
                entry,
              ];
              const newDelegate = {
                ...delegateFound.entry,
                children: newChildren,
              };
              const newWork = replaceInTree(
                view.work,
                delegateFound,
                newDelegate,
              );
              set({ view: { ...view, work: newWork } });
              return "applied";
            }
            // Parent delegate not found — do NOT flatten to top-level.
            // Request a rehydrate so the authoritative tree is re-read.
            return "rehydrate";
          }
          // No parent — append at top level.
          set({ view: { ...view, work: [...view.work, entry] } });
          return "applied";
        }

        case "evener/delegate/updated": {
          const params = n.params as {
            delegate: {
              delegateId: string;
              type: string;
              status: string;
              terminal?: boolean;
              outcome?: string;
              resumable: boolean;
              durationMs?: number;
              resolvedProfileId?: string;
              runStartedAt?: string;
              runEndedAt?: string;
            };
          };
          const entry = projectDelegateEntry(params.delegate);
          const found = findEntryById(view.work, params.delegate.delegateId);
          if (found) {
            // Preserve existing children when replacing a delegate.
            const newEntry = {
              ...entry,
              children: found.entry.children,
            };
            const newWork = replaceInTree(view.work, found, newEntry);
            set({ view: { ...view, work: newWork } });
            return "applied";
          }
          // New delegate — append at top level.
          set({ view: { ...view, work: [...view.work, entry] } });
          return "applied";
        }

        case "evener/task/updated": {
          const params = n.params as { total: number; done: number };
          const open = Math.max(0, params.total - params.done);
          set({
            view: {
              ...view,
              tasks: [
                { status: "active" as const, count: 0 },
                { status: "open" as const, count: open },
                { status: "done" as const, count: params.done },
              ],
            },
          });
          return "applied";
        }

        case "turn/completed": {
          const params = n.params as {
            turn: {
              usage?: {
                totalTokens?: number;
                inputTokens?: number;
                outputTokens?: number;
                cacheReadTokens?: number;
              };
            };
          };
          if (params.turn.usage) {
            set({
              view: {
                ...view,
                usage: { ...view.usage, ...params.turn.usage },
              },
            });
            return "applied";
          }
          // Turn completion lacking authoritative usage — request rehydrate.
          return "rehydrate";
        }

        case "evener/jobs/treeUpdated": {
          // The store owns no coalescer. Signal rehydrate so the caller
          // (conversation reslice A) can schedule the authoritative reread.
          return "rehydrate";
        }

        default:
          return "ignored";
      }
    },

    reset() {
      generation += 1;
      identity = null;
      unbound = false;
      set({ view: null, status: "idle", error: null });
    },

    generationForTest() {
      return generation;
    },
  }));
}

// Re-export for callers that want the default service.
export { defaultService as createDefaultActivityService };
