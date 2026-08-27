// ActivityStore — Zustand state wrapping an ActivityService. Owns the current
// ActivityView projection (never the raw wire Thread).
//
// Two API tiers:
// - Legacy (compiles-only, for the existing conversation store that cannot be
//   edited in this reslice): setView(view) / applyNotification(n) / project() /
//   reset(). These are fail-open compatibility shims that DO NOT enforce
//   identity. They exist solely so state/conversation.ts (outside this
//   reslice's allowlist) keeps compiling. The conversation store's own
//   applyNotification already validates threadId/ref.
// - Live (strict, required): setLiveView(view, identity) and
//   applyLiveNotification(n, identity). These enforce identity + generation
//   on every call. A's sink uses these. No unbound mode.
//
// Identity safety (CRITICAL — live methods):
// - The store tracks an ActivityIdentity { threadId, ref, generation }.
// - setLiveView installs the view and identity atomically and rejects a stale
//   identity whose generation is not strictly newer than the current
//   generation (monotonic). reset() records an invalidated generation so a
//   late setLiveView carrying the old identity cannot revive the view.
// - applyLiveNotification validates BOTH the supplied identity against the
//   current identity AND the notification payload's threadId/ref (when present)
//   against the supplied identity. A mismatched payload is ignored even when
//   the supplied identity matches the store.
// - The store owns NO signal-only coalescer. evener/jobs/treeUpdated returns
//   "rehydrate" so the caller (conversation reslice A) owns the one
//   authoritative reread scheduler.
//
// Notification payload labels use safe operation type/kind only — never
// description, task prompt, command, path, profile ID, ref, or transcript ID.
// Notification params are extracted via generated discriminated types, never
// anonymous casts.

import { create } from "zustand";
import type {
  AnyNotification,
  EvenerDelegateInfo,
  EvenerJobInfo,
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

// The identity the store validates every live patch against. A live patch is
// applied only when the supplied identity matches the current identity on all
// three fields AND the notification payload's threadId/ref (when present)
// match the supplied identity. reset() records an invalidated generation so
// late setLiveView calls cannot revive the view.
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

// Outcome of a live notification patch.
export type NotificationOutcome = "applied" | "rehydrate" | "ignored";

export interface ActivityState {
  readonly view: ActivityView | null;
  readonly status: ActivityStatus;
  readonly error: string | null;

  // Legacy compile-only path (conversation store). Not identity-enforcing.
  project(service: ActivityServiceLike, thread: Thread): void;
  setView(view: ActivityView): void;
  applyNotification(n: AnyNotification): void;

  // Strict live path (A's sink). Identity-enforcing, no unbound mode.
  setLiveView(view: ActivityView, identity: ActivityIdentity): boolean;
  applyLiveNotification(
    n: AnyNotification,
    identity: ActivityIdentity,
  ): NotificationOutcome;

  reset(): void;

  /** @internal Generation counter for deterministic tests. */
  generationForTest(): number;
}

// --- generated typed notification param extraction --------------------------
// Extract the params type for a given notification method from the generated
// AnyNotification discriminated union. This replaces anonymous casts so the
// compiler enforces the real payload shape (including required threadId/ref
// and delegate parentDelegateId).
type NotificationOf<M extends AnyNotification["method"]> = Extract<
  AnyNotification,
  { method: M }
>;
type ParamsOf<M extends AnyNotification["method"]> =
  NotificationOf<M>["params"];

// --- tone classification -----------------------------------------------------

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
function projectJobEntry(job: EvenerJobInfo): WorkEntry {
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
// prompt, transcriptRef, or profile ID.
function projectDelegateEntry(dlg: EvenerDelegateInfo): WorkEntry {
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

// Find the parent delegate rawId of an entry by rawId, or "" if top-level.
function delegateParentOf(work: WorkEntry[], targetId: string): string {
  for (const w of work) {
    if (w.diagnostics?.rawId === targetId) return "";
    if (w.children) {
      if (w.children.some((c) => c.diagnostics?.rawId === targetId)) {
        return w.diagnostics?.rawId ?? "";
      }
      const found = delegateParentOf(w.children, targetId);
      if (found !== "") return found;
    }
  }
  return "";
}

// Check whether a usage object is authoritatively complete. Conservative:
// requires totalTokens present and positive. The generated EvenerUsage fields
// are all optional, so {} or a single-field partial is insufficient.
function usageIsComplete(
  usage: { totalTokens?: number } | undefined,
): usage is { totalTokens: number } {
  return (
    usage !== undefined &&
    typeof usage.totalTokens === "number" &&
    usage.totalTokens > 0
  );
}

// Core live notification patch. Returns the outcome. Caller has already
// validated identity and payload. Uses `set` to install the mutated view.
type Setter = (partial: Partial<ActivityState>) => void;

function patchLive(
  n: AnyNotification,
  view: ActivityView,
  set: Setter,
): NotificationOutcome {
  switch (n.method) {
    case "evener/job/started":
    case "evener/job/finished": {
      const params = n.params as ParamsOf<"evener/job/started">;
      const j = params.job;
      const entry = projectJobEntry(j);
      const found = findEntryById(view.work, j.jobId);
      if (found) {
        // Collision: if the existing entry is a different kind, rehydrate
        // rather than patching the wrong kind.
        if (found.entry.kind !== entry.kind) return "rehydrate";
        const newWork = replaceInTree(view.work, found, entry);
        set({ view: { ...view, work: newWork } });
        return "applied";
      }
      // New job — check for a parent delegate to nest under.
      const parentDelegateId = j.parentDelegateId;
      if (parentDelegateId) {
        const delegateFound = findEntryById(view.work, parentDelegateId);
        if (delegateFound) {
          // Parent must be a delegate; otherwise rehydrate.
          if (delegateFound.entry.kind !== "delegate") return "rehydrate";
          const newChildren = [...(delegateFound.entry.children ?? []), entry];
          const newDelegate: WorkEntry = {
            ...delegateFound.entry,
            children: newChildren,
          };
          const newWork = replaceInTree(view.work, delegateFound, newDelegate);
          set({ view: { ...view, work: newWork } });
          return "applied";
        }
        // Parent delegate not found — do NOT flatten to top-level.
        return "rehydrate";
      }
      // No parent — append at top level.
      set({ view: { ...view, work: [...view.work, entry] } });
      return "applied";
    }

    case "evener/delegate/updated": {
      const params = n.params as ParamsOf<"evener/delegate/updated">;
      const dlg = params.delegate;
      const entry = projectDelegateEntry(dlg);
      const found = findEntryById(view.work, dlg.delegateId);
      if (found) {
        // Collision: existing entry must be a delegate; else rehydrate.
        if (found.entry.kind !== "delegate") return "rehydrate";
        // If the delegate's parent changed (relocation), rehydrate rather
        // than patching in the old branch.
        const oldParent = delegateParentOf(view.work, dlg.delegateId);
        const newParent = dlg.parentDelegateId ?? "";
        if (oldParent !== newParent) return "rehydrate";
        // Preserve existing children when replacing.
        const newEntry: WorkEntry = {
          ...entry,
          children: found.entry.children,
        };
        const newWork = replaceInTree(view.work, found, newEntry);
        set({ view: { ...view, work: newWork } });
        return "applied";
      }
      // New delegate — if it has a parent, it must nest under that parent
      // (which must already be present); else rehydrate.
      const parent = dlg.parentDelegateId;
      if (parent && parent !== "") {
        const delegateFound = findEntryById(view.work, parent);
        if (delegateFound) {
          if (delegateFound.entry.kind !== "delegate") return "rehydrate";
          const newChildren = [...(delegateFound.entry.children ?? []), entry];
          const newDelegate: WorkEntry = {
            ...delegateFound.entry,
            children: newChildren,
          };
          const newWork = replaceInTree(view.work, delegateFound, newDelegate);
          set({ view: { ...view, work: newWork } });
          return "applied";
        }
        return "rehydrate";
      }
      // No parent — append at top level.
      set({ view: { ...view, work: [...view.work, entry] } });
      return "applied";
    }

    case "evener/task/updated": {
      const params = n.params as ParamsOf<"evener/task/updated">;
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
      const params = n.params as ParamsOf<"turn/completed">;
      if (usageIsComplete(params.turn.usage)) {
        set({
          view: {
            ...view,
            usage: { ...view.usage, ...params.turn.usage },
          },
        });
        return "applied";
      }
      // Insufficient usage — request rehydrate.
      return "rehydrate";
    }

    case "evener/jobs/treeUpdated": {
      return "rehydrate";
    }

    default:
      return "ignored";
  }
}

export function createActivityStore() {
  let generation = 0;
  let identity: ActivityIdentity | null = null;
  // The generation at which reset() was last called. A late setLiveView
  // carrying a generation <= this value is rejected so it cannot revive a
  // cleared view.
  let invalidatedAt = 0;

  // Compare a candidate identity with the current identity. Returns true
  // when all three fields match.
  function matchesCurrent(id: ActivityIdentity): boolean {
    if (identity === null) return false;
    return (
      id.threadId === identity.threadId &&
      id.ref === identity.ref &&
      id.generation === identity.generation
    );
  }

  // Validate the notification payload's threadId/ref (when present) against a
  // supplied identity. Returns false on mismatch.
  function payloadMatches(n: AnyNotification, id: ActivityIdentity): boolean {
    const params = n.params as Record<string, unknown> | undefined;
    if (params === undefined || params === null) return true;
    const nThreadId =
      typeof params.threadId === "string" ? params.threadId : undefined;
    const nRef = typeof params.ref === "string" ? params.ref : undefined;
    if (nThreadId !== undefined && nThreadId !== id.threadId) return false;
    if (nRef !== undefined && nRef !== id.ref) return false;
    return true;
  }

  return create<ActivityState>((set, get) => ({
    view: null,
    status: "idle",
    error: null,

    // --- legacy compile-only path (conversation store) ----------------------
    project(service, thread) {
      // Legacy path: bump generation and establish identity from the thread.
      generation += 1;
      identity = {
        threadId: thread.id,
        ref: thread.evener.ref,
        generation,
      };
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

    setView(view) {
      // Legacy: no identity enforcement. The conversation store validates
      // threadId/ref itself.
      set({ view, status: "open", error: null });
    },

    applyNotification(n) {
      // Legacy: no identity enforcement, no return value. Delegate to the
      // core patcher with the current view (fail-closed: if no view, no-op).
      const state = get();
      if (state.view === null) return;
      patchLive(n, state.view, set);
    },

    // --- strict live path (A's sink) ----------------------------------------
    setLiveView(view, id) {
      // Monotonic: reject a stale identity whose generation is not strictly
      // newer than the current generation, and reject any identity whose
      // generation was invalidated by reset().
      if (id.generation <= invalidatedAt) return false;
      if (identity !== null && id.generation <= identity.generation) {
        // Same generation is allowed only on the very first install (identity
        // is null). Otherwise reject stale/equal.
        return false;
      }
      generation = id.generation;
      identity = id;
      set({ view, status: "open", error: null });
      return true;
    },

    applyLiveNotification(n, id) {
      const state = get();
      if (state.view === null) return "ignored";
      // Validate supplied identity against the store.
      if (!matchesCurrent(id)) return "ignored";
      // Validate payload threadId/ref (when present) against supplied identity.
      if (!payloadMatches(n, id)) return "ignored";
      return patchLive(n, state.view, set);
    },

    reset() {
      invalidatedAt = generation;
      generation += 1;
      identity = null;
      set({ view: null, status: "idle", error: null });
    },

    generationForTest() {
      return generation;
    },
  }));
}

// Re-export for callers that want the default service.
export { defaultService as createDefaultActivityService };
