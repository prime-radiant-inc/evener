import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { SettingsOverviewResponse } from "../../../protocol/types.gen";
import { CodexLaunchSection } from "./launchCodex";
import type { SettingsOverviewStore } from "./overviewSeam";

afterEach(cleanup);

function fixture(overrides: Partial<SettingsOverviewStore> = {}): () => SettingsOverviewStore {
  return () => ({ data: null, loading: false, error: null, fetch: async () => {}, ...overrides });
}

describe("CodexLaunchSection", () => {
  test("calls fetch() once on mount", () => {
    const fetchFn = vi.fn().mockResolvedValue(undefined);
    render(<CodexLaunchSection sectionId="launch-codex" useOverview={fixture({ fetch: fetchFn })} />);
    expect(fetchFn).toHaveBeenCalledTimes(1);
  });

  test("empty state shows the worked [[codex_launches]] example and a restart hint", () => {
    const data: SettingsOverviewResponse = { codexLaunches: [] };
    render(<CodexLaunchSection sectionId="launch-codex" useOverview={fixture({ data })} />);
    expect(screen.getByText(/No codex launch entries configured/)).toBeTruthy();
    // "[[codex_launches]]" appears both inline (<code>) and in the worked
    // <pre> example - getAllByText tolerates however RTL groups the pre
    // block's text nodes, rather than pinning an exact match count.
    expect(screen.getAllByText(/\[\[codex_launches\]\]/).length).toBeGreaterThan(0);
    expect(screen.getByText(/Restart the hub after editing hub\.toml/)).toBeTruthy();
  });

  test("renders one heading per entry, keyed by id", () => {
    const data: SettingsOverviewResponse = {
      codexLaunches: [{ id: "primary", binary: "/usr/local/bin/codex" }, { id: "secondary" }],
    };
    render(<CodexLaunchSection sectionId="launch-codex" useOverview={fixture({ data })} />);
    expect(screen.getByRole("heading", { name: "primary" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "secondary" })).toBeTruthy();
  });

  test("applies the 4 display fallbacks when the wire value is empty", () => {
    const data: SettingsOverviewResponse = { codexLaunches: [{ id: "entry" }] };
    render(<CodexLaunchSection sectionId="launch-codex" useOverview={fixture({ data })} />);
    expect(screen.getByText("codex")).toBeTruthy();
    expect(screen.getByText("(inherited)")).toBeTruthy();
    expect(screen.getByText("ws://127.0.0.1:0")).toBeTruthy();
    expect(screen.getByText("30s")).toBeTruthy();
  });

  test("uses the configured values instead of the fallback when present", () => {
    const data: SettingsOverviewResponse = {
      codexLaunches: [
        { id: "entry", binary: "/opt/codex", workingDir: "/repo", listen: "ws://127.0.0.1:9190", timeoutMillis: 45000 },
      ],
    };
    render(<CodexLaunchSection sectionId="launch-codex" useOverview={fixture({ data })} />);
    expect(screen.getByText("/opt/codex")).toBeTruthy();
    expect(screen.getByText("/repo")).toBeTruthy();
    expect(screen.getByText("ws://127.0.0.1:9190")).toBeTruthy();
    expect(screen.getByText("45s")).toBeTruthy();
  });

  test("env keys render redacted as KEY=… regardless of actual content, and only when envKeys is non-empty", () => {
    const data: SettingsOverviewResponse = { codexLaunches: [{ id: "entry", envKeys: ["OPENAI_BASE_URL", "FOO"] }] };
    render(<CodexLaunchSection sectionId="launch-codex" useOverview={fixture({ data })} />);
    expect(screen.getByText("OPENAI_BASE_URL=…")).toBeTruthy();
    expect(screen.getByText("FOO=…")).toBeTruthy();
  });

  test("no Env row at all when envKeys is absent", () => {
    const data: SettingsOverviewResponse = { codexLaunches: [{ id: "entry" }] };
    render(<CodexLaunchSection sectionId="launch-codex" useOverview={fixture({ data })} />);
    expect(screen.queryByText("Env")).toBeNull();
  });
});
