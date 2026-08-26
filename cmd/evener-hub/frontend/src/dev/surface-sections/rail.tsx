// The rail surface: the REAL Rail component (shell/rail/Rail.tsx), fed by
// seeding navigationStore directly with fixture navigation resources — a
// plain zustand store, so the Rail renders from already-present data and
// never issues a network request. No network, no FakeClient.
import { useEffect } from "react";
import type {
  NavigationManifest,
  NavigationProjectCatalog,
  NavigationProjectResource,
  NavigationSectionResource,
  NavigationSessionSummary,
} from "../../protocol/types.gen";
import { Rail } from "../../shell/rail/Rail";
import { navigationStore } from "../../stores/navigation/store";
import { keyID, type ResourceState } from "../../stores/navigation/types";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

const GENERATION = "gallery-generation";

function session(overrides: Partial<NavigationSessionSummary>): NavigationSessionSummary {
  return {
    ref: "ref",
    host_id: "local",
    session_id: "sess_1",
    title: "session",
    project: "evener",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}

const LIVE_SECTION: NavigationSectionResource = {
  generation_id: GENERATION,
  revision: 1,
  sessions: [
    session({ ref: "ref_working", title: "Fix the flaky prune test", state: "active", branch: "fix/prune-race" }),
    session({ ref: "ref_needsyou", title: "Question about the release branch", state: "awaiting", ask_pending: true, branch: "main" }),
    session({ ref: "ref_failed", title: "Refactor the job scheduler", state: "errored", branch: "refactor/scheduler" }),
    session({ ref: "ref_idle", title: "Write docs for the new gallery route", state: "idle" }),
    session({ ref: "ref_dormant", title: "Untitled session", state: "idle", dormant: true }),
  ],
  remaining: 0,
  truncated: false,
};

const NEEDS_YOU_SECTION: NavigationSectionResource = {
  generation_id: GENERATION,
  revision: 1,
  sessions: [],
  remaining: 0,
  truncated: false,
};

const PROJECT_CATALOG: NavigationProjectCatalog = {
  generation_id: GENERATION,
  revision: 1,
  projects: [
    {
      key: "proj_evener",
      name: "evener",
      working_dir: "/home/dev/evener",
      default_expanded: true,
      session_count: 1,
    },
  ],
  remaining: 0,
};

const PROJECT_RESOURCE: NavigationProjectResource = {
  generation_id: GENERATION,
  revision: 1,
  key: "proj_evener",
  current: {
    sessions: [
      session({ ref: "ref_proj_1", title: "Add the surfaces gallery", state: "active" }),
    ],
    remaining: 0,
  },
  recent: { sessions: [], remaining: 0 },
  archived: { sessions: [], remaining: 0 },
  truncated: false,
};

const MANIFEST: NavigationManifest = {
  generation_id: GENERATION,
  revision: 1,
  sources: [],
  attentionSummary: { needsYou: 1, error: 1, working: 1 },
  sections: { live: { count: LIVE_SECTION.sessions.length }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
};

function resourceState<T>(key: NavigationResourceKey, data: T): ResourceState<T> {
  return {
    key: key as ResourceState["key"],
    data,
    loadedRevision: 1,
    targetRevision: 1,
    forceToken: 0,
    etag: '"gallery"',
    loading: false,
    stale: false,
    error: null,
    generationID: GENERATION,
  };
}

type NavigationResourceKey =
  | { kind: "manifest" }
  | { kind: "section"; section: "live" | "needs_you"; offset: number; limit: number }
  | { kind: "catalog"; catalog: "projects" | "archived_projects" | "test_runs"; offset: number; limit: number }
  | { kind: "project"; projectKey: string };

export default function RailSurfaceSection() {
  useEffect(() => {
    const resources = new Map();
    resources.set(keyID({ kind: "section", section: "live", offset: 0, limit: 50 }), resourceState({ kind: "section", section: "live", offset: 0, limit: 50 }, LIVE_SECTION));
    resources.set(keyID({ kind: "section", section: "needs_you", offset: 0, limit: 50 }), resourceState({ kind: "section", section: "needs_you", offset: 0, limit: 50 }, NEEDS_YOU_SECTION));
    resources.set(keyID({ kind: "catalog", catalog: "projects", offset: 0, limit: 100 }), resourceState({ kind: "catalog", catalog: "projects", offset: 0, limit: 100 }, PROJECT_CATALOG));
    resources.set(keyID({ kind: "project", projectKey: "proj_evener" }), resourceState({ kind: "project", projectKey: "proj_evener" }, PROJECT_RESOURCE));
    navigationStore.setState({
      mode: "v1",
      capability: { version: 1, generationId: GENERATION, sequence: 0 },
      clientGenerationID: GENERATION,
      lastSequence: 0,
      manifest: resourceState({ kind: "manifest" }, MANIFEST),
      resources,
      attention: { changed: [], summary: MANIFEST.attentionSummary },
      protocolError: null,
    });
  }, []);

  return (
    <section>
      <h2>Rail</h2>
      <p className={styles.note}>
        Real Rail, with navigationStore seeded directly (fixture navigation resources — no
        network). Live sessions in different states: working, needs-you (a pending question),
        failed, idle, dormant, plus one nested under a project.
      </p>
      <ThemeFlip>
        <Rail width={280} />
      </ThemeFlip>
    </section>
  );
}
