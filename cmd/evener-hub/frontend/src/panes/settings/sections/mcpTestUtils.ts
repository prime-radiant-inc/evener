import { vi } from "vitest";
import type { SettingsOverviewLike } from "./mcp";

/** A settings-overview hook for the MCP section that always returns one fixed
 * state: idle and empty unless `overrides` says otherwise. */
export function overviewHook(overrides: Partial<SettingsOverviewLike> = {}): () => SettingsOverviewLike {
  const state: SettingsOverviewLike = {
    data: null,
    loading: false,
    error: null,
    fetch: vi.fn(async () => {}),
    ...overrides,
  };
  return () => state;
}
