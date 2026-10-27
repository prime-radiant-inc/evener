import type { NavigationReadParams } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { capability, manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, assert, expect, test, vi } from "vitest";
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

test.each(["current", "recent", "archived"] as const)(
  "%s project pages select each ref once and retain raw offsets through refresh and reconnect",
  async (tier) => {
    const client = new FakeClient("ready");
    const initialize = await client.connect();
    const calls: NavigationReadParams[] = [];
    let revision = 1;
    const rows = (...ids: string[]) => ids.map((id) => ({ ref: `local:${tier}-${id}`, children: [] }));
    client.on("evener/navigation/read", (params) => {
      calls.push(params);
      const snapshot = (data: unknown) => wireSnapshot(params, data, `"revision-${revision}"`, revision);
      if (params.resource === "manifest")
        return snapshot(
          manifest({ catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } } }),
        );
      if (params.resource === "catalog")
        return snapshot({ projects: [{ key: "overlap", name: "Overlap fixture", session_count: 4 }], remaining: 0 });
      if (params.resource === "project") {
        const response = snapshot({ key: "overlap", [tier]: { sessions: rows("a", "b") } });
        const data = response.data as { metadata: Record<string, unknown> };
        return { ...response, data: { ...data, metadata: { ...data.metadata, [`${tier}_remaining`]: 4 } } };
      }
      if (params.resource === "project_page") {
        if (params.offset === 2) return snapshot({ sessions: rows("b", "c"), remaining: 2 });
        if (params.offset === 4) return snapshot({ sessions: rows("c", `tail-${revision}`), remaining: 0 });
      }
      throw new Error(`unexpected navigation read ${JSON.stringify(params)}`);
    });
    act(() => initNavigation(client, capability()));
    const selected: string[] = [];
    const mounted = render(<ComicPicker onSelect={(ref) => selected.push(ref)} />);
    // Await the boot catalog before expanding, rather than guessing microtask counts.
    const project = await screen.findByRole("button", { name: "Overlap fixture" });
    act(() => project.click());
    const region = () => screen.getByRole("region", { name: `Overlap fixture ${tier}` });
    await within(region()).findByRole("button", { name: `local:${tier}-a` });
    for (const offset of [2, 4]) {
      act(() =>
        within(region())
          .getByRole("button", { name: `More ${tier}` })
          .click(),
      );
      await waitFor(() =>
        expect(
          navigationStore
            .getState()
            .resources.get(keyID({ kind: "project_page", projectKey: "overlap", tier, offset, limit: 50 }))
            ?.loadedRevision,
        ).toBe(1),
      );
    }
    const checkRows = () => {
      selected.length = 0;
      for (const button of within(region()).getAllByRole("button")) button.click();
      expect
        .soft(selected)
        .toEqual([`local:${tier}-a`, `local:${tier}-b`, `local:${tier}-c`, `local:${tier}-tail-${revision}`]);
      expect(within(region()).queryByRole("button", { name: `More ${tier}` })).toBeNull();
    };
    const pageOffsets = () => calls.filter((call) => call.resource === "project_page").map((call) => call.offset);
    expect(pageOffsets()).toEqual([2, 4]);
    checkRows();
    for (const recovery of ["refresh", "reconnect"]) {
      revision++;
      calls.length = 0;
      await act(async () => {
        if (recovery === "refresh") {
          client.emitNotification({
            method: "evener/navigation/invalidated",
            params: {
              generationId: "generation_test",
              sequence: 1,
              targets: [{ kind: "project", projectKey: "overlap", revision }],
            },
          });
        } else {
          client.emitStateChange("reconnecting");
          client.emitReady({ ...initialize, navigation: { ...capability(), sequence: 2 } });
        }
        await navigationStore.getState().awaitNavigationTargets([{ kind: "project", projectKey: "overlap", revision }]);
      });
      expect(pageOffsets().sort()).toEqual([2, 4]);
      checkRows();
    }
    mounted.unmount();
    client.close();
  },
);

const recoveryCases = [
  { scenario: "terminal page", failed: [4], remaining: 0 },
  { scenario: "nonterminal page", failed: [4], remaining: 2 },
  { scenario: "earlier page with healthy later page", failed: [2], remaining: 2 },
  { scenario: "multiple pages", failed: [2, 4], remaining: 0 },
  { scenario: "initial project", failed: [0], remaining: 0 },
];
test.each(
  (["current", "recent", "archived"] as const).flatMap((tier) =>
    (["refresh", "reconnect"] as const).flatMap((recovery) =>
      recoveryCases.map((scenario) => ({ tier, recovery, ...scenario })),
    ),
  ),
)(
  "$tier $scenario failure during $recovery retains rows and retries the exact resource",
  async ({ tier, recovery, failed, remaining }) => {
    const client = new FakeClient("ready");
    const initialize = await client.connect();
    const calls: NavigationReadParams[] = [];
    let revision = 1;
    const failures = new Set<number>();
    let retryGate: ReturnType<typeof deferred<void>> | undefined;
    const rows = (...ids: string[]) => ids.map((id) => ({ ref: `local:${id}`, children: [] }));
    client.on("evener/navigation/read", async (params) => {
      calls.push(params);
      const snapshot = (data: unknown) => wireSnapshot(params, data, `"revision-${revision}"`, revision);
      if (params.resource === "manifest")
        return snapshot(
          manifest({ catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } } }),
        );
      if (params.resource === "catalog")
        return snapshot({ projects: [{ key: "recovery", name: "Recovery fixture", session_count: 7 }], remaining: 0 });
      if (params.resource === "project" || params.resource === "project_page") {
        const offset = params.resource === "project" ? 0 : params.offset;
        if (failures.has(offset ?? 0)) throw new Error("scripted transport failure");
        await retryGate?.promise;
        if (params.resource === "project") {
          const response = snapshot({ key: "recovery", [tier]: { sessions: rows("a", `initial-${revision}`) } });
          const data = response.data as { metadata: Record<string, unknown> };
          return { ...response, data: { ...data, metadata: { ...data.metadata, [`${tier}_remaining`]: 4 } } };
        }
        if (offset === 2) return snapshot({ sessions: rows("a", `middle-${revision}`), remaining: 2 });
        if (offset === 4)
          return snapshot({
            sessions: revision === 1 ? rows("a", "tail-1") : rows("a", `tail-${revision}`, "bonus"),
            remaining: revision === 1 ? remaining : 1,
          });
        if (offset === 7) return snapshot({ sessions: rows("final"), remaining: 0 });
      }
      throw new Error(`unexpected navigation read ${JSON.stringify(params)}`);
    });
    const resource = (offset: number) =>
      navigationStore
        .getState()
        .resources.get(
          keyID(
            offset === 0
              ? { kind: "project", projectKey: "recovery" }
              : { kind: "project_page", projectKey: "recovery", tier, offset, limit: 50 },
          ),
        );
    act(() => initNavigation(client, capability()));
    const selected: string[] = [];
    const mounted = render(<ComicPicker onSelect={(ref) => selected.push(ref)} />);
    try {
      const project = await screen.findByRole("button", { name: "Recovery fixture" });
      act(() => project.click());
      const region = () => screen.getByRole("region", { name: `Recovery fixture ${tier}` });
      const more = () => within(region()).getByRole("button", { name: `More ${tier}` });
      await waitFor(() => expect(resource(0)?.loadedRevision).toBe(1));
      for (const offset of [2, 4]) {
        act(() => more().click());
        await waitFor(() => expect(resource(offset)?.loadedRevision).toBe(1));
      }
      const selectRows = (initial: number, middle: number, tail: number) => {
        selected.length = 0;
        for (const button of within(within(region()).getByRole("list")).getAllByRole("button")) button.click();
        expect(selected).toEqual([
          "local:a",
          `local:initial-${initial}`,
          `local:middle-${middle}`,
          `local:tail-${tail}`,
          ...(tail === 2 ? ["local:bonus"] : []),
        ]);
      };
      selectRows(1, 1, 1);
      const retained = new Map(failed.map((offset) => [offset, resource(offset)?.data]));
      revision = 2;
      for (const offset of failed) failures.add(offset);
      calls.length = 0;
      await act(async () => {
        if (recovery === "refresh")
          client.emitNotification({
            method: "evener/navigation/invalidated",
            params: {
              generationId: "generation_test",
              sequence: 1,
              targets: [{ kind: "project", projectKey: "recovery", revision }],
            },
          });
        else {
          client.emitStateChange("reconnecting");
          client.emitReady({ ...initialize, navigation: { ...capability(), sequence: 2 } });
        }
        await vi.waitFor(() => {
          for (const offset of [0, 2, 4]) {
            expect(resource(offset)?.loading).toBe(false);
            if (failures.has(offset)) {
              expect(resource(offset)?.error).toBeTruthy();
              expect(resource(offset)?.data).toBe(retained.get(offset));
            } else expect(resource(offset)?.loadedRevision).toBe(2);
          }
        });
      });
      expect(
        calls
          .filter((call) => call.resource === "project_page")
          .map((call) => call.offset)
          .sort(),
      ).toEqual([2, 4]);
      const visibleRevision = (offset: number) => (failures.has(offset) ? 1 : 2);
      selectRows(visibleRevision(0), visibleRevision(2), visibleRevision(4));
      expect(project.getAttribute("aria-expanded")).toBe("true");
      expect(screen.queryAllByRole("alert")).toHaveLength(failed.length);
      for (const offset of failed) {
        calls.length = 0;
        retryGate = deferred<void>();
        const visible = [visibleRevision(0), visibleRevision(2), visibleRevision(4)] as const;
        failures.delete(offset);
        act(() => {
          const alert = screen.getAllByRole("alert")[0];
          assert(alert);
          within(alert).getByRole("button", { name: /Retry/ }).click();
        });
        await waitFor(() => expect(calls).toHaveLength(1));
        expect(calls[0]).toMatchObject(
          offset === 0
            ? { resource: "project", projectKey: "recovery" }
            : { resource: "project_page", projectKey: "recovery", tier, offset, limit: 50 },
        );
        selectRows(...visible);
        if (offset === 4) expect(within(region()).queryByRole("button", { name: `More ${tier}` })).toBeNull();
        await act(async () => retryGate?.resolve());
        await waitFor(() => expect(resource(offset)?.loadedRevision).toBe(2));
        expect(resource(offset)?.error).toBeNull();
        expect(resource(offset)?.data).not.toBe(retained.get(offset));
        selectRows(visibleRevision(0), visibleRevision(2), visibleRevision(4));
        expect(screen.queryAllByRole("alert")).toHaveLength(failures.size);
      }
      expect(within(region()).queryByRole("button", { name: "local:initial-1" })).toBeNull();
      expect(within(region()).queryByRole("button", { name: "local:middle-1" })).toBeNull();
      expect(within(region()).queryByRole("button", { name: "local:tail-1" })).toBeNull();
      calls.length = 0;
      act(() => more().click());
      const final = await within(region()).findByRole("button", { name: "local:final" });
      expect(calls).toHaveLength(1);
      // The recovered last page has three raw rows, but only two new visible refs.
      expect(calls[0]).toMatchObject({ resource: "project_page", projectKey: "recovery", tier, offset: 7, limit: 50 });
      selected.length = 0;
      final.click();
      expect(selected).toEqual(["local:final"]);
      expect(within(region()).queryByRole("button", { name: `More ${tier}` })).toBeNull();
    } finally {
      mounted.unmount();
      client.close();
      retryGate?.resolve();
    }
  },
);

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
  expect(within(current()).getByRole("alert")).toBeDefined();
  await act(async () => {
    const retry = within(current()).getByRole("button", { name: "Retry current page" });
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
  expect(within(projects()).getByRole("alert")).toBeDefined();
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
