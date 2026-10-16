// Settings destination metadata shared by navigation, content and pane titles.

export type SettingsClusterId = "preferences" | "agents-models" | "extensions" | "system";

export interface SettingsSection {
  id: string;
  label: string;
  /** Omitted for ungrouped top links. */
  cluster?: SettingsClusterId;
}

export interface SettingsCluster {
  id: SettingsClusterId;
  label: string;
}

export const SETTINGS_CLUSTERS: SettingsCluster[] = [
  { id: "preferences", label: "Preferences" },
  { id: "agents-models", label: "Agent setup" },
  { id: "extensions", label: "Extensions" },
  { id: "system", label: "System" },
];

export const SETTINGS_SECTIONS: SettingsSection[] = [
  { id: "theme", label: "Appearance", cluster: "preferences" },
  { id: "transcript", label: "Conversation display", cluster: "preferences" },
  { id: "keybindings", label: "Input & shortcuts", cluster: "preferences" },
  { id: "notifications", label: "Notifications", cluster: "preferences" },
  // --- Agents & models -----------------------------------------------
  { id: "credentials", label: "Providers & credentials", cluster: "agents-models" },
  { id: "agents", label: "Agents", cluster: "agents-models" },
  { id: "agents-md", label: "AGENTS.md", cluster: "agents-models" },
  { id: "launch-evener", label: "Evener launch", cluster: "agents-models" },
  { id: "inrepo", label: "In-repo config", cluster: "agents-models" },
  // --- Extensions ------------------------------------------------------
  { id: "plugins-manager", label: "Plugins", cluster: "extensions" },
  { id: "plugins", label: "Plugin directories", cluster: "extensions" },
  { id: "skills", label: "Skill directories", cluster: "extensions" },
  { id: "mcp", label: "MCP servers", cluster: "extensions" },
  { id: "general", label: "General", cluster: "system" },
  { id: "hosts", label: "Hosts", cluster: "system" },
  { id: "mobile", label: "Devices", cluster: "system" },
  { id: "hub", label: "Hub", cluster: "system" },
  { id: "storage", label: "Storage", cluster: "system" },
  { id: "about", label: "About", cluster: "system" },
];

export const DEFAULT_SECTION_ID = "theme";

export function resolveSettingsSection(section: string | undefined, remembered: string | null): string {
  if (section !== undefined) {
    return section === "project" || isKnownSettingsSection(section) ? section : DEFAULT_SECTION_ID;
  }
  return remembered !== null && isKnownSettingsSection(remembered) ? remembered : DEFAULT_SECTION_ID;
}

export function settingsSectionLabel(id: string): string {
  return SETTINGS_SECTIONS.find((s) => s.id === id)?.label ?? id;
}

export function isKnownSettingsSection(id: string): boolean {
  return SETTINGS_SECTIONS.some((s) => s.id === id);
}
