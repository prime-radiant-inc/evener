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
// four sections with no legacy counterpart, "keybindings", "about", "mobile", and
// "agents-md" (see sections.ts's own doc comment).

test("has exactly 20 sections", () => {
  expect(SETTINGS_SECTIONS).toHaveLength(20);
});

test("every section id is unique", () => {
  const ids = SETTINGS_SECTIONS.map((s) => s.id);
  expect(new Set(ids).size).toBe(ids.length);
});

test("the 6 ungrouped sections are general/theme/transcript/display/notifications/keybindings, in this order, first", () => {
  const ungrouped = SETTINGS_SECTIONS.filter((s) => s.cluster === undefined);
  expect(ungrouped.map((s) => s.id)).toEqual([
    "general",
    "theme",
    "transcript",
    "display",
    "notifications",
    "keybindings",
  ]);
  expect(SETTINGS_SECTIONS.slice(0, 6)).toEqual(ungrouped);
});

test('the "agents-models" cluster has exactly these 5 sections, in order, right after the ungrouped ones', () => {
  const cluster = SETTINGS_SECTIONS.filter((s) => s.cluster === "agents-models");
  expect(cluster.map((s) => s.id)).toEqual(["credentials", "agents", "agents-md", "launch-evener", "inrepo"]);
  expect(SETTINGS_SECTIONS.slice(6, 11)).toEqual(cluster);
});

test('the "extensions" cluster has exactly these 4 sections, in order, right after "agents-models"', () => {
  const cluster = SETTINGS_SECTIONS.filter((s) => s.cluster === "extensions");
  expect(cluster.map((s) => s.id)).toEqual(["plugins-manager", "plugins", "skills", "mcp"]);
  expect(SETTINGS_SECTIONS.slice(11, 15)).toEqual(cluster);
});

test('the "daemon" cluster has exactly these 5 sections, in order, last', () => {
  const cluster = SETTINGS_SECTIONS.filter((s) => s.cluster === "daemon");
  expect(cluster.map((s) => s.id)).toEqual(["hosts", "hub", "mobile", "storage", "about"]);
  expect(SETTINGS_SECTIONS.slice(15, 20)).toEqual(cluster);
});

test('"about" is the very last section overall', () => {
  expect(SETTINGS_SECTIONS[SETTINGS_SECTIONS.length - 1]?.id).toBe("about");
});

test("SETTINGS_CLUSTERS lists the 3 labeled clusters in nav order, each with a label", () => {
  expect(SETTINGS_CLUSTERS.map((c) => c.id)).toEqual(["agents-models", "extensions", "daemon"]);
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
  expect(match?.cluster).toBeUndefined();
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
