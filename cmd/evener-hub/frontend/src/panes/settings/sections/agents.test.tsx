import { friendlyErrorMessage, type SettingsOverviewResponse, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "../../../stores/connection";
import { resetSettingsOverviewStoreForTests, type SettingsOverviewStoreState } from "../../../stores/settingsOverview";
import { AgentsSection } from "./agents";

afterEach(() => {
  // Unmount first: resetting the settingsOverview singleton then notifies
  // subscribers, and doing that while AgentsSection is still mounted is an
  // unwrapped state update (React's act warning).
  cleanup();
  resetSettingsOverviewStoreForTests();
  resetConnectionStoreForTests();
});

function fixture(overrides: Partial<SettingsOverviewStoreState> = {}): () => SettingsOverviewStoreState {
  return () => ({
    data: null,
    loading: false,
    error: null,
    fetch: async () => {},
    refresh: async () => {},
    ...overrides,
  });
}

describe("AgentsSection", () => {
  test("calls fetch() once on mount (fetch caches; the store owns re-fetch decisions)", () => {
    // useConnectedEffect gates the mount-time fetch on the connection being
    // ready (see that hook's own doc comment) - true of the real
    // settingsOverview store this section now fronts onto.
    connectionStore.setState({ state: "ready" });
    const fetchFn = vi.fn().mockResolvedValue(undefined);
    render(<AgentsSection sectionId="agents" useOverview={fixture({ fetch: fetchFn })} />);
    expect(fetchFn).toHaveBeenCalledTimes(1);
  });

  test("shows a loading indicator while loading", () => {
    render(<AgentsSection sectionId="agents" useOverview={fixture({ loading: true })} />);
    expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
  });

  // The overview store translates a failure while it still has its type, so
  // the section renders the stored text as-is: a hub-reported explanation or a
  // client-side diagnosis survives instead of collapsing to the generic one.
  test.each([
    ["a hub-reported WireError", new WireError("overview index is rebuilding", -32000)],
    ["a client-side failure", new Error("AppwireClient closed")],
  ])("%s keeps its own friendly explanation", async (_name, err) => {
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    fake.on("evener/settings/overview", () => {
      throw err;
    });
    render(<AgentsSection sectionId="agents" />);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain(friendlyErrorMessage(err)));
    expect(screen.queryByRole("list")).toBeNull();
  });

  test("no agents renders the empty state instead of a list", () => {
    const data: SettingsOverviewResponse = { agents: [] };
    render(<AgentsSection sectionId="agents" useOverview={fixture({ data })} />);
    expect(screen.getByTestId("empty-state")).toBeTruthy();
    expect(screen.queryByRole("list")).toBeNull();
  });

  test("an agent with an editPath renders the standard open affordance: 'open in editor', target=_blank, no opener access", () => {
    const data: SettingsOverviewResponse = {
      agents: [{ name: "reviewer", editPath: "editor://open?path=/plugins/reviewer.md" }],
    };
    render(<AgentsSection sectionId="agents" useOverview={fixture({ data })} />);
    const link = screen.getByRole("link", { name: /open in editor/i }) as HTMLAnchorElement;
    expect(link.href).toBe("editor://open?path=/plugins/reviewer.md");
    expect(link.target).toBe("_blank");
    // The shared OpenButton anchor: no opener access, no referrer - the same
    // rel policy as the app's other new-tab links.
    expect(link.rel).toContain("noopener");
    expect(link.rel).toContain("noreferrer");
  });

  test("an agent with no editPath offers no link", () => {
    const data: SettingsOverviewResponse = { agents: [{ name: "evener" }] };
    render(<AgentsSection sectionId="agents" useOverview={fixture({ data })} />);
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.queryByRole("link")).toBeNull();
  });

  test("renders every agent by name", () => {
    const data: SettingsOverviewResponse = { agents: [{ name: "evener" }, { name: "explorer" }] };
    render(<AgentsSection sectionId="agents" useOverview={fixture({ data })} />);
    expect(screen.getByText("evener")).toBeTruthy();
    expect(screen.getByText("explorer")).toBeTruthy();
  });

  test("each agent is one list row holding its name and its own open control", () => {
    const data: SettingsOverviewResponse = {
      agents: [{ name: "reviewer", editPath: "editor://open?path=/plugins/reviewer.md" }, { name: "evener" }],
    };
    render(<AgentsSection sectionId="agents" useOverview={fixture({ data })} />);
    const rows = screen.getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    const [reviewer, builtin] = rows as [HTMLElement, HTMLElement];
    expect(within(reviewer).getByText("reviewer")).toBeTruthy();
    expect(within(reviewer).getByRole("link", { name: /open in editor/i })).toBeTruthy();
    expect(within(builtin).getByText("evener")).toBeTruthy();
    expect(within(builtin).queryByRole("link")).toBeNull();
  });
});

describe("AgentsSection default overview hook", () => {
  test("reads the real settingsOverview store: fetches evener/settings/overview and renders its agents", async () => {
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    const data: SettingsOverviewResponse = { agents: [{ name: "store-agent" }] };
    fake.on("evener/settings/overview", () => data);

    render(<AgentsSection sectionId="agents" />);

    expect(await screen.findByText("store-agent")).toBeTruthy();
    expect(fake.calls).toContainEqual({ method: "evener/settings/overview", params: {} });
  });
});
