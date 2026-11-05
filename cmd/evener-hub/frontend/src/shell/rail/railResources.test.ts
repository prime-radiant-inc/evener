import { describe, expect, test } from "vitest";
import type { RailSession } from "./railNodes";
import { compareProjectSessions } from "./railResources";

function row(overrides: Partial<RailSession>): RailSession {
  return {
    ref: "local:a",
    row_id: "navigation:local:a",
    host_id: "local",
    session_id: "a",
    title: "Session A",
    project: "Proj",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}

describe("project session order", () => {
  test("joins the current and recent tiers newest-created first", () => {
    const current = row({
      ref: "local:current",
      row_id: "navigation:local:current",
      title: "Current",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-11-05T00:00:00Z",
    });
    const recent = row({
      ref: "local:recent",
      row_id: "navigation:local:recent",
      title: "Recent",
      created_at: "2026-06-01T00:00:00Z",
      updated_at: "2026-06-02T00:00:00Z",
    });
    expect([current, recent].sort(compareProjectSessions)).toEqual([recent, current]);
  });

  test("a later update does not move a row past a newer-created one", () => {
    const olderCreated = row({
      ref: "local:older",
      row_id: "navigation:local:older",
      title: "Older",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-11-05T12:00:00Z",
    });
    const newerCreated = row({
      ref: "local:newer",
      row_id: "navigation:local:newer",
      title: "Newer",
      created_at: "2026-06-01T00:00:00Z",
      updated_at: "2026-06-01T00:00:00Z",
    });
    expect([olderCreated, newerCreated].sort(compareProjectSessions)).toEqual([newerCreated, olderCreated]);
  });

  test("falls back to updated_at for a hub without created_at", () => {
    const older = row({ row_id: "navigation:old", updated_at: "2026-01-01T00:00:00Z" });
    const newer = row({ row_id: "navigation:new", updated_at: "2026-06-01T00:00:00Z" });
    expect([older, newer].sort(compareProjectSessions)).toEqual([newer, older]);
  });
});
