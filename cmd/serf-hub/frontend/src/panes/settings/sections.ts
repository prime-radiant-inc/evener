// The settings nav's section inventory - single source of truth for
// SettingsNav's link list/grouping, the pane's own title() (paneRegistry),
// and (via DEFAULT_SECTION_ID) what a bare /settings resolves to. Verified
// against templates/partials/settings.html:13-31 (16 exact - "16 nav
// sections" per the wave-7 plan's own Goal line): 5 ungrouped top links
// (General/Theme/Transcript/Display/Notifications) plus 3 labeled clusters
// ("Agents & models"/"Extensions"/"Daemon"), in this fixed order. The
// per-project override (?cwd=) and the /credentials alias are
// deliberately NOT rows here - per-project is a standalone page reached
// via a project's own gear icon (no settings-nav entry at all in the
// legacy either), and /credentials resolves to the "credentials" row
// below via routing.ts's own urlToPane alias, not a second nav entry.

export type SettingsClusterId = "agents-models" | "extensions" | "daemon";

export interface SettingsSection {
  id: string;
  label: string;
  /** Omitted for the 5 ungrouped top links. */
  cluster?: SettingsClusterId;
}

export interface SettingsCluster {
  id: SettingsClusterId;
  label: string;
}

export const SETTINGS_CLUSTERS: SettingsCluster[] = [
  { id: "agents-models", label: "Agents & models" },
  { id: "extensions", label: "Extensions" },
  { id: "daemon", label: "Daemon" },
];

export const SETTINGS_SECTIONS: SettingsSection[] = [
  // --- ungrouped ---------------------------------------------------------
  { id: "general", label: "General" },
  { id: "theme", label: "Theme" },
  { id: "transcript", label: "Transcript" },
  { id: "display", label: "Display" },
  { id: "notifications", label: "Notifications" },
  // --- Agents & models -----------------------------------------------
  { id: "credentials", label: "Providers & credentials", cluster: "agents-models" },
  { id: "agents", label: "Agents", cluster: "agents-models" },
  { id: "launch-serf", label: "Serf launch", cluster: "agents-models" },
  { id: "launch-codex", label: "Codex launch", cluster: "agents-models" },
  { id: "inrepo", label: "In-repo config", cluster: "agents-models" },
  // --- Extensions ------------------------------------------------------
  { id: "plugins-manager", label: "Marketplaces & Plugins", cluster: "extensions" },
  { id: "plugins", label: "Plugins", cluster: "extensions" },
  { id: "skills", label: "Skills", cluster: "extensions" },
  { id: "mcp", label: "MCP servers", cluster: "extensions" },
  // --- Daemon ------------------------------------------------------------
  { id: "hub", label: "Hub", cluster: "daemon" },
  { id: "storage", label: "Storage", cluster: "daemon" },
];

// What a bare /settings (no :section) resolves to - matches the legacy
// sidebar's own global "Settings" entry point, which hx-gets
// /_partials/settings/general (test-sidebar-header.js).
export const DEFAULT_SECTION_ID = "general";

export function settingsSectionLabel(id: string): string {
  return SETTINGS_SECTIONS.find((s) => s.id === id)?.label ?? id;
}

export function isKnownSettingsSection(id: string): boolean {
  return SETTINGS_SECTIONS.some((s) => s.id === id);
}
