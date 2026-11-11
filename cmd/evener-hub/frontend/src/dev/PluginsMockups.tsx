// /dev/plugins-mockups: design mockups for the plugins UI unification -
// settings (Installed + Browse), the plugin detail sheet, and the session
// launcher's plugin list. Everything renders from the REAL widgets and the
// REAL section styles with fixture data, so what you react to here is what
// the implementation would look like. Nothing is live and nothing persists:
// every action is inert except reopening a sheet or expanding a contents
// list. Dev build only - see App.tsx, which gates this route (and this
// module's import) behind import.meta.env.DEV.
//
// Fixture provenance: installed plugin names, versions, descriptions,
// statuses and component inventories were enumerated from this machine's
// real install set on 2026-11-08; catalog entries come from the real
// marketplace indexes. Contents shown for NOT-YET-INSTALLED plugins are
// illustrative - that read does not exist yet (open question 2).

import { type ReactElement, type ReactNode, useState } from "react";
import { CatalogPluginRow as SharedCatalogPluginRow } from "../panes/plugins/PluginRow";
import mpStyles from "../panes/settings/sections/marketplacesPlugins/marketplacesPlugins.module.css";
import psStyles from "../panes/spawn/pluginSelection.module.css";
import {
  Badge,
  Button,
  Card,
  Chip,
  Disclosure,
  EmptyState,
  Eyebrow,
  InlineNotice,
  Input,
  ListRow,
  PropertyList,
  type PropertyListRow,
  SegmentedControl,
  type SegmentedControlOption,
  Sheet,
  StatusDot,
  Switch,
} from "../widgets";
import dialogStyles from "../widgets/dialog/dialog.module.css";
import sheetStyles from "../widgets/sheet/sheet.module.css";
import styles from "./plugins-mockups.module.css";

type SheetMode = "installed" | "catalog" | "launcher";

interface MockContents {
  skills: string[];
  agents: string[];
  commands: string[];
  hooks: string[];
  mcpServers: string[];
}

interface MockPlugin {
  name: string;
  marketplace: string;
  /** "" when the catalog does not carry one (pre-install). */
  version: string;
  description: string;
  installed: boolean;
  enabled: boolean;
  autoUpgrade: boolean;
  broken: boolean;
  updateAvailable: boolean;
  source: string;
  author?: string;
  category?: string;
  /** Launcher rows only: whether this launch includes the plugin. */
  selected?: boolean;
  contents: MockContents;
}

const SUPERPOWERS_CHROME: MockPlugin = {
  name: "superpowers-chrome",
  marketplace: "superpowers-marketplace",
  version: "3.3.0",
  description:
    "BETA: VERY LIGHTLY TESTED - Direct Chrome DevTools Protocol access via 'browsing' skill. Skill mode (17 CLI commands) plus an MCP server for tools-style clients.",
  installed: true,
  enabled: true,
  autoUpgrade: true,
  broken: false,
  updateAvailable: false,
  source: "github.com/obra/superpowers-marketplace",
  contents: { skills: ["browsing"], agents: ["browser-user"], commands: [], hooks: [], mcpServers: ["chrome"] },
};

const PRIMERADIANT_OPS: MockPlugin = {
  name: "primeradiant-ops",
  marketplace: "primeradiant",
  version: "2.9.1",
  description: "Prime Radiant AWS operations and infrastructure management",
  installed: true,
  enabled: true,
  autoUpgrade: true,
  broken: false,
  updateAvailable: false,
  source: "github.com/prime-radiant-inc/cc-plugin-primeradiant-ops",
  contents: {
    skills: [
      "access-and-secrets",
      "aws-infrastructure",
      "eod-summary",
      "gws-workspace",
      "htmx",
      "implementing-features",
      "interviewer",
      "linear-ticket-lifecycle",
      "local-dev",
      "maintaining-project-map",
      "observability",
      "par",
      "production-engineering",
      "prompt-mining",
      "provisioning-coworker-accounts",
      "quorum-appliance-remote-run",
      "release-engineering",
      "sen",
      "sen-cli",
      "session-sync",
      "shepherd-pr",
      "tailscale",
      "terminus",
      "terraform",
      "using-git-spice",
      "using-slack-at-prime-radiant",
    ],
    agents: [],
    commands: [],
    hooks: [],
    mcpServers: [],
  },
};

const DESIGN: MockPlugin = {
  name: "design",
  marketplace: "knowledge-work-plugins",
  version: "1.2.0",
  description:
    "Accelerate design workflows - critique, design system management, UX writing, accessibility audits, research synthesis",
  installed: true,
  enabled: false,
  autoUpgrade: true,
  broken: false,
  updateAvailable: false,
  source: "github.com/anthropics/knowledge-work-plugins",
  contents: {
    skills: [
      "accessibility-review",
      "design-critique",
      "design-handoff",
      "design-system",
      "research-synthesis",
      "user-research",
      "ux-copy",
    ],
    agents: [],
    commands: [],
    hooks: [],
    mcpServers: ["asana", "atlassian", "figma", "gmail", "google calendar", "intercom", "linear", "notion", "slack"],
  },
};

const CLAUDE_SESSION_DRIVER: MockPlugin = {
  name: "claude-session-driver",
  marketplace: "superpowers-marketplace",
  version: "4.0.1",
  description: "Launch, control, and monitor other Claude Code sessions as workers via tmux",
  installed: true,
  enabled: false,
  autoUpgrade: true,
  broken: false,
  updateAvailable: true,
  source: "github.com/obra/superpowers-marketplace",
  contents: { skills: ["driving-claude-code-sessions"], agents: [], commands: [], hooks: [], mcpServers: [] },
};

const ELEMENTS_OF_STYLE: MockPlugin = {
  name: "elements-of-style",
  marketplace: "superpowers-marketplace",
  version: "1.0.0",
  description: "Writing guidance based on William Strunk Jr.'s The Elements of Style (1918)",
  installed: true,
  enabled: true,
  autoUpgrade: false,
  broken: false,
  updateAvailable: false,
  source: "github.com/obra/superpowers-marketplace",
  contents: { skills: ["writing-clearly-and-concisely"], agents: [], commands: [], hooks: [], mcpServers: [] },
};

const SUPERPOWERS: MockPlugin = {
  name: "superpowers",
  marketplace: "superpowers-marketplace",
  version: "7.0.2",
  description: "Core skills library for Claude Code: TDD, debugging, collaboration patterns, and proven techniques",
  installed: true,
  enabled: true,
  autoUpgrade: true,
  broken: false,
  updateAvailable: false,
  source: "github.com/obra/superpowers-marketplace",
  contents: {
    skills: [
      "brainstorming",
      "diagnosing-superpowers",
      "dispatching-parallel-agents",
      "executing-plans",
      "finishing-a-development-branch",
      "receiving-code-review",
      "requesting-code-review",
      "subagent-driven-development",
      "systematic-debugging",
      "test-driven-development",
      "using-git-worktrees",
      "using-superpowers",
      "verification-before-completion",
      "writing-plans",
      "writing-skills",
    ],
    agents: [],
    commands: [],
    hooks: [],
    mcpServers: [],
  },
};

const ITERATIVE_DEVELOPMENT: MockPlugin = {
  name: "iterative-development",
  marketplace: "prime-radiant-marketplace",
  version: "0.1.0",
  description:
    "An iterative implementation methodology that pairs with superpowers. Extracts requirements with proof obligations, runs red-green iterations, audits progress.",
  installed: true,
  enabled: true,
  autoUpgrade: false,
  broken: false,
  updateAvailable: false,
  source: "github.com/prime-radiant-inc/prime-radiant-marketplace",
  contents: {
    skills: [
      "auditing-progress",
      "extracting-requirements",
      "implementing-tasks",
      "iterative-development",
      "running-an-iteration",
      "scoping-the-simplest-core",
    ],
    agents: [],
    commands: [],
    hooks: [],
    mcpServers: [],
  },
};

const EPISODIC_MEMORY: MockPlugin = {
  name: "episodic-memory",
  marketplace: "superpowers-marketplace",
  version: "",
  description:
    "Semantic search for Claude Code and Codex conversations. Remember past discussions, decisions, and codebases across sessions.",
  installed: false,
  enabled: false,
  autoUpgrade: false,
  broken: false,
  updateAvailable: false,
  source: "github.com/obra/superpowers-marketplace",
  contents: {
    skills: ["conversation-search", "memory-recall"],
    agents: [],
    commands: [],
    hooks: [],
    mcpServers: ["episodic-memory"],
  },
};

const DOUBLE_SHOT_LATTE: MockPlugin = {
  name: "double-shot-latte",
  marketplace: "superpowers-marketplace",
  version: "",
  description:
    "Stop 'Would you like me to continue?' interruptions. Automatically evaluates whether a continuation is wanted and just keeps going.",
  installed: false,
  enabled: false,
  autoUpgrade: false,
  broken: false,
  updateAvailable: false,
  source: "github.com/obra/superpowers-marketplace",
  contents: { skills: ["continuation-policy"], agents: [], commands: [], hooks: [], mcpServers: [] },
};

const GREENFIELD: MockPlugin = {
  name: "greenfield",
  marketplace: "prime-radiant-marketplace",
  version: "",
  description: "Reverse engineer clean behavioral specs from any codebase.",
  installed: false,
  enabled: false,
  autoUpgrade: false,
  broken: false,
  updateAvailable: false,
  source: "github.com/prime-radiant-inc/prime-radiant-marketplace",
  contents: { skills: ["greenfield"], agents: [], commands: [], hooks: [], mcpServers: [] },
};

const TOOLSMITH: MockPlugin = {
  name: "toolsmith",
  marketplace: "prime-radiant-marketplace",
  version: "",
  description:
    "Stops agents from writing throwaway helper scripts: agents check a shared tool chest of proven scripts first.",
  installed: false,
  enabled: false,
  autoUpgrade: false,
  broken: false,
  updateAvailable: false,
  source: "github.com/prime-radiant-inc/prime-radiant-marketplace",
  contents: { skills: ["toolsmith"], agents: [], commands: [], hooks: [], mcpServers: [] },
};

const ATLASSIAN: MockPlugin = {
  name: "atlassian",
  marketplace: "claude-plugins-official",
  version: "",
  description:
    "Connect to Atlassian products including Jira and Confluence. Search and create issues, access data, and automate workflows.",
  installed: false,
  enabled: false,
  autoUpgrade: false,
  broken: false,
  updateAvailable: false,
  source: "github.com/anthropics/claude-plugins-official",
  author: "Atlassian",
  category: "productivity",
  contents: { skills: [], agents: [], commands: [], hooks: [], mcpServers: ["jira", "confluence"] },
};

const INSTALLED: MockPlugin[] = [
  SUPERPOWERS_CHROME,
  SUPERPOWERS,
  PRIMERADIANT_OPS,
  DESIGN,
  ITERATIVE_DEVELOPMENT,
  ELEMENTS_OF_STYLE,
  CLAUDE_SESSION_DRIVER,
];

const LAUNCHER: MockPlugin[] = [
  { ...SUPERPOWERS, selected: true },
  { ...SUPERPOWERS_CHROME, selected: true },
  { ...DESIGN, selected: true },
  { ...CLAUDE_SESSION_DRIVER, selected: false },
];

const BROWSE_TREES: { marketplace: string; plugins: MockPlugin[] }[] = [
  {
    marketplace: "superpowers-marketplace",
    plugins: [SUPERPOWERS_CHROME, EPISODIC_MEMORY, DOUBLE_SHOT_LATTE, ELEMENTS_OF_STYLE],
  },
  {
    marketplace: "prime-radiant-marketplace",
    plugins: [ITERATIVE_DEVELOPMENT, GREENFIELD, TOOLSMITH],
  },
  {
    marketplace: "claude-plugins-official",
    plugins: [ATLASSIAN],
  },
];

// The shared vocabulary: counts in the launcher's own pluralized form.
function countsSummary(contents: MockContents): string {
  const parts: string[] = [];
  const add = (n: number, word: string) => {
    if (n > 0) parts.push(`${n} ${word}${n === 1 ? "" : "s"}`);
  };
  add(contents.skills.length, "skill");
  add(contents.agents.length, "agent");
  add(contents.commands.length, "command");
  add(contents.hooks.length, "hook");
  add(contents.mcpServers.length, "MCP server");
  return parts.length > 0 ? parts.join(" · ") : "no components";
}

// The status chips a row/sheet carries - same vocabulary and tones as
// pluginStatusChips (broken / off by default / auto-upgrade / update available).
function statusChips(plugin: MockPlugin): ReactElement[] {
  const chips: ReactElement[] = [];
  if (plugin.broken)
    chips.push(
      <Chip key="broken" tone="danger" kind="broken">
        broken
      </Chip>,
    );
  if (plugin.installed && !plugin.enabled)
    chips.push(
      <Chip key="disabled" tone="neutral" kind="disabled">
        off by default
      </Chip>,
    );
  if (plugin.installed && plugin.autoUpgrade)
    chips.push(
      <Chip key="auto" tone="neutral" kind="auto-upgrade">
        auto-upgrade
      </Chip>,
    );
  if (plugin.updateAvailable)
    chips.push(
      <Chip key="update" tone="attention" kind="update-available">
        update available
      </Chip>,
    );
  return chips;
}

function dotState(plugin: MockPlugin): "idle" | "failed" | "ended" {
  if (plugin.broken) return "failed";
  if (!plugin.enabled) return "ended";
  return "idle";
}

// ---------------------------------------------------------------------------
// The proposed shared row: one ListRow composition rendered by every plugin
// surface. Context supplies the leading dot (installed only), the chips
// (status, or "installed" on a catalog row), and the meta line.
// ---------------------------------------------------------------------------

function InstalledPluginRow({ plugin }: { plugin: MockPlugin }) {
  return (
    <ListRow
      leading={<StatusDot state={dotState(plugin)} />}
      label={plugin.name}
      chips={statusChips(plugin).length > 0 ? statusChips(plugin) : undefined}
      meta={`@ ${plugin.marketplace} · v${plugin.version || "unknown"} · ${countsSummary(plugin.contents)}`}
      onSelect={() => {}}
    />
  );
}

function CatalogPluginRow({ plugin }: { plugin: MockPlugin }) {
  return (
    <SharedCatalogPluginRow
      plugin={plugin}
      installed={plugin.installed}
      installDisabled={false}
      onOpen={() => {}}
      onInstall={() => {}}
    />
  );
}

function LauncherSwitchRow({ plugin }: { plugin: MockPlugin }) {
  return (
    <Switch
      checked={plugin.selected === true}
      label={plugin.name}
      onChange={() => {}}
      description={
        <span className={psStyles.metadata}>
          <span className={psStyles.source}>{`installed from ${plugin.marketplace}`}</span>
          <span className={psStyles.counts}>{countsSummary(plugin.contents)}</span>
          <span className={psStyles.description}>{plugin.description}</span>
          <Button variant="link" size="xs" onClick={() => {}}>
            Details
          </Button>
        </span>
      }
    />
  );
}

// ---------------------------------------------------------------------------
// The proposed shared detail sheet body: identity, description, CONTENTS
// (the new part), metadata, then contextual switches. The footer and the
// metadata rows are supplied by the mode.
// ---------------------------------------------------------------------------

const CONTENT_KINDS: [keyof MockContents, string][] = [
  ["skills", "Skills"],
  ["agents", "Agents"],
  ["commands", "Commands"],
  ["hooks", "Hooks"],
  ["mcpServers", "MCP servers"],
];

function ContentsRow({ label, names }: { label: string; names: string[] }) {
  const [showAll, setShowAll] = useState(false);
  const visible = showAll ? names : names.slice(0, 8);
  const rest = names.length - visible.length;
  return (
    <div className={styles.contentsRow}>
      <span className={styles.kindLabel}>{label}</span>
      <Badge count={names.length} />
      <span className={styles.names}>
        {visible.join(", ")}
        {rest > 0 && <Button variant="link" size="sm" onClick={() => setShowAll(true)}>{` +${rest} more`}</Button>}
      </span>
    </div>
  );
}

function DetailBody({ plugin, mode, diagnostics }: { plugin: MockPlugin; mode: SheetMode; diagnostics?: ReactNode }) {
  const chips = mode !== "catalog" || plugin.installed ? statusChips(plugin) : [];
  const present = CONTENT_KINDS.filter(([key]) => plugin.contents[key].length > 0);
  const metaRows: PropertyListRow[] = [];
  if (mode !== "catalog") metaRows.push({ label: "Version", value: `v${plugin.version || "unknown"}`, mono: true });
  metaRows.push({ label: "Marketplace", value: plugin.marketplace });
  metaRows.push({ label: "Source", value: plugin.source, mono: true });
  if (mode === "catalog" && plugin.author !== undefined) metaRows.push({ label: "Author", value: plugin.author });
  return (
    <>
      {chips.length > 0 && <div className={mpStyles.chipRow}>{chips}</div>}
      <p className={mpStyles.sheetDesc}>{plugin.description}</p>
      {mode !== "catalog" && (
        <div className={styles.contents}>
          <Eyebrow as="h3">Contents</Eyebrow>
          {present.length === 0 ? (
            <p className={styles.noContents}>Manifest only - no skills, agents, commands, hooks, or MCP servers.</p>
          ) : (
            <div className={styles.contentsList}>
              {present.map(([key, label]) => (
                <ContentsRow key={key} label={label} names={plugin.contents[key]} />
              ))}
            </div>
          )}
          {diagnostics}
        </div>
      )}
      <div className={mpStyles.metaList}>
        <PropertyList rows={metaRows} />
      </div>
      {mode === "installed" && (
        <div className={mpStyles.switchRows}>
          <Switch
            checked={plugin.enabled}
            onChange={() => {}}
            label="Enabled by default"
            description="Used by new sessions with the default plugin selection. Custom launches and existing sessions keep their own selections."
          />
          <Switch checked={plugin.autoUpgrade} onChange={() => {}} label="Auto-upgrade" />
        </div>
      )}
      {mode === "launcher" && (
        <div className={mpStyles.switchRows}>
          <Switch
            checked={plugin.selected === true}
            onChange={() => {}}
            label="Included in this session"
            description="Turn off to start this session without this plugin. Only this launch's plugin selection changes."
          />
        </div>
      )}
    </>
  );
}

function detailFooter(plugin: MockPlugin, mode: SheetMode): ReactNode {
  if (mode === "installed") {
    return (
      <>
        {plugin.updateAvailable && (
          <Button variant="primary" onClick={() => {}}>
            Upgrade
          </Button>
        )}
        <Button variant="danger" onClick={() => {}}>
          Remove
        </Button>
      </>
    );
  }
  if (mode === "catalog") {
    return (
      <Button variant="primary" onClick={() => {}}>
        Install
      </Button>
    );
  }
  return undefined;
}

// The real Sheet is position:fixed, which headless screenshot tooling cannot
// rasterize (the whole page captures as the bare background). The default
// render is a static stand-in composed from the SAME dialog/sheet classes -
// .panel, .standard, .header, .title, .body, .footer - minus the
// fixed-position geometry, so a capture shows exactly what the sheet body
// looks like. Flip "Live sheets" on to interact with the real Sheet widget
// (scrim, slide-in, Escape) in a browser.
function StaticSheet({ plugin, mode, diagnostics }: { plugin: MockPlugin; mode: SheetMode; diagnostics?: ReactNode }) {
  const footer = detailFooter(plugin, mode);
  return (
    <div className={styles.staticWrap}>
      <div className={`${dialogStyles.panel} ${sheetStyles.standard} ${styles.staticSheet}`}>
        <header className={dialogStyles.header}>
          <h2 className={dialogStyles.title}>{plugin.name}</h2>
        </header>
        <div className={dialogStyles.body}>
          <DetailBody plugin={plugin} mode={mode} diagnostics={diagnostics} />
        </div>
        {footer !== undefined && <div className={dialogStyles.footer}>{footer}</div>}
      </div>
    </div>
  );
}

function LiveSheetFrame({
  plugin,
  mode,
  diagnostics,
}: {
  plugin: MockPlugin;
  mode: SheetMode;
  diagnostics?: ReactNode;
}) {
  const [open, setOpen] = useState(true);
  return (
    <div className={styles.sheetFrame}>
      {!open && (
        <div className={styles.reopen}>
          <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
            Reopen sheet
          </Button>
        </div>
      )}
      <Sheet
        open={open}
        onClose={() => setOpen(false)}
        title={plugin.name}
        side="right"
        footer={detailFooter(plugin, mode)}
      >
        <DetailBody plugin={plugin} mode={mode} diagnostics={diagnostics} />
      </Sheet>
    </div>
  );
}

function SheetFrame({
  plugin,
  mode,
  label,
  note,
  diagnostics,
  live,
}: {
  plugin: MockPlugin;
  mode: SheetMode;
  label: string;
  note?: string;
  diagnostics?: ReactNode;
  live: boolean;
}) {
  return (
    <div className={styles.frameCell}>
      <p className={styles.frameLabel}>{label}</p>
      {live ? (
        <LiveSheetFrame plugin={plugin} mode={mode} diagnostics={diagnostics} />
      ) : (
        <StaticSheet plugin={plugin} mode={mode} diagnostics={diagnostics} />
      )}
      {note !== undefined && <p className={styles.frameNote}>{note}</p>}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Section 1: today, for side-by-side comparison.
// ---------------------------------------------------------------------------

function TodayBrowseNode() {
  const [open, setOpen] = useState(true);
  const rows = [SUPERPOWERS_CHROME, EPISODIC_MEMORY, DOUBLE_SHOT_LATTE];
  return (
    <Disclosure open={open} onOpenChange={setOpen} summary={`superpowers-marketplace (${rows.length})`}>
      <ul aria-label="superpowers-marketplace plugins" className={mpStyles.treeChildren}>
        {rows.map((p) => (
          <li className={mpStyles.row} key={p.name}>
            <div className={mpStyles.rowMain}>
              <div className={mpStyles.rowText}>{p.name}</div>
              <div className={mpStyles.rowMeta}>{p.description}</div>
            </div>
            <div className={mpStyles.rowActions}>
              {p.installed ? (
                <Chip kind="installed">Installed</Chip>
              ) : (
                <Button size="sm" onClick={() => {}}>
                  Install
                </Button>
              )}
            </div>
          </li>
        ))}
      </ul>
    </Disclosure>
  );
}

function LauncherCard({ renderRow }: { renderRow: (plugin: MockPlugin) => ReactNode }) {
  const launcherSelected = LAUNCHER.filter((p) => p.selected).length;
  return (
    <Card variant="outlined" role="region" aria-label="Plugins for this session">
      <div className={psStyles.content}>
        <div className={psStyles.tools}>
          <div className={psStyles.resultCount} role="status">
            {launcherSelected} of {LAUNCHER.length} selected
          </div>
          <div className={psStyles.actions}>
            <Button variant="quiet" size="xs" type="button" aria-label="All" onClick={() => {}}>
              All
            </Button>
            <Button variant="quiet" size="xs" type="button" aria-label="None" onClick={() => {}}>
              None
            </Button>
          </div>
        </div>
        <div className={psStyles.list}>
          {LAUNCHER.map((p) => (
            <div className={psStyles.row} key={p.name}>
              {renderRow(p)}
            </div>
          ))}
        </div>
      </div>
    </Card>
  );
}

function TodaySection() {
  return (
    <section id="today" className={styles.section}>
      <h2 className={styles.sectionTitle}>1 · Today</h2>
      <p className={styles.sectionNote}>
        The current screens, drawn with their real markup and this page&apos;s fixture data: three different row shapes
        for the same thing, and no surface shows what a plugin contains.
      </p>
      <div className={styles.columns}>
        <div className={styles.column}>
          <Eyebrow as="h3">Installed (settings)</Eyebrow>
          <ul aria-label="Installed plugins today" className={mpStyles.list}>
            {INSTALLED.slice(0, 4).map((p) => (
              <li key={p.name}>
                <ListRow
                  leading={<StatusDot state={dotState(p)} />}
                  label={p.name}
                  chips={statusChips(p).length > 0 ? statusChips(p) : undefined}
                  meta={`@ ${p.marketplace} · v${p.version}`}
                  onSelect={() => {}}
                />
              </li>
            ))}
          </ul>
        </div>
        <div className={styles.column}>
          <Eyebrow as="h3">Browse (settings)</Eyebrow>
          <ul aria-label="Marketplace browse tree today" className={mpStyles.tree}>
            <li className={mpStyles.treeNode}>
              <TodayBrowseNode />
            </li>
          </ul>
        </div>
        <div className={styles.column}>
          <Eyebrow as="h3">Session launcher (spawn)</Eyebrow>
          <LauncherCard renderRow={(plugin) => <LauncherSwitchRow plugin={plugin} />} />
        </div>
      </div>
    </section>
  );
}

// ---------------------------------------------------------------------------
// Section 2: the settings screens on the shared row. The segmented control
// and the installed filter actually work.
// ---------------------------------------------------------------------------

const SEGMENT_OPTIONS: SegmentedControlOption[] = [
  { value: "installed", label: "Installed (7)" },
  { value: "browse", label: "Browse" },
  { value: "sources", label: "Sources (7)" },
];

function BrowseTreeNode({ marketplace, plugins }: { marketplace: string; plugins: MockPlugin[] }) {
  const [open, setOpen] = useState(true);
  return (
    <li className={mpStyles.treeNode}>
      <Disclosure open={open} onOpenChange={setOpen} summary={marketplace}>
        <ul aria-label={`${marketplace} plugins`} className={mpStyles.treeChildren}>
          {plugins.map((p) => (
            <li key={p.name}>
              <CatalogPluginRow plugin={p} />
            </li>
          ))}
        </ul>
      </Disclosure>
    </li>
  );
}

function SettingsDemo() {
  const [segment, setSegment] = useState("installed");
  const [filter, setFilter] = useState("");
  const query = filter.trim().toLowerCase();
  const visible =
    query === ""
      ? INSTALLED
      : INSTALLED.filter((p) => p.name.toLowerCase().includes(query) || p.marketplace.includes(query));
  return (
    <section className={mpStyles.page}>
      <SegmentedControl
        label="View"
        value={segment}
        onChange={(value) => setSegment(value)}
        options={SEGMENT_OPTIONS}
        fullWidth
      />
      <p className={mpStyles.help}>
        Installation makes a plugin available on this host. Enabled defaults apply to new sessions using the default
        plugin selection; custom launches choose their own plugins. Existing sessions keep their selected plugins.
      </p>
      {segment === "installed" ? (
        <section className={mpStyles.section}>
          <div className={mpStyles.filterRow}>
            <Input
              aria-label="Filter installed"
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
              placeholder="Filter installed…"
            />
          </div>
          <ul aria-label="Installed plugins" className={mpStyles.list}>
            {visible.map((p) => (
              <li key={p.name}>
                <InstalledPluginRow plugin={p} />
              </li>
            ))}
            {visible.length === 0 && (
              <li>
                <EmptyState size="compact" title={`No plugins match "${filter.trim()}".`} />
              </li>
            )}
          </ul>
        </section>
      ) : (
        <section className={mpStyles.section}>
          <div className={mpStyles.filterRow}>
            <Input aria-label="Filter plugins" value="" onChange={() => {}} placeholder="Filter plugins…" />
          </div>
          <ul aria-label="Marketplace browse tree" className={mpStyles.tree}>
            {BROWSE_TREES.map((node) => (
              <BrowseTreeNode key={node.marketplace} marketplace={node.marketplace} plugins={node.plugins} />
            ))}
          </ul>
        </section>
      )}
    </section>
  );
}

function SharedRowSection() {
  return (
    <section id="rows" className={styles.section}>
      <h2 className={styles.sectionTitle}>2 · One row, every list</h2>
      <p className={styles.sectionNote}>
        Installed and to-install render from one row composition. Installed rows carry the status dot, status chips, and
        counts in meta (decision 3). Browse rows carry the description and an inline Install button, and still open the
        detail sheet (decision 1: both). Browse rows use the shipped shared plugin row and ListRow trailing-control
        composition. Upgrade, Remove, Enabled and Auto-upgrade live in the sheet. The filter and the segments work on
        this page.
      </p>
      <SettingsDemo />
    </section>
  );
}

// ---------------------------------------------------------------------------
// Section 3: the expanded view.
// ---------------------------------------------------------------------------

function SheetSection({ live }: { live: boolean }) {
  return (
    <section id="sheets" className={styles.section}>
      <h2 className={styles.sectionTitle}>3 · The expanded view: contents</h2>
      <p className={styles.sectionNote}>
        The same detail sheet for every plugin, with a Contents block between the description and the metadata: one row
        per component kind the plugin actually ships, a count badge, and the loaded names. Switches and footer actions
        stay contextual. On a phone the sheet slides from the bottom, as today.
      </p>
      <div className={styles.frameGrid}>
        <SheetFrame
          live={live}
          plugin={SUPERPOWERS_CHROME}
          mode="installed"
          label="Installed - every component kind"
          note="Auto-upgrade chip because the plugin auto-upgrades. Skills, agents, and MCP servers all present."
        />
        <SheetFrame
          live={live}
          plugin={PRIMERADIANT_OPS}
          mode="installed"
          label="Installed - long contents list"
          note="26 skills: the first eight names render inline, the +18 link expands the rest. MCP-heavy plugins behave the same for their servers."
        />
        <SheetFrame
          live={live}
          plugin={DESIGN}
          mode="installed"
          label="Installed - off by default"
          note="Status chips carry over verbatim (off by default, auto-upgrade). 9 MCP servers from the plugin's .mcp.json."
        />
        <SheetFrame
          live={live}
          plugin={CLAUDE_SESSION_DRIVER}
          mode="installed"
          label="Installed - update available + load diagnostics"
          diagnostics={
            <InlineNotice tone="danger" title="One component failed to load" live="status">
              MCP config failed: parsing .mcp.json: unexpected token (synthetic fixture - real loads surface
              MCPConfigWarnings, skipped components, and unsupported hooks here).
            </InlineNotice>
          }
          note="Upgrade appears in the footer when the hub's update check found one. Load diagnostics (warnings, skipped components) surface under Contents when present."
        />
        <SheetFrame
          live={live}
          plugin={ATLASSIAN}
          mode="catalog"
          label="To-install - catalog mode"
          note="Same sheet, no switches; Install replaces Remove. Contents are deferred (decision 2): the catalog index carries name/description/category/author only, so this sheet shows catalog metadata; the Contents block appears once a plugin is installed."
        />
      </div>
    </section>
  );
}

// ---------------------------------------------------------------------------
// Section 4: the session launcher.
// ---------------------------------------------------------------------------

function LauncherSection({ live }: { live: boolean }) {
  return (
    <section id="launcher" className={styles.section}>
      <h2 className={styles.sectionTitle}>4 · Session launcher</h2>
      <p className={styles.sectionNote}>
        The launcher keeps its Switch rows and its one-tap toggling; the rows now speak the same vocabulary (source,
        counts, description) and a quiet Details action in the description opens the same detail sheet. The variant
        below trades one-tap toggling for a fully unified drill-in row.
      </p>
      <div className={styles.frameGrid}>
        <div className={styles.frameCell}>
          <p className={styles.frameLabel}>Launcher - shared vocabulary, Switch rows (primary)</p>
          <LauncherCard renderRow={(plugin) => <LauncherSwitchRow plugin={plugin} />} />
          <p className={styles.frameNote}>
            Rejected alternative (decision 4): drill-in rows with the toggle in the sheet - one extra tap per toggle:
          </p>
          <ul aria-label="Drill-in launcher variant" className={mpStyles.list}>
            <li>
              <ListRow
                leading={<StatusDot state="idle" />}
                label={PRIMERADIANT_OPS.name}
                meta={`installed from ${PRIMERADIANT_OPS.marketplace} · ${countsSummary(PRIMERADIANT_OPS.contents)}`}
                onSelect={() => {}}
              />
            </li>
          </ul>
        </div>
        <SheetFrame
          live={live}
          plugin={CLAUDE_SESSION_DRIVER}
          mode="launcher"
          label="Launcher mode - the same sheet"
          note="Contents plus one contextual switch: Included in this session. No install, no enable, no remove - those do not apply before a session exists."
        />
      </div>
    </section>
  );
}

// ---------------------------------------------------------------------------
// Section 5: spec notes.
// ---------------------------------------------------------------------------

function SpecSection() {
  return (
    <section id="spec" className={styles.section}>
      <h2 className={styles.sectionTitle}>5 · Spec notes</h2>
      <div className={styles.columns}>
        <div className={styles.column}>
          <Eyebrow as="h3">Data plumbing</Eyebrow>
          <ul className={styles.plainList}>
            <li>
              New hub read, plugin components: for each installed plugin, the loaded names per kind (skills, agents,
              commands, hook events, MCP servers) plus load diagnostics (MCP config warnings, skipped components,
              unsupported/unknown hooks). The loader already produces all of it; only the RPC surface is new.
            </li>
            <li>
              ListRow grows an interactive variant with a trailing control (decision 1): the row becomes a container
              whose main area is the tap target, with the control in the trailing slot - no nested buttons.
            </li>
            <li>
              Shared view model: PluginEntry (installed status), MarketplaceCatalogPlugin (catalog metadata), and
              PluginLaunchCandidate (launcher counts) unify into one plugin view the three surfaces render from.
            </li>
          </ul>
        </div>
        <div className={styles.column}>
          <Eyebrow as="h3">Preserved</Eyebrow>
          <ul className={styles.plainList}>
            <li>Installed / Browse / Sources segments, filters, help text, empty states, retry notices.</li>
            <li>
              Status chip vocabulary: broken, off by default, auto-upgrade, update available, installed. Same tones.
            </li>
            <li>
              Switch semantics and copy (Enabled by default, Auto-upgrade), Upgrade and Remove with their confirm
              dialogs, the install confirm dialog.
            </li>
            <li>Launcher: Switch rows, All/None, counts and descriptions, preview diagnostics, selection state.</li>
            <li>Sheet geometry: right side on desktop, bottom sheet on mobile (useIsMobile).</li>
          </ul>
        </div>
      </div>
      <Eyebrow as="h3">Decisions (Jesse, 2026-11-08)</Eyebrow>
      <ol className={styles.questionList}>
        <li>
          <strong>Install placement: both.</strong> Browse rows keep an inline Install button and open the detail sheet.
          Needs the ListRow extension above; the composed rows in section 2 are the visual.
        </li>
        <li>
          <strong>Pre-install contents: deferred.</strong> To-install sheets show catalog metadata (description, author,
          category, marketplace, source); the Contents block is installed-only for now. The fetch-and-inspect read is
          the natural follow-up when wanted.
        </li>
        <li>
          <strong>Counts in list rows: included.</strong> Installed row meta reads &quot;@ marketplace · v1.2.3 · 7
          skills · 9 MCP servers&quot;. Catalog rows gain counts too if the pre-install read lands later.
        </li>
        <li>
          <strong>Launcher rows: Switch rows, shared vocabulary.</strong> One-tap toggling unchanged, description speaks
          the shared source · counts language, and a quiet Details action at the end of the description opens the same
          detail sheet for contents.
        </li>
      </ol>
      <p className={styles.sectionNote}>
        Phase 2 - merging the Browse and Sources screens into one marketplace surface - is deliberately not mocked here.
        The shared row and sheet are shaped so that step inherits everything.
      </p>
    </section>
  );
}

const SECTIONS = new Set(["today", "rows", "sheets", "launcher", "spec"]);

export default function PluginsMockups() {
  const [liveSheets, setLiveSheets] = useState(false);
  // ?section=<today|rows|sheets|launcher|spec> renders only that section, so
  // a capture (or a review link) starts with it in view - the headless
  // screenshot path cannot rasterize a scrolled page.
  const sectionParam = new URLSearchParams(window.location.search).get("section");
  return (
    <div className={styles.page}>
      <h1 className={styles.pageTitle}>Plugins UI mockups</h1>
      <p className={styles.intro}>
        Mockups for unifying the plugins UI: the settings screens (Installed and Browse), the plugin detail sheet with
        its contents, and the session launcher&apos;s plugin list - all rendered from the real widgets and the real
        section styles. Fixture data comes from this machine&apos;s real install set and marketplace indexes. Nothing is
        live, and nothing you click here persists. Section 5 records the decisions already made on this page.
      </p>
      <Switch
        checked={liveSheets}
        onChange={() => setLiveSheets((v) => !v)}
        label="Live sheets"
        description="Sheet mocks render as static panels by default (headless screenshot tooling cannot rasterize fixed-position overlays). Turn this on to interact with the real Sheet widget instead - same content, plus the scrim, slide-in, and Escape handling."
      />
      {!SECTIONS.has(sectionParam ?? "") && (
        <>
          <TodaySection />
          <SharedRowSection />
          <SheetSection live={liveSheets} />
          <LauncherSection live={liveSheets} />
          <SpecSection />
        </>
      )}
      {sectionParam === "today" && <TodaySection />}
      {sectionParam === "rows" && <SharedRowSection />}
      {sectionParam === "sheets" && <SheetSection live={liveSheets} />}
      {sectionParam === "launcher" && <LauncherSection live={liveSheets} />}
      {sectionParam === "spec" && <SpecSection />}
    </div>
  );
}
