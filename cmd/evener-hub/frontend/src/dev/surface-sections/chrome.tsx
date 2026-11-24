// The session chrome surface: StatusRow / TasksPanel, all real components that
// take a plain ThreadModel prop - no store seeding needed, just a fixture model
// built through hydrateThread (the same real hydration function the wire read
// path uses) so every derived field (contextPressure, capabilities, …) is
// honest, not hand-typed.
//
// TasksPanel's trigger renders for free off model.tasks; opening its Sheet
// fires a real threadsStore.getState().listTasks() RPC, which has nothing to
// talk to here and resolves into that panel's own honest "couldn't load"
// state - exactly what a disconnected client should show. Nothing here fakes
// that response.

import type { Thread } from "@evener/appwire-client";
import { hydrateThread } from "@evener/appwire-client";
import { DetailsPanel } from "../../panes/session/chrome/DetailsPanel";
import { StatusRow } from "../../panes/session/chrome/StatusRow";
import { TasksPanel } from "../../panes/session/chrome/TasksPanel";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";
import { FULL_CAPABILITIES, fixtureThread } from "./thread-fixture";

const ref = "dev-surface-chrome";
const chromeThread: Thread = {
  ...fixtureThread(ref, { id: "thr_dev_chrome", sessionId: "sess_dev_chrome" }),
  evener: {
    ref,
    capabilities: FULL_CAPABILITIES,
    queue: { revision: 0, depth: 2 },
    contextUsed: 42_000,
    contextWindow: 100_000,
    contextPressure: 0.42,
    contextAssumed: false,
    tasks: { total: 5, done: 2 },
    goal: { status: "in progress", iterations: 3 },
    reasoningEffort: "medium",
    reasoningEffortLevels: ["low", "medium", "high"],
    supportsReasoning: true,
    workMillis: 45_000,
    failedToolCalls: 3,
    access: { sandbox: "workspace-write", network: false },
    diagnostics: {
      plugins: ["superpowers", "go", "frontend-design"].map((name) => ({
        name,
        skillCount: 0,
        agentCount: 0,
        hookCount: 0,
        mcpCount: 0,
      })),
    },
  },
};

const model = hydrateThread({ thread: chromeThread }, ref, Date.now());

// The session's start-time flag remains readable while disconnected.
const managerFixtures = [
  { name: "ordinary", projection: { manager: false } },
  { name: "manager", projection: { manager: true } },
  { name: "existing", projection: {} },
].map(({ name, projection }) => ({
  name,
  model: hydrateThread({ thread: { ...chromeThread, evener: { ...chromeThread.evener, ...projection } } }, ref, 1000),
}));

export default function ChromeSurfaceSection() {
  return (
    <section>
      <h2>Session chrome</h2>
      <p className={styles.note}>
        StatusRow / TasksPanel, fed a fixture ThreadModel built through the real hydrateThread(). The panel trigger
        opens a real Sheet; its body fetch fails honestly with no live connection behind this gallery.
      </p>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>status</p>
          <StatusRow sessionRef={ref} model={model} now={Date.now()} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>tasks</p>
          <TasksPanel sessionRef={ref} model={model} />
        </div>
        {managerFixtures.map((fixture) => (
          <div key={fixture.name} className={styles.row} data-testid={`manager-fixture-${fixture.name}`}>
            <p className={styles.rowLabel}>Manager {fixture.name}, disconnected</p>
            <DetailsPanel model={fixture.model} now={1000} />
          </div>
        ))}
      </ThemeFlip>
    </section>
  );
}
