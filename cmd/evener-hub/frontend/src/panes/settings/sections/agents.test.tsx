import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { SettingsOverviewResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { AgentsSection } from "./agents";
import type { SettingsOverviewStore } from "./overviewSeam";

// The repo's CSS-source pin idiom (difftable.test.tsx, select.test.tsx):
// jsdom has no layout, so placement contracts are pinned against the
// stylesheet's own source.
const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "agents.module.css"), "utf8");

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  cleanup();
});

function fixture(overrides: Partial<SettingsOverviewStore> = {}): () => SettingsOverviewStore {
  return () => ({ data: null, loading: false, error: null, fetch: async () => {}, ...overrides });
}

describe("AgentsSection", () => {
  test("calls fetch() once on mount (fetch caches; the store owns re-fetch decisions)", () => {
    // useConnectedEffect gates the mount-time fetch on the connection being
    // ready (see that hook's own doc comment) - true of the real store this
    // section will front onto once T4 lands, even though this placeholder's
    // own fetch is a no-op that doesn't itself touch the wire.
    connectionStore.setState({ state: "ready" });
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
    // error is converted via friendlyErrorMessage: raw strings like "network down" become the generic message
    expect(screen.getByText(/Failed to load: Something went wrong/)).toBeTruthy();
    // Assert the raw string no longer appears
    expect(screen.queryByText(/network down/)).toBeNull();
  });

  test("empty state: 'No agents discovered.'", () => {
    const data: SettingsOverviewResponse = { agents: [] };
    render(<AgentsSection sectionId="agents" useOverview={fixture({ data })} />);
    expect(screen.getByText("No agents discovered.")).toBeTruthy();
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

  test("an agent with no editPath shows a dim 'built-in' label instead of a link", () => {
    const data: SettingsOverviewResponse = { agents: [{ name: "evener" }] };
    render(<AgentsSection sectionId="agents" useOverview={fixture({ data })} />);
    expect(screen.getByText("built-in")).toBeTruthy();
    expect(screen.queryByRole("link")).toBeNull();
  });

  test("renders every agent by name", () => {
    const data: SettingsOverviewResponse = { agents: [{ name: "evener" }, { name: "codex" }] };
    render(<AgentsSection sectionId="agents" useOverview={fixture({ data })} />);
    expect(screen.getByText("evener")).toBeTruthy();
    expect(screen.getByText("codex")).toBeTruthy();
  });

  test("the open-in-editor anchor rides beside the agent name, not the row's far edge", () => {
    expect(css).not.toMatch(/\.row\s*\{[^}]*justify-content:\s*space-between/);
    expect(css).toMatch(/\.builtin\s*\{[^}]*margin-left:\s*auto/);
  });
});
