import type { ActivityJob, ActivityTree, TurnModel } from "@evener/appwire-client";
import { buildEntityView, type EntityView } from "@evener/appwire-client";

/** The job id a tool summary names and the entity map below indexes. */
export const SUMMARY_ENTITY_JOB = "job_02wMz5TxvEMoJEDTDGOTil_000000000123";

/** The entity map a tool-summary suite renders through: one completed shell
 * job, plus whatever turns the caller needs for the OTHER ids its summary
 * names (a job_watch summary's watch id lives in a turn item). */
export function summaryEntityView(turns: TurnModel[] = []): ReadonlyMap<string, EntityView> {
  const job: ActivityJob = {
    jobId: SUMMARY_ENTITY_JOB,
    ownerSessionId: "02wMz5TxvEMoJEDTDGOTil",
    ownerRef: "local:s",
    type: "shell",
    status: "completed",
    outcome: "success",
    terminal: true,
    background: false,
    hasOutput: true,
    description: "Compile the frontend",
    command: "npm run build",
    startedAt: "2026-09-13T20:00:00Z",
    exitCode: 0,
    outputBytes: 12,
  };
  const tree: ActivityTree = {
    revision: 1,
    root: {
      kind: "session",
      sessionId: "02wMz5TxvEMoJEDTDGOTil",
      ref: "local:s",
      label: "root",
      aggregate: "completed",
      counts: { active: 0, failed: 0, completed: 1, complete: true },
      entries: [{ kind: "shell", job }],
      branch: {},
    },
  };
  return buildEntityView({ sessionRef: "local:s", tree, turns, stale: false, ended: false });
}
