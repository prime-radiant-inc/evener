// Settings destination metadata shared by navigation, content and pane titles.

export type SettingsClusterId = "preferences" | "agents-models" | "extensions" | "system";

export interface SettingsSection {
  id: string;
  label: string;
  searchTerms: readonly string[];
  cluster: SettingsClusterId;
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
  {
    id: "theme",
    searchTerms: ["theme", "font", "color", "density", "text size", "transcript width", "conversation width"],
    label: "Appearance",
    cluster: "preferences",
  },
  {
    id: "transcript",
    searchTerms: ["transcript", "tokens", "cost", "thinking", "tool calls"],
    label: "Conversation display",
    cluster: "preferences",
  },
  {
    id: "keybindings",
    searchTerms: ["composer", "enter", "send", "keyboard", "keybindings"],
    label: "Input & shortcuts",
    cluster: "preferences",
  },
  {
    id: "notifications",
    searchTerms: ["sound", "audio", "alerts", "favicon"],
    label: "Notifications",
    cluster: "preferences",
  },
  // --- Agents & models -----------------------------------------------
  {
    id: "credentials",
    searchTerms: ["API key", "provider"],
    label: "Providers & credentials",
    cluster: "agents-models",
  },
  { id: "agents", searchTerms: ["model", "defaults"], label: "Agents", cluster: "agents-models" },
  { id: "agents-md", searchTerms: ["instructions"], label: "AGENTS.md", cluster: "agents-models" },
  { id: "launch-evener", searchTerms: ["launch options"], label: "Evener launch", cluster: "agents-models" },
  { id: "inrepo", searchTerms: ["repository configuration"], label: "In-repo config", cluster: "agents-models" },
  // --- Extensions ------------------------------------------------------
  {
    id: "plugins-manager",
    searchTerms: ["marketplace", "plugin directory", "plugin directories", "sources", "installed", "browse"],
    label: "Plugins",
    cluster: "extensions",
  },
  { id: "skills", searchTerms: ["skill directory"], label: "Skill directories", cluster: "extensions" },
  { id: "mcp", searchTerms: ["tools", "server"], label: "MCP servers", cluster: "extensions" },
  { id: "hosts", searchTerms: ["remote", "connections"], label: "Hosts", cluster: "system" },
  { id: "mobile", searchTerms: ["pairing", "phone", "device", "mobile"], label: "Devices", cluster: "system" },
  {
    id: "hub",
    searchTerms: ["runtime", "listen", "token", "workers"],
    label: "Hub",
    cluster: "system",
  },
  { id: "storage", searchTerms: ["cache", "index", "state directory"], label: "Storage", cluster: "system" },
  { id: "about", searchTerms: ["version", "credits", "license"], label: "About", cluster: "system" },
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

export function matchesSettingsSection(section: SettingsSection, query: string): boolean {
  const needle = query.trim().toLowerCase();
  return (
    section.label.toLowerCase().includes(needle) ||
    section.searchTerms.some((term) => term.toLowerCase().includes(needle))
  );
}
