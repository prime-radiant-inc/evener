import { expect, test } from "vitest";
import type { NavigationInvalidationTarget } from "../../types.gen";
import { isSequenceGap, matchesTarget, matchingTargets, requiredRevision } from "./invalidation";
import type { ResourceKey } from "./types";

const manifest: ResourceKey = { kind: "manifest" };
const live: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 50 };
const needsYou: ResourceKey = { kind: "section", section: "needs_you", offset: 50, limit: 50 };
const projects: ResourceKey = { kind: "catalog", catalog: "projects", offset: 0, limit: 50 };
const archived: ResourceKey = { kind: "catalog", catalog: "archived_projects", offset: 0, limit: 50 };
const pinCatalog: ResourceKey = { kind: "pin_catalog", offset: 0, limit: 50 };
const pinSection: ResourceKey = { kind: "pin_section", sectionId: "s1", offset: 0, limit: 50 };
const project: ResourceKey = { kind: "project", projectKey: "p" };
const page: ResourceKey = { kind: "project_page", projectKey: "p", tier: "current", offset: 100, limit: 50 };
const otherPage: ResourceKey = { kind: "project_page", projectKey: "q", tier: "recent", offset: 0, limit: 50 };
const location: ResourceKey = { kind: "location", ref: "ref-1" };
const everyKey = [
  manifest,
  live,
  needsYou,
  projects,
  archived,
  pinCatalog,
  pinSection,
  project,
  page,
  otherPage,
  location,
];

const matched = (target: NavigationInvalidationTarget) => everyKey.filter((key) => matchesTarget(key, target));

test("a scoped target names the resources of its kind and scope, whatever page of them is loaded", () => {
  expect(matched({ kind: "manifest" })).toEqual([manifest]);
  expect(matched({ kind: "section", section: "live" })).toEqual([live]);
  expect(matched({ kind: "section", section: "needs_you" })).toEqual([needsYou]);
  expect(matched({ kind: "catalog", catalog: "projects" })).toEqual([projects]);
  expect(matched({ kind: "catalog", catalog: "archived_projects" })).toEqual([archived]);
  expect(matched({ kind: "pin_catalog" })).toEqual([pinCatalog]);
  expect(matched({ kind: "pin_section", sectionId: "s1" })).toEqual([pinSection]);
  expect(matched({ kind: "pin_section", sectionId: "s2" })).toEqual([]);
});

test("a project target names the project resource and every page of it", () => {
  expect(matched({ kind: "project", projectKey: "p" })).toEqual([project, page]);
  expect(matched({ kind: "project", projectKey: "q" })).toEqual([otherPage]);
  expect(matched({ kind: "project", projectKey: "zzz" })).toEqual([]);
});

test("the wildcard names every loaded project resource and nothing else", () => {
  expect(matched({ kind: "all_loaded_projects" })).toEqual([project, page, otherPage]);
});

test("a target outside the vocabulary names nothing, and no target names a location", () => {
  expect(matched({ kind: "section", section: "recent" })).toEqual([]);
  expect(matched({ kind: "catalog", catalog: "everything" })).toEqual([]);
  expect(matched({ kind: "pin_section" })).toEqual([]);
  expect(matched({ kind: "project" })).toEqual([]);
  expect(matched({ kind: "unknown" as NavigationInvalidationTarget["kind"] })).toEqual([]);
  for (const target of [
    { kind: "manifest" },
    { kind: "all_loaded_projects" },
    { kind: "project", projectKey: "p" },
  ] as NavigationInvalidationTarget[])
    expect(matchesTarget(location, target)).toBe(false);
});

test("matchingTargets keeps the receipt's order and drops the targets for other resources", () => {
  const targets: NavigationInvalidationTarget[] = [
    { kind: "manifest", revision: 9 },
    { kind: "all_loaded_projects" },
    { kind: "catalog", catalog: "projects", revision: 3 },
    { kind: "project", projectKey: "p", revision: 4 },
  ];
  expect(matchingTargets(page, targets)).toEqual([targets[1], targets[3]]);
  expect(matchingTargets(projects, targets)).toEqual([targets[2]]);
  expect(matchingTargets(location, targets)).toEqual([]);
});

test("requiredRevision is the highest revision a matching target announces, and zero when none announces one", () => {
  const targets: NavigationInvalidationTarget[] = [
    { kind: "manifest", revision: 90 },
    { kind: "catalog", catalog: "projects", revision: 2 },
    { kind: "catalog", catalog: "projects", revision: 5 },
    { kind: "pin_catalog", revision: 40 },
  ];
  expect(requiredRevision(projects, targets)).toBe(5);
  expect(requiredRevision(pinCatalog, targets)).toBe(40);
  expect(requiredRevision(live, targets)).toBe(0);
  // The wildcard carries no revision on the wire, so it obliges a project
  // page to re-read without raising the revision it must reach.
  expect(requiredRevision(page, [{ kind: "all_loaded_projects" }])).toBe(0);
  expect(
    requiredRevision(page, [{ kind: "all_loaded_projects" }, { kind: "project", projectKey: "p", revision: 7 }]),
  ).toBe(7);
});

test("a sequence gap is a skipped number, not a repeat or a reordering", () => {
  expect(isSequenceGap(0, 1)).toBe(false);
  expect(isSequenceGap(1, 2)).toBe(false);
  expect(isSequenceGap(1, 3)).toBe(true);
  expect(isSequenceGap(0, 4)).toBe(true);
  expect(isSequenceGap(2, 2)).toBe(false);
  expect(isSequenceGap(3, 1)).toBe(false);
});
