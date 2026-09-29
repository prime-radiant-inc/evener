import { afterEach, describe, expect, test } from "vitest";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitySidebarStore";

afterEach(() => {
  resetActivitySidebarStoreForTests();
});

describe("activitySidebarStore", () => {
  test("starts closed on the agents tab", () => {
    expect(activitySidebarStore.getState().open).toBe(false);
    expect(activitySidebarStore.getState().tab).toBe("agents");
  });

  test("openWith opens onto the given tab", () => {
    activitySidebarStore.getState().openWith("watches");
    expect(activitySidebarStore.getState().open).toBe(true);
    expect(activitySidebarStore.getState().tab).toBe("watches");
  });

  test("openWith with no tab keeps the current tab", () => {
    activitySidebarStore.getState().setTab("jobs");
    activitySidebarStore.getState().openWith();
    expect(activitySidebarStore.getState().tab).toBe("jobs");
  });

  test("close keeps the tab for the next open", () => {
    activitySidebarStore.getState().openWith("tasks");
    activitySidebarStore.getState().close();
    expect(activitySidebarStore.getState().open).toBe(false);
    activitySidebarStore.getState().openWith();
    expect(activitySidebarStore.getState().tab).toBe("tasks");
  });
});
