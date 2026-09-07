// @vitest-environment node
import { expect, test } from "vitest";
import {
  DEFAULT_SECTION_ID,
  isKnownSettingsSection,
  SETTINGS_CLUSTERS,
  SETTINGS_SECTIONS,
  settingsSectionLabel,
} from "./sections";

// Section inventory verified against templates/partials/settings.html:13-31
// (15 exact after Codex launch controls were removed) - see the wave-7
// floor doc's own citation of that range - plus
// three sections with no legacy counterpart, "keybindings", "about", and "mobile" (see
// sections.ts's own doc comment).

test("has exactly 18 sections", () => {
  expect(SETTINGS_SECTIONS).toHaveLength(18);
});

test("every section id is unique", () => {
  const ids = SETTINGS_SECTIONS.map((s) => s.id);
  expect(new Set(ids).size).toBe(ids.length);
});

test("the 6 ungrouped sections are General/Theme/Transcript display/Display/Notifications/Keybindings, in this order, first", () => {
  const ungrouped = SETTINGS_SECTIONS.filter((s) => s.cluster === undefined);
  expect(ungrouped.map((s) => s.label)).toEqual([
    "General",
    "Theme",
    "Transcript display",
    "Display",
    "Notifications",
    "Keybindings",
  ]);
  expect(SETTINGS_SECTIONS.slice(0, 6)).toEqual(ungrouped);
});

test('the "Agent setup" cluster has exactly these 4 sections, in order, right after the ungrouped ones', () => {
  const cluster = SETTINGS_SECTIONS.filter((s) => s.cluster === "agents-models");
  expect(cluster.map((s) => s.label)).toEqual(["Providers & credentials", "Agents", "Evener launch", "In-repo config"]);
  expect(SETTINGS_SECTIONS.slice(6, 10)).toEqual(cluster);
});

test('the "Extensions" cluster has exactly these 4 sections, in order, right after "Agent setup"', () => {
  const cluster = SETTINGS_SECTIONS.filter((s) => s.cluster === "extensions");
  expect(cluster.map((s) => s.label)).toEqual(["Marketplaces & Plugins", "Plugins", "Skills", "MCP servers"]);
  expect(SETTINGS_SECTIONS.slice(10, 14)).toEqual(cluster);
});

test('the "Daemon" cluster has exactly these 4 sections, in order, last', () => {
  const cluster = SETTINGS_SECTIONS.filter((s) => s.cluster === "daemon");
  expect(cluster.map((s) => s.label)).toEqual(["Hub", "Mobile app", "Storage", "About"]);
  expect(SETTINGS_SECTIONS.slice(14, 18)).toEqual(cluster);
});

test('"About" is the very last section overall', () => {
  expect(SETTINGS_SECTIONS[SETTINGS_SECTIONS.length - 1]?.label).toBe("About");
});

test("the credentials section's id is exactly credentials (the /credentials alias resolves to this id)", () => {
  expect(SETTINGS_SECTIONS.find((s) => s.label === "Providers & credentials")?.id).toBe("credentials");
});

test("SETTINGS_CLUSTERS lists the 3 labeled clusters in nav order", () => {
  expect(SETTINGS_CLUSTERS.map((c) => c.label)).toEqual(["Agent setup", "Extensions", "Daemon"]);
});

test("every section's cluster field (when set) references a real cluster id", () => {
  const clusterIds = new Set(SETTINGS_CLUSTERS.map((c) => c.id));
  for (const section of SETTINGS_SECTIONS) {
    if (section.cluster !== undefined) expect(clusterIds.has(section.cluster)).toBe(true);
  }
});

test("DEFAULT_SECTION_ID names a real, ungrouped section", () => {
  const match = SETTINGS_SECTIONS.find((s) => s.id === DEFAULT_SECTION_ID);
  expect(match).toBeDefined();
  expect(match?.cluster).toBeUndefined();
});

test("settingsSectionLabel resolves a known id to its label", () => {
  expect(settingsSectionLabel("theme")).toBe("Theme");
  expect(settingsSectionLabel("transcript")).toBe("Transcript display");
  expect(settingsSectionLabel("credentials")).toBe("Providers & credentials");
});

test("settingsSectionLabel falls back to the raw id for an unknown section (e.g. a stale bookmark)", () => {
  expect(settingsSectionLabel("some-removed-section")).toBe("some-removed-section");
});

test("isKnownSettingsSection distinguishes real ids from unknown ones", () => {
  expect(isKnownSettingsSection("hub")).toBe(true);
  expect(isKnownSettingsSection("not-a-real-section")).toBe(false);
});
