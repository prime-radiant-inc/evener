// @vitest-environment node
import { expect, test } from "vitest";
import {
  DEFAULT_SECTION_ID,
  isKnownSettingsSection,
  resolveSettingsSection,
  SETTINGS_CLUSTERS,
  SETTINGS_SECTIONS,
  settingsSectionLabel,
} from "./sections";

test("has exactly 18 navigation destinations during migration", () => {
  expect(SETTINGS_SECTIONS).toHaveLength(18);
});

test("every section id is unique", () => {
  const ids = SETTINGS_SECTIONS.map((s) => s.id);
  expect(new Set(ids).size).toBe(ids.length);
});

test("preferences lead the navigation in task order", () => {
  const preferences = SETTINGS_SECTIONS.filter((s) => s.cluster === "preferences");
  expect(preferences.map((s) => s.id)).toEqual(["theme", "transcript", "keybindings", "notifications"]);
  expect(SETTINGS_SECTIONS.slice(0, 4)).toEqual(preferences);
});

test('the "agents-models" cluster has exactly these 5 sections, in order, right after the ungrouped ones', () => {
  const cluster = SETTINGS_SECTIONS.filter((s) => s.cluster === "agents-models");
  expect(cluster.map((s) => s.id)).toEqual(["credentials", "agents", "agents-md", "launch-evener", "inrepo"]);
  expect(SETTINGS_SECTIONS.slice(4, 9)).toEqual(cluster);
});

test('the "extensions" cluster has exactly these 4 sections, in order, right after "agents-models"', () => {
  const cluster = SETTINGS_SECTIONS.filter((s) => s.cluster === "extensions");
  expect(cluster.map((s) => s.id)).toEqual(["plugins-manager", "plugins", "skills", "mcp"]);
  expect(SETTINGS_SECTIONS.slice(9, 13)).toEqual(cluster);
});

test('the "daemon" cluster has exactly these 5 sections, in order, last', () => {
  const cluster = SETTINGS_SECTIONS.filter((s) => s.cluster === "system");
  expect(cluster.map((s) => s.id)).toEqual(["hosts", "mobile", "hub", "storage", "about"]);
  expect(SETTINGS_SECTIONS.slice(13, 18)).toEqual(cluster);
});

test('"about" is the very last section overall', () => {
  expect(SETTINGS_SECTIONS[SETTINGS_SECTIONS.length - 1]?.id).toBe("about");
});

test("SETTINGS_CLUSTERS lists the 3 labeled clusters in nav order, each with a label", () => {
  expect(SETTINGS_CLUSTERS.map((c) => c.id)).toEqual(["preferences", "agents-models", "extensions", "system"]);
  for (const cluster of SETTINGS_CLUSTERS) expect(cluster.label).not.toBe("");
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
  [undefined, "general", "theme"],
  ["unrecognized", "notifications", "theme"],
  ["project", "theme", "project"],
  [undefined, "project", "theme"],
  [undefined, "display", "theme"],
  ["hub", "notifications", "hub"],
] as const)("resolves explicit %s and remembered %s to %s", (explicit, remembered, expected) => {
  expect(resolveSettingsSection(explicit, remembered)).toBe(expected);
});
