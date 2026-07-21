import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { SettingsOverviewResponse } from "../../../protocol/types.gen";
import { AgentsSection } from "./agents";
import type { SettingsOverviewStore } from "./overviewSeam";

afterEach(cleanup);

function fixture(overrides: Partial<SettingsOverviewStore> = {}): () => SettingsOverviewStore {
  return () => ({ data: null, loading: false, error: null, fetch: async () => {}, ...overrides });
}

describe("AgentsSection", () => {
  test("calls fetch() once on mount (fetch caches; the store owns re-fetch decisions)", () => {
    const fetchFn = vi.fn().mockResolvedValue(undefined);
    render(<AgentsSection sectionId="agents" useOverview={fixture({ fetch: fetchFn })} />);
    expect(fetchFn).toHaveBeenCalledTimes(1);
  });

  test("shows a loading indicator while loading", () => {
    render(<AgentsSection sectionId="agents" useOverview={fixture({ loading: true })} />);
    expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
  });

  test("shows an error message on failure", () => {
    render(<AgentsSection sectionId="agents" useOverview={fixture({ error: "network down" })} />);
    expect(screen.getByText(/Failed to load: network down/)).toBeTruthy();
  });

  test("empty state: 'No agents discovered.'", () => {
    const data: SettingsOverviewResponse = { agents: [] };
    render(<AgentsSection sectionId="agents" useOverview={fixture({ data })} />);
    expect(screen.getByText("No agents discovered.")).toBeTruthy();
  });

  test("an agent with an editPath renders an 'open in editor' link, target=_blank rel=noopener", () => {
    const data: SettingsOverviewResponse = {
      agents: [{ name: "reviewer", editPath: "editor://open?path=/plugins/reviewer.md" }],
    };
    render(<AgentsSection sectionId="agents" useOverview={fixture({ data })} />);
    const link = screen.getByRole("link", { name: /open in editor/i }) as HTMLAnchorElement;
    expect(link.href).toBe("editor://open?path=/plugins/reviewer.md");
    expect(link.target).toBe("_blank");
    expect(link.rel).toBe("noopener");
  });

  test("an agent with no editPath shows a dim 'built-in' label instead of a link", () => {
    const data: SettingsOverviewResponse = { agents: [{ name: "serf" }] };
    render(<AgentsSection sectionId="agents" useOverview={fixture({ data })} />);
    expect(screen.getByText("built-in")).toBeTruthy();
    expect(screen.queryByRole("link")).toBeNull();
  });

  test("renders every agent by name", () => {
    const data: SettingsOverviewResponse = { agents: [{ name: "serf" }, { name: "codex" }] };
    render(<AgentsSection sectionId="agents" useOverview={fixture({ data })} />);
    expect(screen.getByText("serf")).toBeTruthy();
    expect(screen.getByText("codex")).toBeTruthy();
  });
});
