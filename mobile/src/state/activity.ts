// ActivityStore — Zustand state wrapping an ActivityService. Owns the current
// ActivityView projection (never the raw wire Thread) and exposes project()
// (re-project from a Thread), setView() (adopt a pre-projected view from the
// read boundary), applyNotification() (patch from activity notifications),
// and reset() (clear on conversation switch).
//
// Generation safety (CRITICAL): the store tracks both the thread identity
// (threadId + ref) and a generation counter. Notifications that don't match
// the current thread identity are rejected. reset() bumps the generation so
// a late completion from the older conversation is dropped. setView() records
// the thread identity so subsequent notifications can be validated.

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

// The minimal service surface the store depends on. Structurally compatible
// with ActivityService so tests can inject a scripted stub.
export interface ActivityServiceLike {
  projectActivity(thread: Thread): ActivityView;
}

// An injected coalescer for jobs-tree rehydrate requests (mirrors the
// conversation store's RehydrateCoalescer).
export interface ActivityRehydrateCoalescer {
  requestRehydrate(ref: string): void;
}

export interface ActivityState {
  readonly view: ActivityView | null;
  readonly status: ActivityStatus;
  readonly error: string | null;

  project(service: ActivityServiceLike, thread: Thread): void;
  setView(view: ActivityView): void;
  // Set the thread identity for notification validation. Called by the
  // conversation store's openProjected to record which thread this activity
  // view belongs to.
  setThreadIdentity(threadId: string, ref: string): void;
  applyNotification(n: AnyNotification): void;
  setCoalescer(coalescer: ActivityRehydrateCoalescer): void;
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
// jobType (the operation name), never the task/prompt text.
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
// uses the delegate type (not description/task/prompt), to prevent leaking
// delegated task text into the live view.
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
  // Use the type (operation name), NOT description or task — those may carry
  // the delegated prompt text.
  const label = dlg.type ?? "Delegate";
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

export function createActivityStore() {
  let generation = 0;
  let lastThreadKey: string | null = null;
  let threadIdentity: { threadId: string; ref: string } | null = null;
  let coalescer: ActivityRehydrateCoalescer | null = null;

  return create<ActivityState>((set, get) => ({
    view: null,
    status: "idle",
    error: null,

    project(service, thread) {
      // Bump the generation only when the conversation identity changes, so a
      // re-projection of the same thread (after a notification) does not
      // regress or advance the generation — only a switch to a different
      // conversation bumps it.
      const key = `${thread.id}:${thread.evener.ref}`;
      if (key !== lastThreadKey) {
        generation += 1;
        lastThreadKey = key;
        threadIdentity = { threadId: thread.id, ref: thread.evener.ref };
      }
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
      set({ view, status: "open", error: null });
    },

    setThreadIdentity(threadId, ref) {
      threadIdentity = { threadId, ref };
      lastThreadKey = `${threadId}:${ref}`;
    },

    applyNotification(n) {
      const state = get();
      if (state.view === null) return;
      const view = state.view;

      // Generation/identity safety: reject notifications that don't match
      // the current thread identity.
      if (threadIdentity !== null) {
        const params = n.params as Record<string, unknown> | undefined;
        if (params !== undefined && params !== null) {
          const nThreadId =
            typeof params.threadId === "string" ? params.threadId : undefined;
          const nRef = typeof params.ref === "string" ? params.ref : undefined;
          if (
            (nThreadId !== undefined &&
              nThreadId !== threadIdentity.threadId) ||
            (nRef !== undefined && nRef !== threadIdentity.ref)
          ) {
            return;
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
          // Search recursively for the job — it may be nested under a delegate.
          const found = findEntryById(view.work, params.job.jobId);
          if (found) {
            const newWork = replaceEntry(found.parent, found.index, entry);
            // If the found entry was nested, we need to update the parent's
            // children. Since we used replaceEntry on the parent array, we
            // need to rebuild the full work array.
            if (found.parent !== view.work) {
              // The entry was nested — rebuild by finding and replacing the
              // top-level parent that contains it.
              const newWorkTree = view.work.map((w) => {
                if (w.children === found.parent) {
                  return { ...w, children: newWork };
                }
                // Deep search for the parent
                return replaceChildEntry(w, found.parent, newWork);
              });
              set({ view: { ...view, work: newWorkTree } });
            } else {
              set({ view: { ...view, work: newWork } });
            }
          } else {
            // Check if this job has a parent delegate — nest it.
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
                const newWork = replaceEntry(
                  delegateFound.parent,
                  delegateFound.index,
                  newDelegate,
                );
                if (delegateFound.parent !== view.work) {
                  const newWorkTree = view.work.map((w) => {
                    if (w.children === delegateFound.parent) {
                      return { ...w, children: newWork };
                    }
                    return replaceChildEntry(w, delegateFound.parent, newWork);
                  });
                  set({ view: { ...view, work: newWorkTree } });
                } else {
                  set({ view: { ...view, work: newWork } });
                }
              } else {
                // Parent delegate not found — append at top level.
                set({ view: { ...view, work: [...view.work, entry] } });
              }
            } else {
              set({ view: { ...view, work: [...view.work, entry] } });
            }
          }
          break;
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
            const newWork = replaceEntry(found.parent, found.index, newEntry);
            if (found.parent !== view.work) {
              const newWorkTree = view.work.map((w) => {
                if (w.children === found.parent) {
                  return { ...w, children: newWork };
                }
                return replaceChildEntry(w, found.parent, newWork);
              });
              set({ view: { ...view, work: newWorkTree } });
            } else {
              set({ view: { ...view, work: newWork } });
            }
          } else {
            set({ view: { ...view, work: [...view.work, entry] } });
          }
          break;
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
          break;
        }

        case "turn/completed": {
          const params = n.params as {
            turn: {
              usage?: {
                totalTokens?: number;
                inputTokens?: number;
                outputTokens?: number;
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
          }
          break;
        }

        case "evener/jobs/treeUpdated": {
          // Coalesce to one rehydrate via the injected coalescer.
          if (coalescer !== null) {
            const ref = (n.params as { ref?: string }).ref;
            if (ref !== undefined) {
              coalescer.requestRehydrate(ref);
            }
          }
          break;
        }

        default:
          break;
      }
    },

    setCoalescer(c) {
      coalescer = c;
    },

    reset() {
      generation += 1;
      lastThreadKey = null;
      threadIdentity = null;
      set({ view: null, status: "idle", error: null });
    },

    generationForTest() {
      return generation;
    },
  }));
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

// Re-export for callers that want the default service.
export { defaultService as createDefaultActivityService };
