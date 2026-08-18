// The rail surface: the REAL Rail component (shell/rail/Rail.tsx), fed by
// seeding treeStore directly with a fixture TreeResponse - a plain zustand
// store, so treeStore.getState().ensureLoaded() (Rail's own mount effect)
// sees a tree already present and never issues a network request at all
// (stores/tree.ts's own ensureLoaded: "if (get().tree !== null) return
// Promise.resolve(true)"). No network, no FakeClient.
import { useEffect } from "react";
import { Rail } from "../../shell/rail/Rail";
import type { TreeNode, TreeResponse } from "../../stores/tree";
import { treeStore } from "../../stores/tree";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

function node(overrides: Partial<TreeNode>): TreeNode {
  return {
    row_id: overrides.ref ?? "row",
    ref: "ref",
    host_id: "host_1",
    session_id: "sess_1",
    title: "session",
    project: "evener",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    age: "2m",
    ...overrides,
  };
}

const FIXTURE_TREE: TreeResponse = {
  generated_at: new Date().toISOString(),
  sources: [],
  attentionSummary: { needsYou: 1, error: 1, working: 1 },
  needs_you: [],
  pin_sections: [],
  archived_projects: [],
  test_runs: [],
  live: [
    node({
      row_id: "row_working",
      ref: "ref_working",
      title: "Fix the flaky prune test",
      state: "active",
      branch: "fix/prune-race",
    }),
    node({
      row_id: "row_needsyou",
      ref: "ref_needsyou",
      title: "Question about the release branch",
      state: "awaiting",
      ask_pending: true,
      branch: "main",
    }),
    node({
      row_id: "row_failed",
      ref: "ref_failed",
      title: "Refactor the job scheduler",
      state: "errored",
      branch: "refactor/scheduler",
    }),
    node({
      row_id: "row_idle",
      ref: "ref_idle",
      title: "Write docs for the new gallery route",
      state: "idle",
    }),
    node({
      row_id: "row_dormant",
      ref: "ref_dormant",
      title: "Untitled session",
      state: "idle",
      dormant: true,
    }),
  ],
  projects: [
    {
      key: "proj_evener",
      name: "evener",
      working_dir: "/home/dev/evener",
      sessions: [
        node({
          row_id: "row_proj_1",
          ref: "ref_proj_1",
          title: "Add the surfaces gallery",
          state: "active",
          tier: "current",
        }),
      ],
    },
  ],
};

export default function RailSurfaceSection() {
  useEffect(() => {
    treeStore.setState({ tree: FIXTURE_TREE, treeGeneration: 1, loading: false, error: null });
  }, []);

  return (
    <section>
      <h2>Rail</h2>
      <p className={styles.note}>
        Real Rail, with treeStore seeded directly (a fixture TreeResponse - no network). Live sessions in different
        states: working, needs-you (a pending question), failed, idle, dormant, plus one nested under a project.
      </p>
      <ThemeFlip>
        <Rail width={280} />
      </ThemeFlip>
    </section>
  );
}
