// Mockups for two proposed changes (mockups.html). NOT product code and not a
// guard: this page exists so the design can be reviewed before anything ships.
//
//   1. Remote session launch: the Host picker above the working directory.
//   2. Sidebar: an "Organize by" control choosing host→project or
//      project→host grouping for the Projects section.
//
// Fidelity: everything under review renders with the production components,
// widgets, and CSS modules — the real Spawn pane, the real Tree + RailRow
// rows, the real RadioGroup/Popover/SegmentedControl/IconButton — against a
// scripted FakeClient (canned data, not a live hub; nothing here issues a
// network request). Two deliberate preview tricks, both marked where they
// appear:
//
//   - The "Host above directory" spawn variant reorders the REAL pane with a
//     scoped `order: -1` rule on the host FormRow (the pane's .form is a
//     flex column). The shipped change would move the JSX instead, so tab
//     order follows visual order there; in this preview the tab order still
//     walks the DOM.
//   - The rail is hand-composed from the real chrome classes (Rail.module.css
//     header/section markup) because host group rows and the grouping toggle
//     do not exist in the navigation data model yet. The rows themselves are
//     the real RailRow components over hand-built node trees.

import type { MethodName, NavigationReadParams, NavigationReadResponse, Source } from "@evener/appwire-client";
import { navigationRootContainerKey, type ResourceKey } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { type JSX, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import Spawn from "../../panes/spawn/Spawn";
import { GLOBAL_MODEL_KEY, GLOBAL_WORKING_DIR_KEY, SPAWN_DEFAULTS_PREFIX } from "../../panes/spawn/spawnDefaults";
import { ClientProvider } from "../../shell/clientContext";
import railStyles from "../../shell/rail/Rail.module.css";
import { RailRow, type RailRowActions } from "../../shell/rail/RailRow";
import rowStyles from "../../shell/rail/RailRow.module.css";
import { GearIcon, SearchIcon, SidebarIcon } from "../../shell/rail/railIcons";
import type { ProjectRailNode, RailNode, RailProject, RailSession, SessionRailNode } from "../../shell/rail/railNodes";
import { RailTickProvider } from "../../shell/rail/railNow";
import { connectionStore } from "../../stores/connection";
import { initNavigation, navigationStore } from "../../stores/navigation/store";
import {
  Badge,
  Button,
  Chevron,
  IconButton,
  Popover,
  RadioGroup,
  SegmentedControl,
  Toast,
  Tooltip,
  Tree,
  type TreeNode,
  type TreeRowInfo,
} from "../../widgets";
import formrowStyles from "../../widgets/formrow/formrow.module.css";
import popoverStyles from "../../widgets/popover/popover.module.css";
import styles from "./mockups.module.css";
import "../../styles/tokens.css";
import "../../styles/global.css";

// ---------------------------------------------------------------------------
// Scripted world: four hosts, per-host filesystems, one offline.
// ---------------------------------------------------------------------------

const GENERATION = "mockups-generation";

const HOSTS: Source[] = [
  { id: "local", label: "this host", kind: "local", online: true },
  { id: "devbox", label: "devbox", kind: "appwire", online: true },
  { id: "render-farm", label: "render-farm", kind: "appwire", online: true },
  { id: "ci-runner", label: "ci-runner", kind: "appwire", online: false },
];

interface World {
  recents: string[];
  tree: Map<string, string[]>;
  branch: string;
}

const WORLDS: Record<string, World> = {
  local: {
    recents: ["/home/jesse/git/prime-radiant-inc/evener", "/home/jesse/git/prime-radiant-inc"],
    tree: new Map(
      Object.entries({
        "/": ["home"],
        "/home": ["jesse"],
        "/home/jesse": ["git"],
        "/home/jesse/git": ["prime-radiant-inc"],
        "/home/jesse/git/prime-radiant-inc": ["evener"],
        "/home/jesse/git/prime-radiant-inc/evener": ["agent", "cmd", "web", "docs"],
      }),
    ),
    branch: "main",
  },
  devbox: {
    recents: ["/srv/work/evener", "/srv/work/data-pipeline"],
    tree: new Map(
      Object.entries({
        "/": ["srv"],
        "/srv": ["work"],
        "/srv/work": ["evener", "data-pipeline"],
        "/srv/work/evener": ["agent", "cmd"],
        "/srv/work/data-pipeline": ["etl", "notes"],
      }),
    ),
    branch: "trunk",
  },
  "render-farm": {
    recents: ["/farm/render/jobs"],
    tree: new Map(
      Object.entries({
        "/": ["farm"],
        "/farm": ["render"],
        "/farm/render": ["jobs", "tools"],
        "/farm/render/jobs": ["queue-sim"],
        "/farm/render/tools": [],
      }),
    ),
    branch: "release",
  },
};

const MODELS = {
  data: [
    { provider: "anthropic", model: "claude-sonnet-4-5" },
    { provider: "openai", model: "gpt-5" },
    { provider: "example-provider-with-a-very-long-name", model: "extra-long-qualifier-model-variant-turbo-01" },
  ],
};

const HARNESS = { data: [{ id: "evener", label: "evener", kind: "evener" }] };

const WORKING_INSTANCE = {
  name: "anthropic",
  providerId: "anthropic",
  protocol: "anthropic-messages",
  auth: "bearer",
  implicit: false,
  activeSource: "store",
};

const PLUGINS = {
  plugins: [
    {
      name: "superpowers",
      description: "Marketplace tools and skills",
      source: "installed",
      marketplace: "superpowers-marketplace",
      selected: true,
      skillCount: 12,
      agentCount: 3,
      commandCount: 4,
      hookCount: 0,
      mcpCount: 1,
    },
    {
      name: "layout-harness",
      description: "Directory-sourced dev harnesses",
      source: "directory",
      path: "/tmp/mock-plugin",
      selected: false,
      skillCount: 0,
      agentCount: 0,
      commandCount: 2,
      hookCount: 1,
      mcpCount: 0,
    },
  ],
};

// One answer function shared by the local calls and the evener/host/request
// forwarding, so a remote host behaves the way a real one does: its own
// filesystem, its own recents, its own HEAD.
function answer(host: string, method: string, params: unknown): unknown {
  const world = WORLDS[host];
  switch (method) {
    case "model/list":
      return MODELS;
    case "evener/harnesses/list":
      return HARNESS;
    case "evener/launch/schema":
      return { options: [] };
    case "evener/launch/resolve":
      return { effective: {}, layers: {}, provenance: {} };
    case "evener/instance/list":
      return { instances: [WORKING_INSTANCE], availableProviders: [] };
    case "evener/projects/recent":
      return { data: world?.recents ?? [] };
    case "evener/paths/complete": {
      const prefix = String((params as { prefix?: string } | undefined)?.prefix ?? "").replace(/\/+$/, "");
      return { data: world?.tree.get(prefix) ?? [] };
    }
    case "evener/path/validate": {
      const path = String((params as { path?: string } | undefined)?.path ?? "");
      const resolved = path === "~" ? "/home/jesse" : path;
      const valid = (world?.tree.has(resolved) ?? false) || resolved === "/" || resolved === "~";
      return { path: resolved, valid, error: valid ? undefined : "Directory not found" };
    }
    case "evener/dirs/create": {
      const path = String((params as { path?: string } | undefined)?.path ?? "");
      world?.tree.set(path, []);
      return { path, created: true };
    }
    case "evener/spawn/slashCatalog":
      return { commands: [], skills: [] };
    case "evener/git/head":
      return { head: world?.branch ?? "" };
    case "evener/plugin/preview":
      return PLUGINS;
    default:
      throw new Error(`mockups: no fixture for ${method} on ${host}`);
  }
}

function emptySnapshotMetadata(key: ResourceKey): unknown {
  switch (key.kind) {
    case "section":
    case "pin_section":
      return {
        generation_id: GENERATION,
        revision: 1,
        offset: key.offset,
        limit: key.limit,
        remaining: 0,
        truncated: false,
      };
    case "pin_catalog":
    case "catalog":
      return { generation_id: GENERATION, revision: 1, offset: key.offset, limit: key.limit, remaining: 0 };
    case "project":
      return {
        generation_id: GENERATION,
        revision: 1,
        key: key.projectKey,
        current_remaining: 0,
        recent_remaining: 0,
        archived_remaining: 0,
        truncated: false,
      };
    case "project_page":
      return {
        generation_id: GENERATION,
        revision: 1,
        key: key.projectKey,
        tier: key.tier,
        offset: key.offset,
        limit: key.limit,
        remaining: 0,
      };
    case "location":
      return { generation_id: GENERATION, revision: 1, ref: key.ref, top_level_ref: key.ref, top_level: true };
    default:
      throw new Error(`mockups: no empty snapshot for ${key.kind}`);
  }
}

function navigationResourceKey(params: NavigationReadParams): ResourceKey {
  const offset = params.offset ?? 0;
  const limit = params.limit ?? 0;
  switch (params.resource) {
    case "manifest":
      return { kind: "manifest" };
    case "section":
      return { kind: "section", section: (params.section ?? "live") as "live" | "needs_you", offset, limit };
    case "pin_catalog":
      return { kind: "pin_catalog", offset, limit };
    case "pin_section":
      return { kind: "pin_section", sectionId: params.sectionId ?? "", offset, limit };
    case "catalog":
      return {
        kind: "catalog",
        catalog: (params.catalog ?? "projects") as "projects" | "archived_projects" | "test_runs",
        offset,
        limit,
      };
    case "project":
      return { kind: "project", projectKey: params.projectKey ?? "" };
    case "project_page":
      return {
        kind: "project_page",
        projectKey: params.projectKey ?? "",
        tier: (params.tier ?? "current") as "current" | "recent" | "archived",
        offset,
        limit,
      };
    case "location":
      return { kind: "location", ref: params.ref ?? "" };
    default:
      throw new Error(`mockups: unsupported navigation resource ${JSON.stringify(params.resource)}`);
  }
}

const MANIFEST = {
  generation_id: GENERATION,
  revision: 1,
  sources: HOSTS,
  attentionSummary: { needsYou: 1, error: 0, working: 2 },
  sections: { live: { count: 3 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: 4 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
};

function okRead(data: unknown): NavigationReadResponse {
  return { status: "ok", representation: "snapshot", generationId: GENERATION, revision: 1, etag: '"mockups-1"', data };
}

const fake = new FakeClient("ready");

fake.on("evener/navigation/read", (params: NavigationReadParams) => {
  if (params.representationVersion !== 2) throw new Error("mockups expected v2 navigation reads");
  const key = navigationResourceKey(params);
  if (key.kind === "manifest") {
    return okRead({
      metadata: MANIFEST,
      entities: [],
      containers: [
        {
          key: navigationRootContainerKey(key, "manifest"),
          owner: { kind: "resource_root", slot: "manifest" },
          children: [],
        },
      ],
    });
  }
  return okRead({
    metadata: emptySnapshotMetadata(key),
    entities: [],
    containers: [
      {
        key: navigationRootContainerKey(key, "sessions"),
        owner: { kind: "resource_root", slot: "sessions" },
        children: [],
      },
    ],
  });
});

fake.on("evener/host/request", ({ host, method, params }) => answer(host, method, params) as never);

fake.on("evener/host/attach", ({ host }) => {
  // The offline host's Connect affordance is real: it dials, waits, and
  // reports success — the scripted manifest just keeps listing it offline,
  // the way a hub would until its next manifest refresh.
  return new Promise((resolve) => {
    setTimeout(() => resolve({ attached: true, host }), 1200);
  }) as never;
});

// Local calls: the plain methods the pane issues against its own hub. The
// params-carrying three forward explicitly; the rest are constant.
function scriptLocal(method: MethodName): void {
  fake.on(method, () => answer("local", method, undefined) as never);
}
for (const method of [
  "model/list",
  "evener/harnesses/list",
  "evener/launch/schema",
  "evener/launch/resolve",
  "evener/instance/list",
  "evener/projects/recent",
  "evener/spawn/slashCatalog",
  "evener/plugin/preview",
  "evener/git/head",
] as const satisfies readonly MethodName[]) {
  scriptLocal(method);
}
fake.on("evener/paths/complete", (params) => answer("local", "evener/paths/complete", params) as never);
fake.on("evener/path/validate", (params) => answer("local", "evener/path/validate", params) as never);
fake.on("evener/dirs/create", (params) => answer("local", "evener/dirs/create", params) as never);
fake.on("thread/start", () => {
  throw new Error("Fixture stops at the thread/start RPC boundary");
});

connectionStore.getState().connect(fake);

// The spawn "host above directory" preview: one scoped rule moving the real
// host FormRow (the one containing #spawn-host) to the front of the pane's
// flex-column .form. See the header comment — the shipped change moves the
// JSX instead.
const formrowRoot = String(formrowStyles.root ?? "");
const reorderStyle = document.createElement("style");
reorderStyle.textContent = `[data-mock-order="host-above"] .${formrowRoot}:has(#spawn-host){order:-1}`;
document.head.appendChild(reorderStyle);

// ---------------------------------------------------------------------------
// Spawn section
// ---------------------------------------------------------------------------

type SpawnVariant = "current" | "proposed";

function SpawnSection(): JSX.Element {
  const [variant, setVariant] = useState<SpawnVariant>("proposed");
  return (
    <section className={styles.section}>
      <h2 className={styles.sectionHeading}>Remote session launch: Host above the directory</h2>
      <p className={styles.note}>
        The pane is the real Spawn component against the scripted world (devbox and render-farm online, ci-runner
        offline). Pick devbox, then open the directory picker: the folders, the recents, and the branch chip all come
        from that machine. The toggle only flips this preview — the pane keeps its DOM order, so tab order here still
        walks the old sequence; the shipped change moves the JSX.
      </p>
      <SegmentedControl
        label="Preview order"
        size="sm"
        value={variant}
        onChange={(value) => setVariant(value)}
        options={[
          { value: "current", label: "Current" },
          { value: "proposed", label: "Host above directory" },
        ]}
      />
      <div className={styles.figure}>
        <div className={styles.spawnFrame} data-mock-order={variant === "proposed" ? "host-above" : undefined}>
          <Spawn params={{}} paneId="mockups-spawn" focused />
        </div>
        <p className={styles.figureCaption}>
          Same pane, both orders. Host above reads as: pick the machine first, then the folder on it.
        </p>
      </div>
    </section>
  );
}

// ---------------------------------------------------------------------------
// Rail world: hand-built node trees over the real row components.
// ---------------------------------------------------------------------------

type GroupingMode = "host-project" | "project-host";

// The proposed host group row. HostRailNode is not part of the product's
// RailNode union yet; RailRow's props type only knows today's kinds, so the
// renderRow seam below casts non-host nodes back to RailNode the same way
// hand-built test doubles do.
interface HostRailNode extends TreeNode {
  kind: "host";
  host: { id: string; label: string; online: boolean; attention: number };
  children: MockRailNode[];
}

interface MockProjectRailNode extends Omit<ProjectRailNode, "children"> {
  children: MockRailNode[];
}

type MockRailNode = HostRailNode | MockProjectRailNode | SessionRailNode;

interface SessionSpec {
  key: string;
  title: string;
  state: string;
  live?: boolean;
  branch?: string;
  ageMinutes: number;
  askPending?: boolean;
}

const minutesAgo = (minutes: number): string => new Date(Date.now() - minutes * 60_000).toISOString();

function sessionNode(scope: string, host: string, projectName: string, spec: SessionSpec): SessionRailNode {
  const sessionID = `${projectName}:${spec.key}`;
  const ref = host === "local" ? `local:${sessionID}` : `${host}:${sessionID}`;
  const session: RailSession = {
    ref,
    host_id: host,
    session_id: sessionID,
    title: spec.title,
    project: projectName,
    state: spec.state,
    kind: "session",
    live: spec.live ?? false,
    children: [],
    row_id: `row:${ref}`,
    branch: spec.branch,
    updated_at: minutesAgo(spec.ageMinutes),
    ask_pending: spec.askPending,
  };
  return { id: `${scope}:s:${ref}`, kind: "session", session, children: [] };
}

// The Live section stays flat and cross-project, exactly as today: one
// session per project that is running now, rows already carrying their host
// label when they live on a remote host.
const LIVE_SESSIONS: ReadonlyArray<{ host: string; project: string; spec: SessionSpec }> = [
  {
    host: "local",
    project: "evener",
    spec: {
      key: "watch-delivery",
      title: "Fix flaky watch delivery test",
      state: "active",
      live: true,
      branch: "intent-always-emitted",
      ageMinutes: 4,
    },
  },
  {
    host: "devbox",
    project: "data-pipeline",
    spec: {
      key: "queue-worker",
      title: "Refactor queue worker",
      state: "active",
      live: true,
      branch: "trunk",
      ageMinutes: 1,
    },
  },
  {
    host: "local",
    project: "evener",
    spec: {
      key: "guard-triage",
      title: "Triage failing browser guards",
      state: "awaiting",
      live: true,
      askPending: true,
      ageMinutes: 9,
    },
  },
];

// Live always answers "which machine" the same way, independent of the
// organize-by mode: while running sessions span more than one host, they
// group under host subheaders (hosts with live sessions only); a
// single-host Live stays flat, exactly today's shape.
function liveNodes(scope: string): MockRailNode[] {
  const hostsInPlay = HOSTS.filter((host) => LIVE_SESSIONS.some((entry) => entry.host === host.id));
  if (hostsInPlay.length <= 1) {
    return LIVE_SESSIONS.map(({ host, project, spec }) => sessionNode(scope, host, project, spec));
  }
  return HOSTS.flatMap((host) => {
    const rows = LIVE_SESSIONS.filter((entry) => entry.host === host.id);
    if (rows.length === 0) return [];
    return [
      hostNode(
        scope,
        host.id,
        host.online,
        0,
        rows.map(({ host: rowHost, project, spec }) => sessionNode(scope, rowHost, project, spec)),
        `live:${host.id}`,
      ),
    ];
  });
}

interface ProjectSpec {
  key: string;
  name: string;
  workingDir: string;
  rollupState: string;
  rollupLive: number;
  rollupAttn: number;
  favorite?: boolean;
  sources: string[];
  sessionCount: number;
  sessions: Record<string, SessionSpec[]>;
}

const PROJECT_SPECS: ProjectSpec[] = [
  {
    key: "evener",
    name: "evener",
    workingDir: "/home/jesse/git/prime-radiant-inc/evener",
    rollupState: "active",
    rollupLive: 2,
    rollupAttn: 1,
    favorite: true,
    sources: ["local", "devbox"],
    sessionCount: 3,
    sessions: {
      local: [
        { key: "card-tests", title: "Sweep notification card tests", state: "idle", ageMinutes: 125 },
        { key: "doctor-audit", title: "Audit doctor runbooks", state: "ended", ageMinutes: 1560 },
      ],
      devbox: [{ key: "port-harness", title: "Port guard harness to devbox", state: "idle", ageMinutes: 180 }],
    },
  },
  {
    key: "website",
    name: "website",
    workingDir: "/home/jesse/git/prime-radiant-inc/website",
    rollupState: "ended",
    rollupLive: 0,
    rollupAttn: 0,
    sources: ["local"],
    sessionCount: 1,
    sessions: { local: [{ key: "pricing-faq", title: "Rewrite pricing FAQ", state: "ended", ageMinutes: 2880 }] },
  },
  {
    key: "data-pipeline",
    name: "data-pipeline",
    workingDir: "/srv/work/data-pipeline",
    rollupState: "active",
    rollupLive: 1,
    rollupAttn: 0,
    sources: ["devbox", "ci-runner"],
    sessionCount: 2,
    sessions: {
      devbox: [{ key: "etl-backfill", title: "Backfill etl_notes table", state: "idle", ageMinutes: 300 }],
      "ci-runner": [{ key: "schema-check", title: "Nightly schema check", state: "ended", ageMinutes: 1320 }],
    },
  },
  {
    key: "render-jobs",
    name: "render-jobs",
    workingDir: "/farm/render/jobs",
    rollupState: "idle",
    rollupLive: 0,
    rollupAttn: 0,
    sources: ["render-farm"],
    sessionCount: 2,
    sessions: {
      "render-farm": [
        { key: "queue-batch", title: "Queue simulation batch 47", state: "idle", ageMinutes: 40 },
        { key: "log-trim", title: "Trim render log rotation", state: "ended", ageMinutes: 4320 },
      ],
    },
  },
];

function hostLabel(id: string): string {
  return id === "local" ? "This host" : id;
}

function hostAttention(sessions: readonly SessionSpec[]): number {
  return sessions.filter((spec) => spec.state === "awaiting").length;
}

function projectNode(scope: string, spec: ProjectSpec, children: MockRailNode[]): MockProjectRailNode {
  const project: RailProject = {
    key: spec.key,
    name: spec.name,
    working_dir: spec.workingDir,
    rollup_state: spec.rollupState,
    rollup_live: spec.rollupLive,
    rollup_attn: spec.rollupAttn,
    favorite: spec.favorite,
    sources: spec.sources,
    session_count: spec.sessionCount,
    sessions: [],
  };
  return { id: `${scope}:p:${spec.key}`, kind: "project", project, children };
}

function hostNode(
  scope: string,
  id: string,
  online: boolean,
  attention: number,
  children: MockRailNode[],
  nodeKey: string = id,
): HostRailNode {
  return {
    id: `${scope}:h:${nodeKey}`,
    kind: "host",
    host: { id, label: hostLabel(id), online, attention },
    children,
  };
}

function hostProjectNodes(scope: string): MockRailNode[] {
  return HOSTS.map((host) => {
    const children = PROJECT_SPECS.filter((spec) => spec.sessions[host.id] !== undefined).map((spec) =>
      projectNode(
        scope,
        spec,
        (spec.sessions[host.id] ?? []).map((session) => sessionNode(scope, host.id, spec.name, session)),
      ),
    );
    const attention = PROJECT_SPECS.reduce((sum, spec) => sum + hostAttention(spec.sessions[host.id] ?? []), 0);
    return hostNode(scope, host.id, host.online, attention, children);
  });
}

function projectHostNodes(scope: string): MockRailNode[] {
  return PROJECT_SPECS.map((spec) =>
    projectNode(
      scope,
      spec,
      HOSTS.filter((host) => spec.sessions[host.id] !== undefined).map((host) =>
        hostNode(
          scope,
          host.id,
          host.online,
          hostAttention(spec.sessions[host.id] ?? []),
          (spec.sessions[host.id] ?? []).map((session) => sessionNode(scope, host.id, spec.name, session)),
          `${spec.key}:${host.id}`,
        ),
      ),
    ),
  );
}

function withExpansion<T extends TreeNode>(nodes: T[], expanded: ReadonlySet<string>): T[] {
  return nodes.map((node) => {
    const children = withExpansion((node.children ?? []) as T[], expanded);
    return { ...node, expanded: expanded.has(node.id), children } as T;
  });
}

// ---------------------------------------------------------------------------
// Rail rows: real RailRow for projects and sessions, the proposed HostRow for
// host groups.
// ---------------------------------------------------------------------------

const MOCK_ACTIONS: RailRowActions = {
  onOpenSessionPane: () => {},
  onRenameSession: () => Promise.resolve(),
  onShutdownSession: () => Promise.resolve(),
  onForceStopSession: () => Promise.resolve(),
  onPinSession: () => Promise.resolve(),
  onUnpinRequest: () => Promise.resolve(),
  onToggleArchiveSession: () => Promise.resolve(),
  onDeleteSession: () => Promise.resolve(),
  onToggleFavoriteProject: () => {},
  onToggleArchiveProject: () => {},
  onDeleteProjectRequest: () => {},
};

// The drawn host glyph: the rail's 16x16 icon grammar (railIcons.tsx), sized
// and inked like the watch row's drawn clock (RailRow.module.css .watchGlyph).
function HostGlyph(): JSX.Element {
  return (
    <svg className={styles.hostGlyph} viewBox="0 0 16 16" aria-hidden="true" focusable="false">
      <rect x="2" y="2.75" width="12" height="4.5" rx="1.25" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <rect x="2" y="8.75" width="12" height="4.5" rx="1.25" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M4.75 5h.01M4.75 11h.01" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" />
    </svg>
  );
}

// A host group row: the project row's anatomy (RailRow.module.css classes) —
// leading glyph instead of a signal dot (a host is infrastructure, not
// triage), label, trailing chevron, attention Badge when a host's sessions
// need you. Offline follows the session rows' own host-label convention:
// italic, dimmed, with "(offline)" in the caption ink. (The Badge is
// superseded: the shipped host row carries none - the manifest has no
// per-host attention count - kept here because this page is the frozen
// review artifact that was approved.)
function HostRow({ node, info }: { node: HostRailNode; info: TreeRowInfo }): JSX.Element {
  const { host } = node;
  const labelClass = host.online ? rowStyles.label : `${rowStyles.label} ${rowStyles.hostOffline}`;
  return (
    <span
      className={rowStyles.railRow}
      data-testid="mock-host-row"
      title={host.online ? `Host ${host.label}` : `Host ${host.label} is offline`}
    >
      <span className={rowStyles.textCol}>
        <span className={rowStyles.titleLine}>
          <HostGlyph />
          {/* biome-ignore lint/a11y/noStaticElementInteractions: mirrors RailRow's own label convention — the treeitem owns keyboard activation */}
          {/* biome-ignore lint/a11y/useKeyWithClickEvents: same as RailRow — Enter on the treeitem activates */}
          <span className={labelClass} onClick={info.activate}>
            {host.label}
          </span>
          {!host.online && <span className={rowStyles.host}>{"(offline)"}</span>}
          {info.hasChildren && (
            <span
              className={rowStyles.chevronButton}
              aria-hidden="true"
              onClick={(event) => {
                event.stopPropagation();
                info.toggle();
              }}
            >
              <Chevron direction={info.expanded ? "down" : "right"} />
            </span>
          )}
        </span>
      </span>
      {host.attention > 0 && <Badge count={host.attention} tone="attention" />}
    </span>
  );
}

// The organize-by glyph candidates, drawn on the rail's 16x16 icon grid.
//
// TuneGlyph is the one in use: three lines with staggered knobs — the
// cross-platform "arrange this view" convention (Finder's view options,
// Material's tune), so an icon-only control still reads. The knobs are
// round-cap zero-length paths at a heavier stroke, the same dot idiom
// HostGlyph uses.
function TuneGlyph({ className }: { className?: string }): JSX.Element {
  return (
    <svg className={className} viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">
      <g fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
        <path d="M2.25 4.25h11.5M2.25 8h11.5M2.25 11.75h11.5" />
      </g>
      <g stroke="currentColor" strokeWidth="2.5" strokeLinecap="round">
        <path d="M10.75 4.25h.01M5.25 8h.01M9.25 11.75h.01" />
      </g>
    </svg>
  );
}

// Grouping-specific but less universally recognized: two rows gathered by a
// left brace, one below outside it.
function GroupRowsGlyph({ className }: { className?: string }): JSX.Element {
  return (
    <svg className={className} viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">
      <g fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
        <path d="M6.25 4h7.5M6.25 8.75h7.5" />
        <path d="M3.25 3.25v6M3.25 3.25h1.5M3.25 9.25h1.5" />
        <path d="M2.25 12.75h11.5" />
      </g>
    </svg>
  );
}

// The original tree/hierarchy glyph — precise, but it reads as structure,
// not as "choose the arrangement".
function OrganizeGlyph({ className }: { className?: string }): JSX.Element {
  return (
    <svg className={className} viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">
      <g fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
        <circle cx="3.25" cy="4.25" r="1.75" />
        <circle cx="3.25" cy="11.75" r="1.75" />
        <circle cx="12.75" cy="8" r="1.75" />
        <path d="M5 4.25h1.75a2 2 0 0 1 2 2v.75M5 11.75h1.75a2 2 0 0 0 2-2v-.75" />
      </g>
    </svg>
  );
}

const ORGANIZE_OPTIONS = [
  { value: "host-project", label: "Host, then project" },
  { value: "project-host", label: "Project, then host" },
];

// The sidebar-wide organize-by switcher: an icon-only quiet trigger, hard
// right in its own row between Live and the section it controls, opening
// the RadioGroup popover. The Tooltip carries the name the icon drops, and
// the popover's checked option plus the re-titled section below ("Hosts" /
// "Projects") state the current mode.
function OrganizeControl({
  mode,
  onChange,
}: {
  mode: GroupingMode;
  onChange: (mode: GroupingMode) => void;
}): JSX.Element {
  const [open, setOpen] = useState(false);
  return (
    <Popover
      open={open}
      onClose={() => setOpen(false)}
      trigger={
        <Tooltip label="Organize by">
          <IconButton
            label="Organize by"
            icon={<TuneGlyph />}
            variant="quiet"
            size="sm"
            onClick={() => setOpen((current) => !current)}
          />
        </Tooltip>
      }
    >
      <div className={styles.closeupPanelPad}>
        <RadioGroup
          label="Organize by"
          value={mode}
          onChange={(value) => onChange(value as GroupingMode)}
          options={ORGANIZE_OPTIONS}
        />
      </div>
    </Popover>
  );
}

// Section markup mirrors Rail.tsx's own SectionHeading + RailSection, using
// the same Rail.module.css classes.
function MockSection(props: {
  title: string;
  nodes: MockRailNode[];
  open: boolean;
  onToggleOpen: () => void;
  renderRow: (node: MockRailNode, info: TreeRowInfo) => JSX.Element;
  onToggle: (id: string) => void;
}): JSX.Element {
  const { title, nodes, open, onToggleOpen, renderRow, onToggle } = props;
  if (nodes.length === 0) return <span className="mock-section-empty" hidden />;
  return (
    <section className={railStyles.section}>
      <div className={railStyles.sectionHeadingRow}>
        <h3 className={`${railStyles.sectionTitle} ${railStyles.staticSectionLabel}`}>
          <button type="button" className={railStyles.sectionDisclosure} aria-expanded={open} onClick={onToggleOpen}>
            {title}
            <Chevron direction={open ? "down" : "right"} />
          </button>
        </h3>
      </div>
      {open && (
        <Tree nodes={nodes} onToggle={(node) => onToggle(node.id)} onActivate={() => {}} renderRow={renderRow} />
      )}
    </section>
  );
}

function MockRail(props: { scope: string; initialMode: GroupingMode; initiallyExpanded: string[] }): JSX.Element {
  const { scope, initialMode, initiallyExpanded } = props;
  const [mode, setMode] = useState<GroupingMode>(initialMode);
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set(initiallyExpanded));
  const [sectionsOpen, setSectionsOpen] = useState<ReadonlySet<string>>(() => new Set(["live", "projects"]));

  const renderRow = (node: MockRailNode, info: TreeRowInfo): JSX.Element =>
    node.kind === "host" ? (
      <HostRow node={node} info={info} />
    ) : (
      <RailRow node={node as RailNode} info={info} actions={MOCK_ACTIONS} />
    );

  const grouped = mode === "host-project" ? hostProjectNodes(scope) : projectHostNodes(scope);
  const nodes = withExpansion(grouped, expanded);
  const live = withExpansion(liveNodes(scope), expanded);

  const toggleRow = (id: string): void => {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };
  const toggleSection = (id: string): void => {
    setSectionsOpen((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  return (
    <div className={railStyles.rail}>
      <div className={railStyles.header}>
        <div className={railStyles.brand}>
          <span className={railStyles.brandIdentity}>evener</span>
          <Badge count={1} tone="attention" />
          <IconButton label="Settings" icon={<GearIcon />} variant="quiet" size="md" onClick={() => {}} />
          <Tooltip label="Search sessions and commands">
            <IconButton label="Search" icon={<SearchIcon />} variant="quiet" size="md" onClick={() => {}} />
          </Tooltip>
          <IconButton label="Hide sidebar" icon={<SidebarIcon />} variant="quiet" size="md" onClick={() => {}} />
        </div>
        <div className={railStyles.newSession}>
          <Button variant="primary" onClick={() => {}}>
            {"+ New session"}
          </Button>
        </div>
      </div>
      <RailTickProvider>
        <div className={railStyles.body}>
          <MockSection
            title="Live"
            nodes={live}
            open={sectionsOpen.has("live")}
            onToggleOpen={() => toggleSection("live")}
            renderRow={renderRow}
            onToggle={toggleRow}
          />
          {/* The switcher row: between Live and the section it controls, so
              it reads as the toolbar for everything below it and never as a
              Live affordance. */}
          <div className={styles.organizeRow}>
            <OrganizeControl mode={mode} onChange={setMode} />
          </div>
          <MockSection
            title={mode === "host-project" ? "Hosts" : "Projects"}
            nodes={nodes}
            open={sectionsOpen.has("projects")}
            onToggleOpen={() => toggleSection("projects")}
            renderRow={renderRow}
            onToggle={toggleRow}
          />
        </div>
      </RailTickProvider>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

function RailSection(): JSX.Element {
  return (
    <section className={styles.section}>
      <h2 className={styles.sectionHeading}>Sidebar: organize by host then project, or project then host</h2>
      <p className={styles.note}>
        Two instances of the same mock rail, one per grouping. The switcher is an icon-only control hard right in its
        own row between Live and the section it governs — the tooltip names it, and the popover&#39;s checked option
        plus the re-titled section below (Hosts / Projects) state the mode. It is live on both rails, so flip either.
        Live groups under host subheaders whenever sessions span more than one host, in either mode; pinned sections
        would keep your own grouping. Row content under the host and project groups is the real RailRow; host group rows
        are the proposed row (drawn glyph, trailing chevron, attention badge; offline in the italic "(offline)"
        convention session rows already use).
      </p>
      <div className={styles.strip}>
        <figure className={styles.figure}>
          <div className={styles.railFrame}>
            <MockRail
              scope="a"
              initialMode="host-project"
              initiallyExpanded={[
                "a:h:live:local",
                "a:h:live:devbox",
                "a:h:local",
                "a:h:devbox",
                "a:p:evener",
                "a:p:website",
                "a:p:data-pipeline",
              ]}
            />
          </div>
          <figcaption className={styles.figureCaption}>
            Host, then project — the section re-titles to Hosts, machines are the top groups.
          </figcaption>
        </figure>
        <figure className={styles.figure}>
          <div className={styles.railFrame}>
            <MockRail
              scope="b"
              initialMode="project-host"
              initiallyExpanded={[
                "b:h:live:local",
                "b:h:live:devbox",
                "b:p:evener",
                "b:p:website",
                "b:p:data-pipeline",
                "b:h:evener:local",
                "b:h:data-pipeline:devbox",
              ]}
            />
          </div>
          <figcaption className={styles.figureCaption}>
            Project, then host — today&#39;s grouping for the tree, hosts nested inside each project. Live keeps its
            host subheaders either way: sessions span two machines here.
          </figcaption>
        </figure>
      </div>
    </section>
  );
}

function ControlCloseups(): JSX.Element {
  const [mode, setMode] = useState<GroupingMode>("host-project");
  return (
    <section className={styles.section}>
      <h2 className={styles.sectionHeading}>The organize-by control, close up</h2>
      <p className={styles.note}>
        Left — the chosen control: an icon-only quiet trigger (the tooltip names it) opening the RadioGroup popover,
        whose checked option states the current mode. Middle — the always-visible SegmentedControl alternative, at the
        cost of a permanently taller row in a 280px rail. Right — icon candidates for the trigger: the tune glyph in use
        is the cross-platform "arrange this view" convention; the other two are drawn for comparison.
      </p>
      <div className={styles.closeupStrip}>
        <div className={styles.closeup}>
          <div className={styles.closeupHeadingRow}>
            <Tooltip label="Organize by">
              <IconButton label="Organize by" icon={<TuneGlyph />} variant="quiet" size="sm" onClick={() => {}} />
            </Tooltip>
          </div>
          <div className={`${popoverStyles.panel} ${styles.closeupPanel} ${styles.closeupPanelPad}`}>
            <RadioGroup
              label="Organize by"
              value={mode}
              onChange={(value) => setMode(value as GroupingMode)}
              options={ORGANIZE_OPTIONS}
            />
          </div>
        </div>
        <div className={styles.closeup}>
          <div className={styles.closeupHeadingRow}>
            <span className={railStyles.staticSectionLabel}>PROJECTS</span>
            <SegmentedControl
              label="Group by"
              size="sm"
              value={mode}
              onChange={(value) => setMode(value as GroupingMode)}
              options={[
                { value: "host-project", label: "Host" },
                { value: "project-host", label: "Project" },
              ]}
            />
          </div>
        </div>
        <div className={styles.closeup}>
          <div className={styles.candidateRow}>
            <div className={styles.candidate}>
              <IconButton label="Tune" icon={<TuneGlyph />} variant="quiet" size="sm" onClick={() => {}} />
              <span className={styles.candidateCaption}>Tune — "arrange this view". In use.</span>
            </div>
            <div className={styles.candidate}>
              <IconButton label="Grouped rows" icon={<GroupRowsGlyph />} variant="quiet" size="sm" onClick={() => {}} />
              <span className={styles.candidateCaption}>Grouped rows — says grouping, less known.</span>
            </div>
            <div className={styles.candidate}>
              <IconButton label="Tree" icon={<OrganizeGlyph />} variant="quiet" size="sm" onClick={() => {}} />
              <span className={styles.candidateCaption}>Tree — reads as structure, not choice.</span>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

function NotesSection(): JSX.Element {
  return (
    <section className={styles.section}>
      <h2 className={styles.sectionHeading}>What the real change would touch</h2>
      <ul className={styles.notesList}>
        <li>
          Spawn: move the Host FormRow above the directory row in panes/spawn/Spawn.tsx. Nothing else changes — the
          directory picker, recents, validation, and the branch chip already run against the selected host through
          hostRequest, which is exactly why host-first is the natural order: the folder list depends on the machine.
        </li>
        <li>
          Sidebar: a persisted, sidebar-wide pref (for example sidebarGrouping: "host-project" | "project-host",
          defaulting to "project-host", today&#39;s shape). Its control is an icon-only trigger (the tune glyph, the
          cross-platform "arrange view" convention) hard right in its own row between Live and the section it governs,
          and that section re-titles to match — Hosts when hosts lead, Projects when projects do. Hosts come from the
          navigation manifest&#39;s sources — grouping is a client-side reshaping of the same project and session nodes,
          so no wire changes.
        </li>
        <li>
          Host group rows: a new rail node kind rendered like a project row, with the drawn host glyph in place of the
          signal dot (a host is infrastructure, not triage), a trailing chevron, and the attention Badge when any
          session under the host needs you. Ordering: This host first, then online hosts alphabetically, offline hosts
          last.
        </li>
        <li>
          Live always groups under host subheaders while its sessions span more than one host, in either mode — hosts
          with live sessions only, so a single-machine Live stays flat, exactly today. Pinned sections stay flat in both
          modes: a pin is your own grouping, and its rows already say which host a remote session lives on.
        </li>
      </ul>
    </section>
  );
}

function MockupsPage(): JSX.Element {
  useEffect(() => {
    // Wire the navigation store the way AppShell's connection flow does
    // (notifications/index.ts calls initNavigation on ready): the store
    // gates every read on a navigation capability, and both the spawn pane's
    // host list and RailRow's host-label lookups read the manifest through
    // it. The capability matches shellguard's scripted shape (v1 capability,
    // v2 reads).
    initNavigation(fake, { version: 1, readVersions: [2], generationId: GENERATION, sequence: 0 });
    void navigationStore
      .getState()
      .loadManifest()
      .catch(() => {});
  }, []);
  return (
    <div className={styles.page}>
      <header className={styles.section}>
        <h1 className={styles.pageTitle}>Host above the directory + sidebar organize-by</h1>
        <p className={styles.intro}>
          Mockups for two proposed changes, rendered with the production components, widgets, and CSS modules against a
          scripted client — canned data, not a live hub. Start stops at the thread/start boundary on purpose.
        </p>
      </header>
      <SpawnSection />
      <RailSection />
      <ControlCloseups />
      <NotesSection />
    </div>
  );
}

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("mockups.html is missing #root");
// Mockup state hygiene, gated to the mockup server's own origin: this page
// is ALSO served by the app's plain dev server (same vite config, so the
// same origin as the real app), and there the shared localStorage may hold
// a real browser's spawn defaults - a mockup visit must not wipe or rewrite
// them. On the dedicated mockup port the origin is the mockup's own, so the
// wipe and seeding keep every reload of the review artifact deterministic:
// stale spawn-defaults from earlier mockup visits clear, and the global
// working dir + model seed the way a returning user's browser would already
// have them.
if (location.port === "5199") {
  for (const key of Object.keys(localStorage)) {
    if (key.startsWith(SPAWN_DEFAULTS_PREFIX)) localStorage.removeItem(key);
  }
  localStorage.setItem(GLOBAL_WORKING_DIR_KEY, WORLDS.local?.recents[0] ?? "");
  // Same for the model: a returning user has a sticky choice, and without it
  // the pane truthfully reports "no default model configured" — a real state,
  // but not the one this mockup is about.
  localStorage.setItem(GLOBAL_MODEL_KEY, "anthropic/claude-sonnet-4-5");
}
document.body.style.margin = "0";
document.body.style.background = "var(--surface-canvas)";
createRoot(rootEl).render(
  <ClientProvider client={fake}>
    <MockupsPage />
    <Toast />
  </ClientProvider>,
);
