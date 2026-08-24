// Fixture-driven tests for the activity projection service. projectActivity
// folds a wire Thread's diagnostics/tasks/jobs/usage into ActivityView view
// models the ActivitySheet consumes. Every fixture is a literal Thread wire
// object; no network, no clock, no provider credentials. These exercise task
// grouping (active/open/done), nested delegate/job/watch projection, unknown
// status preservation, running/failed/terminal tones, duration/output
// summaries, token/cost/context values, and redacted diagnostics (metadata
// allowlist only — never URL queries, auth headers, bodies, transcript text,
// filenames, attachment bytes, speech text, or provider payloads).

import { describe, expect, it } from "vitest";
import type {
  EvenerDelegateInfo,
  EvenerDiagnostics,
  EvenerJobInfo,
  EvenerThread,
  EvenerUsage,
  QueueState,
  Thread,
  ThreadCapabilities,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { MobileCapabilities } from "../conversation/model";
import {
  createActivityService,
  type RedactedDiagnostic,
  type WorkEntry,
} from "./activity";

// Safe accessor: with noUncheckedIndexedAccess, array indexing returns T | undefined.
// In tests we always know the array is non-empty from the fixture, so we assert.
function firstWork(view: { readonly work: readonly WorkEntry[] }): WorkEntry {
  const entry = view.work[0];
  if (entry === undefined) throw new Error("expected at least one work entry");
  return entry;
}

// --- fixture helpers ---------------------------------------------------------

const ALL_TRUE_CAPS: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

const EMPTY_QUEUE: QueueState = { revision: 0 };

function evenerThread(over: Partial<EvenerThread> = {}): EvenerThread {
  return {
    ref: "ref-1",
    capabilities: ALL_TRUE_CAPS,
    queue: EMPTY_QUEUE,
    ...over,
  };
}

function thread(over: Partial<Thread> = {}): Thread {
  return {
    id: "thread-1",
    sessionId: "session-1",
    preview: "",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 1_000_000,
    updatedAt: 1_000_000,
    status: { type: "ready" },
    cwd: "/tmp",
    cliVersion: "1.0.0",
    source: "local",
    evener: evenerThread(),
    ...over,
  };
}

function delegate(over: Partial<EvenerDelegateInfo> = {}): EvenerDelegateInfo {
  return {
    delegateId: "dlg-1",
    ownerSessionId: "sess-root",
    rootSessionId: "sess-root",
    childSessionId: "sess-child",
    transcriptRef: "local:abc",
    type: "subagent",
    lifecycle: "running",
    phase: "running",
    status: "running",
    resumable: true,
    projectionRevision: 1,
    needsAttention: false,
    ...over,
  };
}

function job(over: Partial<EvenerJobInfo> = {}): EvenerJobInfo {
  return {
    jobId: "job-1",
    jobType: "shell",
    status: "running",
    outputBytes: 0,
    ...over,
  };
}

function usage(over: Partial<EvenerUsage> = {}): EvenerUsage {
  return {
    inputTokens: 100,
    outputTokens: 50,
    cacheReadTokens: 200,
    totalTokens: 350,
    ...over,
  };
}

// --- tests -------------------------------------------------------------------

describe("ActivityService — projectActivity", () => {
  const service = createActivityService();

  describe("tasks", () => {
    it("groups tasks into active, open, and done", () => {
      const t = thread({
        evener: evenerThread({ tasks: { total: 5, done: 2 } }),
      });
      const view = service.projectActivity(t);
      const byStatus = new Map(view.tasks.map((g) => [g.status, g.count]));
      expect(byStatus.get("done")).toBe(2);
      expect(byStatus.get("open")).toBe(3);
      expect(byStatus.get("active")).toBe(0);
    });

    it("reports zero open when all tasks done", () => {
      const t = thread({
        evener: evenerThread({ tasks: { total: 4, done: 4 } }),
      });
      const view = service.projectActivity(t);
      const byStatus = new Map(view.tasks.map((g) => [g.status, g.count]));
      expect(byStatus.get("done")).toBe(4);
      expect(byStatus.get("open")).toBe(0);
    });

    it("omits task groups when tasks is absent", () => {
      const t = thread({ evener: evenerThread({ tasks: undefined }) });
      const view = service.projectActivity(t);
      expect(view.tasks).toEqual([]);
    });

    it("clamps open to zero when done exceeds total", () => {
      const t = thread({
        evener: evenerThread({ tasks: { total: 2, done: 5 } }),
      });
      const view = service.projectActivity(t);
      const byStatus = new Map(view.tasks.map((g) => [g.status, g.count]));
      expect(byStatus.get("done")).toBe(5);
      expect(byStatus.get("open")).toBe(0);
    });
  });

  describe("work — nested delegates/jobs/watches", () => {
    it("projects top-level delegates and standalone jobs", () => {
      const diag: EvenerDiagnostics = {
        delegates: [delegate({ delegateId: "dlg-1" })],
        jobs: [job({ jobId: "job-1", parentDelegateId: undefined })],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      expect(view.work).toHaveLength(2);
      const kinds = view.work.map((w) => w.kind);
      expect(kinds).toContain("delegate");
      expect(kinds).toContain("job");
    });

    it("nests jobs under their parent delegate", () => {
      const diag: EvenerDiagnostics = {
        delegates: [delegate({ delegateId: "dlg-1" })],
        jobs: [
          job({ jobId: "job-1", parentDelegateId: "dlg-1" }),
          job({ jobId: "job-2", parentDelegateId: "dlg-1" }),
        ],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      const dlg = view.work.find((w) => w.kind === "delegate");
      expect(dlg).toBeDefined();
      expect(dlg?.children).toHaveLength(2);
      expect(dlg?.children?.every((c) => c.kind === "job")).toBe(true);
    });

    it("classifies watch-origin jobs as watch entries", () => {
      const diag: EvenerDiagnostics = {
        jobs: [
          job({ jobId: "job-1", fromWatch: true }),
          job({ jobId: "job-2", fromWatch: false }),
        ],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      const watchEntry = view.work.find((w) => w.kind === "watch");
      const jobEntry = view.work.find((w) => w.kind === "job");
      expect(watchEntry).toBeDefined();
      expect(jobEntry).toBeDefined();
    });

    it("nests watch-origin jobs under their parent delegate", () => {
      const diag: EvenerDiagnostics = {
        delegates: [delegate({ delegateId: "dlg-1" })],
        jobs: [
          job({ jobId: "w1", parentDelegateId: "dlg-1", fromWatch: true }),
        ],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      const dlg = view.work.find((w) => w.kind === "delegate");
      expect(dlg?.children?.[0]?.kind).toBe("watch");
    });

    it("returns empty work when diagnostics is absent", () => {
      const t = thread({ evener: evenerThread({ diagnostics: undefined }) });
      const view = service.projectActivity(t);
      expect(view.work).toEqual([]);
    });
  });

  describe("unknown status preservation", () => {
    it("preserves an unknown job status string in diagnostics", () => {
      const diag: EvenerDiagnostics = {
        jobs: [job({ jobId: "job-1", status: "future-state-v2" })],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      const entry = firstWork(view);
      expect(entry.diagnostics?.statusClass).toBe("future-state-v2");
    });

    it("preserves an unknown delegate status string in diagnostics", () => {
      const diag: EvenerDiagnostics = {
        delegates: [
          delegate({ delegateId: "dlg-1", status: "future-phase-x" }),
        ],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      const entry = firstWork(view);
      expect(entry.diagnostics?.statusClass).toBe("future-phase-x");
    });

    it("assigns unknown tone to unrecognized statuses", () => {
      const diag: EvenerDiagnostics = {
        jobs: [job({ jobId: "job-1", status: "extraterrestrial" })],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      expect(firstWork(view).tone).toBe("unknown");
    });
  });

  describe("running/failed/terminal tones", () => {
    it("marks a running delegate as running tone", () => {
      const diag: EvenerDiagnostics = {
        delegates: [
          delegate({ delegateId: "dlg-1", status: "running", terminal: false }),
        ],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      expect(firstWork(view).tone).toBe("running");
    });

    it("marks a failed job (nonzero exit) as failed tone", () => {
      const diag: EvenerDiagnostics = {
        jobs: [job({ jobId: "job-1", status: "completed", exitCode: 2 })],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      expect(firstWork(view).tone).toBe("failed");
    });

    it("marks a terminal delegate as terminal tone", () => {
      const diag: EvenerDiagnostics = {
        delegates: [
          delegate({
            delegateId: "dlg-1",
            status: "completed",
            terminal: true,
            outcome: "completed",
          }),
        ],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      expect(firstWork(view).tone).toBe("terminal");
    });

    it("marks a terminal delegate with failed outcome as failed tone", () => {
      const diag: EvenerDiagnostics = {
        delegates: [
          delegate({
            delegateId: "dlg-1",
            status: "completed",
            terminal: true,
            outcome: "failed",
          }),
        ],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      expect(firstWork(view).tone).toBe("failed");
    });

    it("marks a running job as running tone", () => {
      const diag: EvenerDiagnostics = {
        jobs: [job({ jobId: "job-1", status: "running" })],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      expect(firstWork(view).tone).toBe("running");
    });

    it("marks an idle delegate as idle tone", () => {
      const diag: EvenerDiagnostics = {
        delegates: [
          delegate({
            delegateId: "dlg-1",
            status: "idle",
            terminal: false,
            resumable: true,
          }),
        ],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      expect(firstWork(view).tone).toBe("idle");
    });
  });

  describe("duration and output summary", () => {
    it("surfaces delegate durationMs in the work entry", () => {
      const diag: EvenerDiagnostics = {
        delegates: [delegate({ delegateId: "dlg-1", durationMs: 45_000 })],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      expect(firstWork(view).durationMs).toBe(45_000);
    });

    it("summarizes job output bytes in a human-readable format", () => {
      const diag: EvenerDiagnostics = {
        jobs: [job({ jobId: "job-1", outputBytes: 2048 })],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      expect(firstWork(view).outputSummary).toBe("2.0 KB");
    });

    it("summarizes small byte counts as raw bytes", () => {
      const diag: EvenerDiagnostics = {
        jobs: [job({ jobId: "job-1", outputBytes: 512 })],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      expect(firstWork(view).outputSummary).toBe("512 B");
    });

    it("includes outputBytes in job diagnostics", () => {
      const diag: EvenerDiagnostics = {
        jobs: [job({ jobId: "job-1", outputBytes: 4096 })],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      expect(firstWork(view).diagnostics?.outputBytes).toBe(4096);
    });
  });

  describe("token/cost/context values", () => {
    it("projects token counts from usage", () => {
      const t = thread({
        evener: evenerThread({
          usage: usage(),
        }),
      });
      const view = service.projectActivity(t);
      expect(view.usage.inputTokens).toBe(100);
      expect(view.usage.outputTokens).toBe(50);
      expect(view.usage.cacheReadTokens).toBe(200);
      expect(view.usage.totalTokens).toBe(350);
    });

    it("projects cost string from evener", () => {
      const t = thread({
        evener: evenerThread({ cost: "$0.042" }),
      });
      const view = service.projectActivity(t);
      expect(view.usage.cost).toBe("$0.042");
    });

    it("projects context pressure and window values", () => {
      const t = thread({
        evener: evenerThread({
          contextUsed: 50_000,
          contextWindow: 200_000,
          contextRemaining: 150_000,
          contextPressure: 0.25,
        }),
      });
      const view = service.projectActivity(t);
      expect(view.usage.contextUsed).toBe(50_000);
      expect(view.usage.contextWindow).toBe(200_000);
      expect(view.usage.contextRemaining).toBe(150_000);
      expect(view.usage.contextPressure).toBe(0.25);
    });

    it("projects work duration from workMillis", () => {
      const t = thread({
        evener: evenerThread({ workMillis: 120_000 }),
      });
      const view = service.projectActivity(t);
      expect(view.usage.durationMs).toBe(120_000);
    });

    it("defaults usage fields to undefined when absent", () => {
      const t = thread({ evener: evenerThread() });
      const view = service.projectActivity(t);
      expect(view.usage.inputTokens).toBeUndefined();
      expect(view.usage.cost).toBeUndefined();
      expect(view.usage.contextPressure).toBeUndefined();
    });
  });

  describe("capabilities and reasoning", () => {
    it("projects capabilities 1:1 from the thread", () => {
      const caps: ThreadCapabilities = {
        send: true,
        steer: false,
        interrupt: true,
        compact: false,
        clear: true,
        forkFromTurn: true,
        shutdown: true,
        changeModel: false,
        queue: true,
        goal: false,
        rename: true,
      };
      const t = thread({ evener: evenerThread({ capabilities: caps }) });
      const view = service.projectActivity(t);
      expect(view.capabilities).toEqual(caps as MobileCapabilities);
    });

    it("projects reasoning effort and levels", () => {
      const t = thread({
        evener: evenerThread({
          reasoningEffort: "high",
          reasoningEffortLevels: ["low", "medium", "high"],
          supportsReasoning: true,
        }),
      });
      const view = service.projectActivity(t);
      expect(view.reasoningEffort).toBe("high");
      expect(view.reasoningEffortLevels).toEqual(["low", "medium", "high"]);
      expect(view.supportsReasoning).toBe(true);
    });

    it("omits reasoning fields when absent", () => {
      const t = thread({ evener: evenerThread() });
      const view = service.projectActivity(t);
      expect(view.reasoningEffort).toBeUndefined();
      expect(view.reasoningEffortLevels).toBeUndefined();
      expect(view.supportsReasoning).toBeUndefined();
    });
  });

  describe("redacted diagnostics", () => {
    it("includes only allowlisted metadata in job diagnostics", () => {
      const diag: EvenerDiagnostics = {
        jobs: [
          job({
            jobId: "job-1",
            jobType: "shell",
            status: "running",
            outputBytes: 1024,
            exitCode: undefined,
            command: "curl https://secret.example.com/?token=hunter2",
            task: "exfiltrate data",
          }),
        ],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      const d = firstWork(view).diagnostics as RedactedDiagnostic;
      // Allowlisted fields present:
      expect(d.operationName).toBe("shell");
      expect(d.statusClass).toBe("running");
      expect(d.outputBytes).toBe(1024);
      expect(d.rawId).toBe("job-1");
      // Disallowed fields absent:
      expect(d).not.toHaveProperty("command");
      expect(d).not.toHaveProperty("task");
    });

    it("never exposes delegate transcript text or message payload", () => {
      const diag: EvenerDiagnostics = {
        delegates: [
          delegate({
            delegateId: "dlg-1",
            transcriptRef: "local:01ABC",
            message: { secret: "payload" },
            structuredResult: { data: "leak" },
          }),
        ],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      const d = firstWork(view).diagnostics as RedactedDiagnostic;
      expect(d).not.toHaveProperty("transcriptRef");
      expect(d).not.toHaveProperty("message");
      expect(d).not.toHaveProperty("structuredResult");
    });

    it("includes timestamps and duration in delegate diagnostics", () => {
      const diag: EvenerDiagnostics = {
        delegates: [
          delegate({
            delegateId: "dlg-1",
            runStartedAt: "2026-01-01T00:00:00Z",
            runEndedAt: "2026-01-01T00:01:00Z",
            durationMs: 60_000,
          }),
        ],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      const d = firstWork(view).diagnostics as RedactedDiagnostic;
      expect(d.startedAt).toBe("2026-01-01T00:00:00Z");
      expect(d.endedAt).toBe("2026-01-01T00:01:00Z");
      expect(d.durationMs).toBe(60_000);
    });

    it("redacts profile IDs in delegate diagnostics", () => {
      const diag: EvenerDiagnostics = {
        delegates: [
          delegate({
            delegateId: "dlg-1",
            resolvedProfileId: "profile-secret-123",
          }),
        ],
      };
      const t = thread({ evener: evenerThread({ diagnostics: diag }) });
      const view = service.projectActivity(t);
      const d = firstWork(view).diagnostics as RedactedDiagnostic;
      // Profile ID is redacted to a class, not the raw value.
      expect(d.profileId).toBe("redacted");
      expect(d).not.toHaveProperty("resolvedProfileId");
    });
  });

  describe("determinism", () => {
    it("produces identical output for identical input", () => {
      const diag: EvenerDiagnostics = {
        delegates: [delegate({ delegateId: "dlg-1", durationMs: 1000 })],
        jobs: [job({ jobId: "job-1", parentDelegateId: "dlg-1" })],
      };
      const t = thread({
        evener: evenerThread({
          diagnostics: diag,
          tasks: { total: 3, done: 1 },
          usage: usage(),
        }),
      });
      const a = service.projectActivity(t);
      const b = service.projectActivity(t);
      expect(a).toEqual(b);
    });
  });
});
