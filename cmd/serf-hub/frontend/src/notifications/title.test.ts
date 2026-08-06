import type { ComponentType } from "react";
import { lazy } from "react";
import { afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import type { ThreadModel } from "../protocol/model";
import { type PaneProps, registerPane } from "../shell/paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { resetThreadsStoreForTests, threadsStore } from "../stores/threads";
import type { AttentionSummary } from "../stores/tree";
import { applyTitle, baseTitle, formatTitle } from "./title";

function summary(needsYou: number, error: number): AttentionSummary {
  return { needsYou, error, working: 0 };
}

// A real PaneTypeId ("doc"), fixture component, title driven off the same
// threadName ctx DockHost builds (DockHost.tsx:210) so baseTitle's own ctx
// wiring is exercised, not stubbed.
beforeAll(() => {
  registerPane<{ ref?: string }>({
    id: "doc",
    title: (params, ctx) => ctx.threadName?.(params.ref ?? "") ?? "New session",
    component: lazy(() => new Promise<{ default: ComponentType<PaneProps<{ ref?: string }>> }>(() => {})),
  });
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  document.title = "";
});
afterEach(() => {
  document.title = "";
});

describe("formatTitle", () => {
  // "(<needsYou + error>) " prefix, ONLY when that sum > 0 AND the title pref
  // is on (notifications.js:118-123).
  test("title on, count > 0: prepends the sum of needsYou + error", () => {
    expect(formatTitle("serf hub", summary(2, 1), true)).toBe("(3) serf hub");
  });
  test("title on, count 0: no prefix", () => {
    expect(formatTitle("serf hub", summary(0, 0), true)).toBe("serf hub");
  });
  test("title on, null summary: no prefix", () => {
    expect(formatTitle("Settings · serf hub", null, true)).toBe("Settings · serf hub");
  });

  // THE all-OFF trap (schedule-W6 #4): title pref OFF must NEVER prefix a
  // count, no matter how high. A mutation resurrecting the legacy's
  // title-default-TRUE would prepend "(5)" here and fail.
  test("title OFF: no prefix even with a high count", () => {
    expect(formatTitle("serf hub", summary(3, 2), false)).toBe("serf hub");
  });
});

describe("baseTitle", () => {
  test("no focused pane: bare 'serf hub'", () => {
    expect(baseTitle()).toBe("serf hub");
  });

  test("a focused pane: '<pane title> · serf hub' (the honest divergence)", () => {
    workspaceStore.getState().openPane("doc", {});
    expect(baseTitle()).toBe("New session · serf hub");
  });

  test("a session pane resolves its live name via the threadName ctx", () => {
    threadsStore.setState({
      threads: new Map([["local:r1", { name: "Fix the parser", turns: [] } as unknown as ThreadModel]]),
    });
    workspaceStore.getState().openPane("doc", { ref: "local:r1" });
    expect(baseTitle()).toBe("Fix the parser · serf hub");
  });
});

describe("applyTitle", () => {
  test("writes the composed document.title", () => {
    workspaceStore.getState().openPane("doc", {});
    applyTitle(true, summary(2, 0));
    expect(document.title).toBe("(2) New session · serf hub");
  });

  test("title OFF writes only the base", () => {
    workspaceStore.getState().openPane("doc", {});
    applyTitle(false, summary(2, 0));
    expect(document.title).toBe("New session · serf hub");
  });
});
