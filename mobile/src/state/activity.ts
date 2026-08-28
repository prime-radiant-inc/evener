// ActivityStore — Zustand state. Owns the current ActivityView projection
// (never the raw wire Thread). Exposes ONLY the strict LiveActivityState
// surface: setLiveView / applyLiveNotification / setLiveCapabilities / reset /
// generationForTest. No identity-free fail-open API — no setView,
// applyNotification, or project.
// No identity-free fail-open API — no setView, applyNotification, or project.
//
// Identity safety (CRITICAL — live methods):
// - The store tracks an ActivityIdentity { threadId, ref, generation }.
// - setLiveView installs the view and identity atomically. The same exact
//   current identity {threadId, ref, generation} may replace the view during an
//   authoritative reread. It rejects older generations, the invalidated
//   generation after reset, a wrong thread/ref at the same generation, and a
//   late old view. A new greater generation is accepted.
// - applyLiveNotification validates BOTH the supplied identity against the
//   current identity AND the notification payload's threadId/ref (when present)
//   against the supplied identity. A mismatched payload is ignored even when
//   the supplied identity matches the store.
// - Job/delegate relocation: the store compares the reported parent to the
//   actual tree parent and returns "rehydrate" on a mismatch. Duplicate
//   same-kind IDs anywhere, cross-kind collisions, ambiguous multiple matches,
//   and a unique target beneath a duplicated/ambiguous parent ID all return
//   "rehydrate" — the store never patches the first match silently.
// - turn/completed carries per-turn usage, NOT the cumulative Thread.evener
//   usage aggregate. The current protocol provides no authoritative
//   cumulative projection in that notification, so the store returns
//   "rehydrate" and lets the caller perform the one authoritative reread. It
//   never overwrites the activity usage aggregate with per-turn values.
// - The store owns NO signal-only coalescer. evener/jobs/treeUpdated and
//   turn/completed return "rehydrate" so the caller (conversation reslice A)
//   owns the one authoritative reread scheduler.
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
  ThreadCapabilities,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  ActivityView,
  RedactedDiagnostic,
  WorkEntry,
  WorkKind,
  WorkTone,
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

// Outcome of a live notification patch.
export type NotificationOutcome = "applied" | "rehydrate" | "ignored";

// Strict live state: the ONLY surface createActivityStore exposes. No
// unbound mode — setLiveView and applyLiveNotification always carry an
// ActivityIdentity. setLiveCapabilities always carries an ActivityIdentity.
// No identity-free fail-open methods exist.
export interface LiveActivityState {
  readonly view: ActivityView | null;
  readonly status: ActivityStatus;
  readonly error: string | null;

  setLiveView(view: ActivityView, identity: ActivityIdentity): boolean;
  applyLiveNotification(
    n: AnyNotification,
    identity: ActivityIdentity,
  ): NotificationOutcome;

  // Narrow strict sink for a capabilities-only refresh. Updates only
  // view.capabilities for the exact current identity and an open view; returns
  // false on stale/missing/wrong identity and preserves tasks/work/usage/
  // reasoning. This is the seam the conversation cap-refresh writer calls
  // independently of the notification stream.
  setLiveCapabilities(
    capabilities: ThreadCapabilities,
    identity: ActivityIdentity,
  ): boolean;

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

// Count how many work entries share a rawId anywhere in the tree. Used to
// detect duplicate same-kind IDs: when more than one entry carries the same
// rawId, the target is ambiguous and the store must rehydrate rather than
// patch the first match silently. Also used to detect a unique target
// beneath a duplicated/ambiguous parent ID.
function countEntriesById(work: WorkEntry[], rawId: string): number {
  let count = 0;
  for (const entry of work) {
    if (entry === undefined) continue;
    if (entry.diagnostics?.rawId === rawId) count += 1;
    if (entry.children) count += countEntriesById(entry.children, rawId);
  }
  return count;
}

// Find a work entry by rawId in the work tree, searching recursively. Returns
// null when no entry matches. Callers must separately check countEntriesById
// before patching: a count > 1 means the ID is ambiguous (duplicate same-kind)
// and the store must rehydrate rather than patch the first match silently.
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

// Core live notification patch. Returns the outcome. Caller has already
// validated identity and payload. Uses `set` to install the mutated view.
type Setter = (partial: Partial<LiveActivityState>) => void;

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
      // Detect duplicate same-kind IDs anywhere in the tree: when more than
      // one entry carries this rawId, the target is ambiguous — rehydrate
      // rather than patch the first match silently.
      if (countEntriesById(view.work, j.jobId) > 1) return "rehydrate";
      const found = findEntryById(view.work, j.jobId);
      if (found) {
        // Cross-kind collision: if the existing entry is a different kind,
        // rehydrate rather than patching the wrong kind.
        if (found.entry.kind !== entry.kind) return "rehydrate";
        // Job relocation: compare the reported parent to the actual tree
        // parent. If the job moved (top-level↔nested, or between delegates),
        // rehydrate rather than patching in the old branch.
        const oldParent = delegateParentOf(view.work, j.jobId);
        const newParent = j.parentDelegateId ?? "";
        if (oldParent !== newParent) return "rehydrate";
        // I3: a unique target beneath a duplicated/ambiguous parent ID must
        // rehydrate — the store cannot know which parent instance owns it.
        if (oldParent !== "" && countEntriesById(view.work, oldParent) > 1) {
          return "rehydrate";
        }
        const newWork = replaceInTree(view.work, found, entry);
        set({ view: { ...view, work: newWork } });
        return "applied";
      }
      // New job — check for a parent delegate to nest under.
      const parentDelegateId = j.parentDelegateId;
      if (parentDelegateId) {
        // The parent must be unambiguous; a duplicate parent ID is ambiguous.
        if (countEntriesById(view.work, parentDelegateId) > 1)
          return "rehydrate";
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
      // Detect duplicate same-kind IDs: ambiguous delegate ID → rehydrate.
      if (countEntriesById(view.work, dlg.delegateId) > 1) return "rehydrate";
      const found = findEntryById(view.work, dlg.delegateId);
      if (found) {
        // Cross-kind collision: existing entry must be a delegate; else
        // rehydrate rather than patching the wrong kind.
        if (found.entry.kind !== "delegate") return "rehydrate";
        // Delegate relocation: if the parent changed, rehydrate rather than
        // patching in the old branch.
        const oldParent = delegateParentOf(view.work, dlg.delegateId);
        const newParent = dlg.parentDelegateId ?? "";
        if (oldParent !== newParent) return "rehydrate";
        // I3: a unique target beneath a duplicated/ambiguous parent ID must
        // rehydrate.
        if (oldParent !== "" && countEntriesById(view.work, oldParent) > 1) {
          return "rehydrate";
        }
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
        if (countEntriesById(view.work, parent) > 1) return "rehydrate";
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
      // turn/completed carries per-turn usage (Turn.usage), NOT the cumulative
      // Thread.evener.usage aggregate. The current protocol provides no
      // authoritative cumulative projection in this notification, so the
      // store must NOT overwrite the activity usage aggregate with per-turn
      // values. Return "rehydrate" so the caller performs the one
      // authoritative reread.
      return "rehydrate";
    }

    case "evener/jobs/treeUpdated": {
      return "rehydrate";
    }

    // --- I2: control-copy notifications ------------------------------------
    // These patch ONLY the named control fields on the shared ActivityView;
    // tasks/work/usage are preserved. Identity/ref validation already happened
    // in applyLiveNotification before patchLive is reached, so a wrong
    // identity/ref never reaches here. Never infer anything from status or
    // model labels — only the explicitly supplied fields are applied.

    case "thread/status/changed": {
      const params = n.params as ParamsOf<"thread/status/changed">;
      // Replace capabilities ONLY when supplied; preserve otherwise. The
      // status payload's type/activeFlags carry no ActivityView state.
      if (params.capabilities === undefined) return "applied";
      set({ view: { ...view, capabilities: { ...params.capabilities } } });
      return "applied";
    }

    case "thread/model/changed": {
      const params = n.params as ParamsOf<"thread/model/changed">;
      // Set reasoningEffortLevels + supportsReasoning exactly, including an
      // explicit undefined (clearing the field). modelProvider/model are not
      // ActivityView fields; never infer reasoning support from the label.
      set({
        view: {
          ...view,
          reasoningEffortLevels: params.reasoningEffortLevels,
          supportsReasoning: params.supportsReasoning,
        },
      });
      return "applied";
    }

    case "thread/reasoning-effort/changed": {
      const params = n.params as ParamsOf<"thread/reasoning-effort/changed">;
      // Set reasoningEffort exactly, including an explicit undefined.
      set({ view: { ...view, reasoningEffort: params.reasoningEffort } });
      return "applied";
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

  return create<LiveActivityState>((set, get) => ({
    view: null,
    status: "idle",
    error: null,

    setLiveView(view, id) {
      // Reject any identity whose generation was invalidated by reset().
      if (id.generation <= invalidatedAt) return false;
      if (identity !== null) {
        if (id.generation < identity.generation) return false;
        if (id.generation === identity.generation) {
          // Same generation: accept only the exact same identity (an
          // authoritative reread replacement). A different thread/ref at the
          // same generation is a stale identity from a different
          // conversation — reject.
          if (id.threadId !== identity.threadId || id.ref !== identity.ref) {
            return false;
          }
        }
        // id.generation > identity.generation is a new conversation (or a
        // newer generation of the same one) — accepted.
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

    setLiveCapabilities(capabilities, id) {
      // Narrow strict sink: update ONLY view.capabilities for the exact current
      // identity and an open view. Return false (without mutation) on
      // stale/missing/wrong identity or a null view. Preserve tasks/work/usage/
      // reasoning.
      if (identity === null) return false;
      if (!matchesCurrent(id)) return false;
      const state = get();
      if (state.view === null) return false;
      set({ view: { ...state.view, capabilities: { ...capabilities } } });
      return true;
    },

    reset() {
      // Idempotent while already reset (identity === null): a repeated/external
      // reset, or a reset before any view was opened, must NOT advance the
      // rejection boundary. Only a real reset (identity !== null) invalidates
      // the current accepted generation and clears the view. This preserves
      // rejection of late old identities while avoiding a phantom boundary
      // advance that would reject a valid newer identity.
      if (identity !== null) {
        invalidatedAt = generation;
        generation += 1;
        identity = null;
      }
      set({ view: null, status: "idle", error: null });
    },

    generationForTest() {
      return generation;
    },
  }));
}
