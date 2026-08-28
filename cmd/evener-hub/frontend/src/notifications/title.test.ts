import type { ComponentType } from "react";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import type { ThreadModel } from "../protocol/model";
import type { AttentionSummary, NavigationSessionLocation } from "../protocol/types.gen";
import { type PaneProps, registerPaneForTests } from "../shell/paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { keyID } from "../stores/navigation/types";
import { resetThreadsStoreForTests, threadsStore } from "../stores/threads";
import { applyTitle, baseTitle, formatTitle } from "./title";

function summary(needsYou: number, error: number): AttentionSummary {
  return { needsYou, error, working: 0 };
}

// A real PaneTypeId ("doc"), fixture component, title driven off the same
// threadName ctx DockHost builds (DockHost.tsx:210) so baseTitle's own ctx
// wiring is exercised, not stubbed. paneRegistry.ts is a shared module
// singleton - the restorer (called in the afterAll below) puts back
// whatever "doc" resolved to before this file ran, so a later file sharing
// the same registry never inherits this fixture (whose own fallback title
// literally reads "New session", which corrupted panes/doc/register.test.ts's
// assertion on the REAL doc pane's title before this fix).
let restoreDocPane: () => void;

beforeAll(() => {
  restoreDocPane = registerPaneForTests<{ ref?: string }>({
    id: "doc",
    title: (params, ctx) => ctx.threadName?.(params.ref ?? "") ?? "New session",
    component: lazy(() => new Promise<{ default: ComponentType<PaneProps<{ ref?: string }>> }>(() => {})),
  });
});

afterAll(() => {
  restoreDocPane();
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  resetNavigationStoreForTests();
  document.title = "";
});
afterEach(() => {
  document.title = "";
});

describe("formatTitle", () => {
  // "(<needsYou + error>) " prefix, ONLY when that sum > 0 AND the title pref
  // is on (notifications.js:118-123).
  test("title on, count > 0: prepends the sum of needsYou + error", () => {
    expect(formatTitle("evener hub", summary(2, 1), true)).toBe("(3) evener hub");
  });
  test("title on, count 0: no prefix", () => {
    expect(formatTitle("evener hub", summary(0, 0), true)).toBe("evener hub");
  });
  test("title on, null summary: no prefix", () => {
    expect(formatTitle("Settings · evener hub", null, true)).toBe("Settings · evener hub");
  });

  // THE all-OFF trap (schedule-W6 #4): title pref OFF must NEVER prefix a
  // count, no matter how high. A mutation resurrecting the legacy's
  // title-default-TRUE would prepend "(5)" here and fail.
  test("title OFF: no prefix even with a high count", () => {
    expect(formatTitle("evener hub", summary(3, 2), false)).toBe("evener hub");
  });
});

describe("baseTitle", () => {
  test("no focused pane: bare 'evener hub'", () => {
    expect(baseTitle()).toBe("evener hub");
  });

  test("a focused pane: '<pane title> · evener hub' (the honest divergence)", () => {
    workspaceStore.getState().openPane("doc", {});
    expect(baseTitle()).toBe("New session · evener hub");
  });

  test("with no hydrated thread, the location summary backs the tab title", () => {
    const location: NavigationSessionLocation = {
      generation_id: "generation_test",
      revision: 1,
      ref: "local:r2",
      top_level_ref: "local:r2",
      top_level: true,
      session: {
        ref: "local:r2",
        host_id: "local",
        session_id: "local:r2",
        title: "Fix Four Open Issues",
        project: "test-project",
        state: "idle",
        kind: "session",
        live: true,
        children: [],
      },
    };
    const key = { kind: "location", ref: "local:r2" } as const;
    navigationStore.setState({
      mode: "v1",
      clientGenerationID: "generation_test",
      resources: new Map([
        [
          keyID(key),
          {
            key,
            data: location,
            loadedRevision: 1,
            targetRevision: null,
            forceToken: 0,
            etag: "etag",
            loading: false,
            stale: false,
            error: null,
            generationID: "generation_test",
          },
        ],
      ]),
    });
    workspaceStore.getState().openPane("doc", { ref: "local:r2" });
    expect(baseTitle()).toBe("Fix Four Open Issues · evener hub");
  });

  test("a session pane resolves its live name via the threadName ctx", () => {
    threadsStore.setState({
      threads: new Map([["local:r1", { name: "Fix the parser", turns: [] } as unknown as ThreadModel]]),
    });
    workspaceStore.getState().openPane("doc", { ref: "local:r1" });
    expect(baseTitle()).toBe("Fix the parser · evener hub");
  });
});

describe("applyTitle", () => {
  test("writes the composed document.title", () => {
    workspaceStore.getState().openPane("doc", {});
    applyTitle(true, summary(2, 0));
    expect(document.title).toBe("(2) New session · evener hub");
  });

  test("title OFF writes only the base", () => {
    workspaceStore.getState().openPane("doc", {});
    applyTitle(false, summary(2, 0));
    expect(document.title).toBe("New session · evener hub");
  });
});
