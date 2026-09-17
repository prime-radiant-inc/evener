import { expect, test } from "vitest";
import type { NavigationReadParams } from "../../types.gen";
import { NAVIGATION_CATALOG_LIMIT, NAVIGATION_SECTION_LIMIT, navigationParamsToResourceKey } from "./types";

// The hub serves an omitted `limit` as the MAXIMUM for the resource's family,
// not one shared default: navigationReadPage in cmd/evener-hub/app_navigation.go
// starts from the maximum it is given and only narrows it when the caller
// names one, and its callers pass maxNavigationSectionRows (50) for section,
// pin_section and project_page and maxNavigationCatalogRows (100) for catalog
// and pin_catalog (cmd/evener-hub/navigation_projection.go). A client that
// defaults a catalog to 50 names a page the hub never served.
test("an omitted limit is the hub's maximum for that resource family", () => {
  expect(NAVIGATION_SECTION_LIMIT).toBe(50);
  expect(NAVIGATION_CATALOG_LIMIT).toBe(100);

  const limitOf = (params: NavigationReadParams) => {
    const key = navigationParamsToResourceKey(params);
    return "limit" in key ? key.limit : undefined;
  };
  expect(limitOf({ representationVersion: 2, resource: "section", section: "live" })).toBe(50);
  expect(limitOf({ representationVersion: 2, resource: "pin_section", sectionId: "pins" })).toBe(50);
  expect(limitOf({ representationVersion: 2, resource: "project_page", projectKey: "p", tier: "current" })).toBe(50);
  expect(limitOf({ representationVersion: 2, resource: "catalog", catalog: "projects" })).toBe(100);
  expect(limitOf({ representationVersion: 2, resource: "pin_catalog" })).toBe(100);
});

test("an explicit page wins over the maximum, and an unpaged resource carries none", () => {
  expect(
    navigationParamsToResourceKey({
      representationVersion: 2,
      resource: "catalog",
      catalog: "projects",
      offset: 100,
      limit: 25,
    }),
  ).toEqual({ kind: "catalog", catalog: "projects", offset: 100, limit: 25 });
  expect(navigationParamsToResourceKey({ representationVersion: 2, resource: "manifest" })).toEqual({
    kind: "manifest",
  });
  expect(navigationParamsToResourceKey({ representationVersion: 2, resource: "project", projectKey: "p" })).toEqual({
    kind: "project",
    projectKey: "p",
  });
  expect(navigationParamsToResourceKey({ representationVersion: 2, resource: "location", ref: "local:a" })).toEqual({
    kind: "location",
    ref: "local:a",
  });
});
