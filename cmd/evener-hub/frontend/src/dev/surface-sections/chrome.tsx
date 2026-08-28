// The session chrome surface: StatusRow / GoalControl / TasksPanel /
// ActivityPanel, all real components that take a plain ThreadModel prop -
// no store seeding needed, just a fixture model built through hydrateThread
// (the same real hydration function the wire read path uses) so every
// derived field (contextPressure, capabilities, …) is honest, not hand-typed.
//
// TasksPanel/ActivityPanel's triggers render for free off model.tasks /
// their own summary store; opening either Sheet fires a real
// threadsStore.getState().listTasks()/listJobs() RPC, which has nothing to
// talk to here and resolves into that panel's own honest "couldn't load"
// state - exactly what a disconnected client should show. Nothing here fakes
// that response.

import { ActivityPanel } from "../../panes/session/chrome/ActivityPanel";
import { GoalControl } from "../../panes/session/chrome/GoalControl";
import { StatusRow } from "../../panes/session/chrome/StatusRow";
import { TasksPanel } from "../../panes/session/chrome/TasksPanel";
import { hydrateThread } from "../../protocol/reducer";
import type { Thread, ThreadCapabilities } from "../../protocol/types.gen";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

const FULL_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  rename: true,
};

const ref = "dev-surface-chrome";
const fixtureThread: Thread = {
  id: "thr_dev_chrome",
  sessionId: "sess_dev_chrome",
  preview: "dev fixture",
  ephemeral: false,
  modelProvider: "anthropic/claude-sonnet-4-5",
  createdAt: 1000,
  updatedAt: 1000,
  status: { type: "idle" },
  cwd: "/home/dev/project",
  cliVersion: "1.0.0",
  source: "evener",
  evener: {
    ref,
    capabilities: FULL_CAPABILITIES,
    queue: { revision: 0, depth: 2 },
    contextUsed: 42_000,
    contextWindow: 100_000,
    contextPressure: 0.42,
    tasks: { total: 5, done: 2 },
    goal: { status: "in progress", iterations: 3 },
    reasoningEffort: "medium",
    reasoningEffortLevels: ["low", "medium", "high"],
    supportsReasoning: true,
    workMillis: 45_000,
  },
};

const model = hydrateThread({ thread: fixtureThread }, ref, Date.now());

export default function ChromeSurfaceSection() {
  return (
    <section>
      <h2>Session chrome</h2>
      <p className={styles.note}>
        StatusRow / GoalControl / TasksPanel / ActivityPanel, fed a fixture ThreadModel built through the real
        hydrateThread(). The two panel triggers open a real Sheet; their body fetches fail honestly with no live
        connection behind this gallery.
      </p>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>status</p>
          <StatusRow sessionRef={ref} model={model} now={Date.now()} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>goal</p>
          <GoalControl sessionRef={ref} model={model} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>tasks</p>
          <TasksPanel sessionRef={ref} model={model} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>activity</p>
          <ActivityPanel sessionRef={ref} model={model} now={Date.now()} />
        </div>
      </ThemeFlip>
    </section>
  );
}
