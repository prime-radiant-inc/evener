// ActivityStore — Zustand state wrapping an ActivityService. Owns the current
// ActivityView projection (never the raw wire Thread) and exposes project()
// (re-project from a Thread), setView() (adopt a pre-projected view from the
// read boundary), applyNotification() (patch from activity notifications),
// and reset() (clear on conversation switch).
//
// Generation safety: the generation counter increments only when the
// conversation identity changes (thread id / ref), not on every re-projection
// of the same conversation. This lets a caller re-project the same thread
// (e.g. after a notification) without "regressing" the generation, while a
// switch to a different conversation bumps the generation so a late
// completion from the older conversation is dropped. reset() bumps the
// generation and returns to idle.

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

// Project a wire EvenerJobInfo into a sanitized WorkEntry.
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

// Project a wire EvenerDelegateInfo into a sanitized WorkEntry.
function projectDelegateEntry(dlg: {
  delegateId: string;
  type: string;
  status: string;
  terminal?: boolean;
  outcome?: string;
  resumable: boolean;
  description?: string;
  durationMs?: number;
  resolvedProfileId?: string;
  runStartedAt?: string;
  runEndedAt?: string;
}): WorkEntry {
  const tone = classifyTone(dlg.status, dlg.terminal, undefined, dlg.outcome);
  const label = dlg.description ?? dlg.type ?? "Delegate";
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

export function createActivityStore() {
  let generation = 0;
  let lastThreadKey: string | null = null;
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

    applyNotification(n) {
      const state = get();
      if (state.view === null) return;
      const view = state.view;

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
            };
          };
          const entry = projectJobEntry(params.job);
          // Replace if same jobId exists, otherwise append.
          const existingIdx = view.work.findIndex(
            (w) => w.diagnostics?.rawId === params.job.jobId,
          );
          const newWork =
            existingIdx >= 0
              ? view.work.map((w, i) => (i === existingIdx ? entry : w))
              : [...view.work, entry];
          set({ view: { ...view, work: newWork } });
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
              description?: string;
              durationMs?: number;
              resolvedProfileId?: string;
              runStartedAt?: string;
              runEndedAt?: string;
            };
          };
          const entry = projectDelegateEntry(params.delegate);
          const existingIdx = view.work.findIndex(
            (w) => w.diagnostics?.rawId === params.delegate.delegateId,
          );
          const newWork =
            existingIdx >= 0
              ? view.work.map((w, i) => (i === existingIdx ? entry : w))
              : [...view.work, entry];
          set({ view: { ...view, work: newWork } });
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
      set({ view: null, status: "idle", error: null });
    },

    generationForTest() {
      return generation;
    },
  }));
}

// Re-export for callers that want the default service.
export { defaultService as createDefaultActivityService };
