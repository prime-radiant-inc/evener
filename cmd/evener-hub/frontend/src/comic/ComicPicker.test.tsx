import type { NavigationReadParams } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { capability, manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { ComicPicker } from "./ComicPicker";

const flush = async () => {
  for (let n = 0; n < 64; n++) await Promise.resolve();
};
afterEach(() => resetNavigationStoreForTests());
test("only requested pages load, and selecting a shallow row opens its exact ref", async () => {
  const calls: NavigationReadParams[] = [];
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    calls.push(params);
    if (params.resource === "manifest")
      return wireSnapshot(
        params,
        manifest({
          sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "section")
      return wireSnapshot(params, {
        sessions: [{ ref: "local:one", title: "One", children: [] }],
        remaining: 0,
        truncated: false,
      });
    if (params.resource === "catalog")
      return wireSnapshot(params, { projects: [{ key: "p", name: "Project P", session_count: 1 }], remaining: 0 });
    if (params.resource === "project")
      return wireSnapshot(params, {
        key: params.projectKey,
        current: { sessions: [{ ref: "local:two", title: "Two", children: [] }], remaining: 0 },
        recent: { sessions: [], remaining: 0 },
        archived: { sessions: [], remaining: 0 },
        truncated: false,
      });
    throw new Error(`unexpected ${params.resource}`);
  });
  initNavigation(client, capability());
  await act(flush);
  const selected: string[] = [];
  const mounted = render(<ComicPicker onSelect={(ref) => selected.push(ref)} />);
  await act(flush);
  expect(calls.filter((call) => call.resource === "project")).toHaveLength(0);
  expect(
    navigationStore.getState().resources.get(keyID({ kind: "section", section: "live", offset: 0, limit: 50 }))?.error,
  ).toBeNull();
  screen.getByRole("button", { name: "One" }).click();
  await act(async () => {
    screen.getByRole("button", { name: /Project P/ }).click();
    await flush();
  });
  await act(flush);
  screen.getByRole("button", { name: /Two/ }).click();
  expect(selected).toEqual(["local:one", "local:two"]);
  expect(calls.filter((call) => call.resource === "project_page")).toHaveLength(0);
  expect(navigationStore.getState().expanded.has("p")).toBe(false);
  mounted.unmount();
});

test("remote live rows and current, recent, archived project pages load only on request", async () => {
  const calls: NavigationReadParams[] = [];
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    calls.push(params);
    if (params.resource === "manifest")
      return wireSnapshot(
        params,
        manifest({
          sources: [{ id: "remote", kind: "remote", label: "Other host", online: false }],
          sections: { live: { count: 2 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
          catalogs: { projects: { count: 1 }, archived_projects: { count: 1 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "section")
      return wireSnapshot(params, {
        sessions: [
          { ref: "local:live", title: "Local live", children: [] },
          { ref: "remote:live", title: "Remote live", children: [] },
        ],
        remaining: 0,
        truncated: false,
      });
    if (params.resource === "catalog")
      return wireSnapshot(params, {
        projects: [
          {
            key: params.catalog === "projects" ? "p" : "old",
            name: params.catalog === "projects" ? "Active project" : "Archived project",
            session_count: 1,
            is_archived: params.catalog !== "projects",
          },
        ],
        remaining: 0,
      });
    if (params.resource === "project") {
      const response = wireSnapshot(params, {
        key: params.projectKey,
        current: { sessions: [{ ref: "local:current", title: "Current", children: [] }] },
        recent: { sessions: [{ ref: "local:recent", title: "Recent", children: [] }] },
        archived: { sessions: [{ ref: "local:archived", title: "Archived", children: [] }] },
        truncated: true,
      });
      const data = response.data as { metadata: Record<string, unknown> };
      return {
        ...response,
        data: {
          ...data,
          metadata: { ...data.metadata, current_remaining: 1, recent_remaining: 1, archived_remaining: 1 },
        },
      };
    }
    if (params.resource === "project_page")
      return wireSnapshot(params, {
        key: params.projectKey,
        tier: params.tier,
        offset: params.offset,
        sessions: [{ ref: `local:${params.tier}-more`, title: `${params.tier} more`, children: [] }],
        remaining: 0,
        truncated: false,
      });
    throw new Error(`unexpected ${params.resource}`);
  });
  initNavigation(client, capability());
  await act(flush);
  const selected: string[] = [];
  const mounted = render(<ComicPicker onSelect={(ref) => selected.push(ref)} />);
  await act(flush);
  expect(calls.filter((call) => call.resource === "project_page")).toHaveLength(0);
  await act(async () => {
    screen.getByRole("button", { name: /Active project/ }).click();
    await flush();
  });
  expect(calls.filter((call) => call.resource === "project_page")).toHaveLength(0);
  for (const tier of ["current", "recent", "archived"] as const) {
    expect(navigationStore.getState().resources.get(keyID({ kind: "project", projectKey: "p" }))?.data).toMatchObject({
      [tier]: { remaining: 1 },
    });
    await act(async () => {
      screen.getByRole("button", { name: `More ${tier}` }).click();
      await flush();
    });
  }
  expect(
    calls
      .filter((call) => call.resource === "project_page")
      .map((call) => [call.projectKey, call.tier, call.offset, call.limit]),
  ).toEqual([
    ["p", "current", 1, 50],
    ["p", "recent", 1, 50],
    ["p", "archived", 1, 50],
  ]);
  screen.getByRole("button", { name: "Remote live" }).click();
  screen.getByRole("button", { name: "Local live" }).click();
  expect(selected).toEqual(["remote:live", "local:live"]);
  await act(async () => {
    screen.getByRole("button", { name: /Archived project/ }).click();
    await flush();
  });
  for (const tier of ["current", "recent", "archived"] as const) {
    await act(async () => {
      within(screen.getByRole("region", { name: `Archived project ${tier}` }))
        .getByRole("button", { name: `More ${tier}` })
        .click();
      await flush();
    });
  }
  expect(
    calls
      .filter((call) => call.resource === "project_page" && call.projectKey === "old")
      .map((call) => [call.tier, call.offset, call.limit]),
  ).toEqual([
    ["current", 1, 50],
    ["recent", 1, 50],
    ["archived", 1, 50],
  ]);
  mounted.unmount();
});

test("a shallow root retains an explicitly supplied child ref until project expansion", async () => {
  const calls: NavigationReadParams[] = [];
  const childRef = "local:child-of-parent-034l-ref";
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    calls.push(params);
    if (params.resource === "manifest")
      return wireSnapshot(
        params,
        manifest({
          sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "section")
      return wireSnapshot(params, {
        sessions: [{ ref: "local:parent-034l-ref", title: "Parent", children: [] }],
        remaining: 0,
      });
    if (params.resource === "catalog")
      return wireSnapshot(params, { projects: [{ key: "p", name: "Parent project", session_count: 2 }], remaining: 0 });
    if (params.resource === "project")
      return wireSnapshot(params, {
        key: "p",
        current: { sessions: [{ ref: childRef, title: "Supplied child", children: [] }], remaining: 0 },
        recent: { sessions: [] },
        archived: { sessions: [] },
      });
    throw new Error(`unexpected ${params.resource}`);
  });
  initNavigation(client, capability());
  await act(flush);
  const selected: string[] = [];
  const mounted = render(<ComicPicker onSelect={(ref) => selected.push(ref)} />);
  expect(screen.getByRole("button", { name: "Parent" })).toBeDefined();
  expect(screen.queryByRole("button", { name: "Supplied child" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Children of Parent" })).toBeNull();
  expect(calls.filter((call) => call.resource === "project" || call.resource === "project_page")).toHaveLength(0);
  await act(async () => {
    screen.getByRole("button", { name: "Parent project" }).click();
    await flush();
  });
  screen.getByRole("button", { name: "Supplied child" }).click();
  expect(selected).toEqual([childRef]);
  expect(calls.filter((call) => call.resource === "project_page" || call.resource === "location")).toHaveLength(0);
  mounted.unmount();
});

test("a failed next page retains last-known live rows during retry", async () => {
  const client = new FakeClient("ready");
  let attempts = 0;
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest")
      return wireSnapshot(
        params,
        manifest({ sections: { live: { count: 2 }, needs_you: { count: 0 }, pin_sections: { count: 0 } } }),
      );
    if (params.resource === "section") {
      if (params.offset === 1 && ++attempts === 1) throw new Error("temporarily unavailable");
      return wireSnapshot(params, {
        sessions: [
          {
            ref: params.offset ? "local:second" : "local:first",
            title: params.offset ? "Second" : "First",
            children: [],
          },
        ],
        remaining: params.offset ? 0 : 1,
        truncated: false,
      });
    }
    if (params.resource === "catalog") return wireSnapshot(params, { projects: [], remaining: 0 });
    throw new Error(`unexpected ${params.resource}`);
  });
  initNavigation(client, capability());
  await act(flush);
  const selected: string[] = [];
  const mounted = render(<ComicPicker onSelect={(ref) => selected.push(ref)} />);
  await act(async () => {
    screen.getByRole("button", { name: "More live" }).click();
    await flush();
  });
  expect(screen.getByRole("button", { name: "First" })).toBeDefined();
  await act(async () => {
    screen.getByRole("button", { name: "Retry live" }).click();
    await flush();
  });
  screen.getByRole("button", { name: "First" }).click();
  screen.getByRole("button", { name: "Second" }).click();
  expect(selected).toEqual(["local:first", "local:second"]);
  mounted.unmount();
});

test("a failed project page retries once per offset and follows returned continuation", async () => {
  const calls: NavigationReadParams[] = [];
  const client = new FakeClient("ready");
  let failed = false;
  client.on("evener/navigation/read", (params) => {
    calls.push(params);
    if (params.resource === "manifest")
      return wireSnapshot(
        params,
        manifest({ catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } } }),
      );
    if (params.resource === "catalog")
      return wireSnapshot(params, { projects: [{ key: "p", name: "Project P", session_count: 3 }], remaining: 0 });
    if (params.resource === "project") {
      const response = wireSnapshot(params, {
        key: "p",
        current: { sessions: [{ ref: "local:first", title: "First", children: [] }] },
        recent: { sessions: [] },
        archived: { sessions: [] },
      });
      const data = response.data as { metadata: Record<string, unknown> };
      return { ...response, data: { ...data, metadata: { ...data.metadata, current_remaining: 2 } } };
    }
    if (params.resource === "project_page") {
      if (params.offset === 1 && !failed) {
        failed = true;
        throw new Error("project page temporarily unavailable");
      }
      return wireSnapshot(params, {
        sessions: [
          {
            ref: params.offset === 1 ? "local:second" : "local:third",
            title: params.offset === 1 ? "Second" : "Third",
            children: [],
          },
        ],
        remaining: params.offset === 1 ? 1 : 0,
      });
    }
    throw new Error(`unexpected ${params.resource}`);
  });
  initNavigation(client, capability());
  await act(flush);
  const mounted = render(<ComicPicker onSelect={() => {}} />);
  await act(async () => {
    screen.getByRole("button", { name: "Project P" }).click();
    await flush();
  });
  const current = () => screen.getByRole("region", { name: "Project P current" });
  await act(async () => {
    within(current()).getByRole("button", { name: "More current" }).click();
    await flush();
  });
  expect(within(current()).getByRole("button", { name: "First" })).toBeDefined();
  expect(within(current()).getByRole("alert").textContent).toContain("Page unavailable");
  await act(async () => {
    const retry = within(current()).getByRole("button", { name: "More current" });
    retry.click();
    retry.click();
    await flush();
  });
  expect(within(current()).getAllByRole("button", { name: "Second" })).toHaveLength(1);
  await act(async () => {
    within(current()).getByRole("button", { name: "More current" }).click();
    await flush();
  });
  expect(within(current()).getAllByRole("button", { name: "Third" })).toHaveLength(1);
  expect(within(current()).queryByRole("button", { name: "More current" })).toBeNull();
  expect(calls.filter((call) => call.resource === "project_page").map((call) => call.offset)).toEqual([1, 1, 2]);
  mounted.unmount();
});

test("a failed catalog page preserves prior projects and retries its exact offset", async () => {
  const calls: NavigationReadParams[] = [];
  const client = new FakeClient("ready");
  let failed = false;
  client.on("evener/navigation/read", (params) => {
    calls.push(params);
    if (params.resource === "manifest")
      return wireSnapshot(
        params,
        manifest({ catalogs: { projects: { count: 3 }, archived_projects: { count: 0 }, test_runs: { count: 0 } } }),
      );
    if (params.resource === "catalog") {
      if (params.catalog === "projects" && params.offset === 1 && !failed) {
        failed = true;
        throw new Error("catalog temporarily unavailable");
      }
      return wireSnapshot(params, {
        projects: [{ key: `p${params.offset}`, name: `Project ${params.offset}`, session_count: 1 }],
        remaining: params.catalog === "projects" ? 2 - (params.offset ?? 0) : 0,
      });
    }
    throw new Error(`unexpected ${params.resource}`);
  });
  initNavigation(client, capability());
  await act(flush);
  const mounted = render(<ComicPicker onSelect={() => {}} />);
  const projects = () => screen.getByRole("region", { name: "projects" });
  await act(async () => {
    within(projects()).getByRole("button", { name: "More projects" }).click();
    await flush();
  });
  expect(within(projects()).getByRole("button", { name: "Project 0" })).toBeDefined();
  expect(within(projects()).getByRole("alert").textContent).toContain("Catalog unavailable");
  await act(async () => {
    within(projects()).getByRole("button", { name: "Retry projects" }).click();
    await flush();
  });
  expect(within(projects()).getAllByRole("button", { name: "Project 1" })).toHaveLength(1);
  await act(async () => {
    within(projects()).getByRole("button", { name: "More projects" }).click();
    await flush();
  });
  expect(within(projects()).getAllByRole("button", { name: "Project 2" })).toHaveLength(1);
  expect(within(projects()).queryByRole("button", { name: "More projects" })).toBeNull();
  expect(
    calls.filter((call) => call.resource === "catalog" && call.catalog === "projects").map((call) => call.offset),
  ).toEqual([0, 1, 1, 2]);
  mounted.unmount();
});
