// @vitest-environment node
import { expect, test } from "vitest";
import {
  DEFAULT_SECTION_ID,
  isKnownSettingsSection,
  matchesSettingsSection,
  resolveSettingsSection,
  SETTINGS_CLUSTERS,
  SETTINGS_SECTIONS,
  settingsSectionLabel,
} from "./sections";

test("has exactly 17 navigation destinations", () => {
  expect(SETTINGS_SECTIONS).toHaveLength(17);
});

test("every section id is unique", () => {
  const ids = SETTINGS_SECTIONS.map((s) => s.id);
  expect(new Set(ids).size).toBe(ids.length);
});

test.each([
  ["", true],
  ["   ", true],
  ["  ExAm  ", true],
  [" control ", true],
  ["missing", false],
  ["example control", false],
])("matches labels and separate terms for query %s", (query, expected) => {
  expect(
    matchesSettingsSection({ id: "test", label: "Example", searchTerms: ["Control"], cluster: "preferences" }, query),
  ).toBe(expected);
});

test("preferences lead the navigation in task order", () => {
  const preferences = SETTINGS_SECTIONS.filter((s) => s.cluster === "preferences");
  expect(preferences.map((s) => s.id)).toEqual(["theme", "transcript", "keybindings", "notifications"]);
  expect(SETTINGS_SECTIONS.slice(0, 4)).toEqual(preferences);
});

test.each([
  ["composer", "keybindings"],
  ["favicon", "notifications"],
  ["transcript", "transcript"],
  ["cost", "transcript"],
  ["mobile", "mobile"],
])("control query %s finds its owning destination %s", (query, owner) => {
  expect(
    SETTINGS_SECTIONS.filter((section) => matchesSettingsSection(section, query)).map((section) => section.id),
  ).toContain(owner);
});

test.each(["origin", "TLS"])("unsupported Hub field %s does not offer a destination", (query) => {
  expect(SETTINGS_SECTIONS.filter((section) => matchesSettingsSection(section, query))).toEqual([]);
});

test('the "agents-models" cluster has exactly these 5 sections, in order, right after preferences', () => {
  const cluster = SETTINGS_SECTIONS.filter((s) => s.cluster === "agents-models");
  expect(cluster.map((s) => s.id)).toEqual(["credentials", "agents", "agents-md", "launch-evener", "inrepo"]);
  expect(SETTINGS_SECTIONS.slice(4, 9)).toEqual(cluster);
});

test('the "extensions" cluster has exactly these 3 sections, in order, right after "agents-models"', () => {
  const cluster = SETTINGS_SECTIONS.filter((s) => s.cluster === "extensions");
  expect(cluster.map((s) => s.id)).toEqual(["plugins-manager", "skills", "mcp"]);
  expect(SETTINGS_SECTIONS.slice(9, 12)).toEqual(cluster);
});

test('the "system" cluster has exactly these 5 sections, in order, last', () => {
  const cluster = SETTINGS_SECTIONS.filter((s) => s.cluster === "system");
  expect(cluster.map((s) => s.id)).toEqual(["hosts", "mobile", "hub", "storage", "about"]);
  expect(SETTINGS_SECTIONS.slice(12, 17)).toEqual(cluster);
});

test('"about" is the very last section overall', () => {
  expect(SETTINGS_SECTIONS[SETTINGS_SECTIONS.length - 1]?.id).toBe("about");
});

test("SETTINGS_CLUSTERS lists the 4 labeled clusters in nav order, each with a label", () => {
  expect(SETTINGS_CLUSTERS.map((c) => c.id)).toEqual(["preferences", "agents-models", "extensions", "system"]);
  for (const cluster of SETTINGS_CLUSTERS) expect(cluster.label).not.toBe("");
});

test("every section's cluster field (when set) references a real cluster id", () => {
  const clusterIds = new Set(SETTINGS_CLUSTERS.map((c) => c.id));
  for (const section of SETTINGS_SECTIONS) {
    if (section.cluster !== undefined) expect(clusterIds.has(section.cluster)).toBe(true);
  }
});

test("DEFAULT_SECTION_ID names the Appearance preference", () => {
  const match = SETTINGS_SECTIONS.find((s) => s.id === DEFAULT_SECTION_ID);
  expect(match).toBeDefined();
  expect(match?.id).toBe("theme");
  expect(match?.cluster).toBe("preferences");
});

test("settingsSectionLabel resolves a known id to that section's own label", () => {
  for (const section of SETTINGS_SECTIONS) {
    expect(section.label).not.toBe(section.id);
    expect(settingsSectionLabel(section.id)).toBe(section.label);
  }
});

test("settingsSectionLabel falls back to the raw id for an unknown section (e.g. a stale bookmark)", () => {
  expect(settingsSectionLabel("some-removed-section")).toBe("some-removed-section");
});

test("isKnownSettingsSection distinguishes real ids from unknown ones", () => {
  expect(isKnownSettingsSection("hub")).toBe(true);
  expect(isKnownSettingsSection("not-a-real-section")).toBe(false);
});

test.each([
  [undefined, null, "theme"],
  [undefined, "notifications", "notifications"],
  ["display", "notifications", "theme"],
  ["general", "notifications", "theme"],
  ["plugins", "notifications", "theme"],
  [undefined, "plugins", "theme"],
  ["plugins-manager", "notifications", "plugins-manager"],
  [undefined, "general", "theme"],
  ["unrecognized", "notifications", "theme"],
  ["project", "theme", "project"],
  [undefined, "project", "theme"],
  [undefined, "display", "theme"],
  ["hub", "notifications", "hub"],
] as const)("resolves explicit %s and remembered %s to %s", (explicit, remembered, expected) => {
  expect(resolveSettingsSection(explicit, remembered)).toBe(expected);
});
