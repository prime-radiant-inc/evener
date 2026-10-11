import type { NavigationSessionSummary } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { capability, manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { navigationInvalidatedNotification } from "@evener/appwire-client/testing/notifications";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import "../../panes/session";
import { connectionStore } from "../../stores/connection";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetPrefsStoreForTests } from "../../stores/prefs";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { hoverForTooltip } from "../../widgets/tooltip/tooltipTestUtils";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "../activitybar/activitySidebarStore";
import { ClientProvider } from "../clientContext";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { Rail } from "./Rail";

beforeEach(() => {
  localStorage.clear();
  resetNavigationStoreForTests();
  resetPrefsStoreForTests();
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
});

afterEach(() => {
  cleanup();
  resetNavigationStoreForTests();
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
});

test("parent status recovers through navigation updates without losing failed-child access", async () => {
  let parent: NavigationSessionSummary = {
    ref: "local:parent",
    host_id: "local",
    session_id: "parent",
    title: "Parent work",
    project: "",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    subagents: { running: 1, failed: 1, done: 0 },
  };
  let revision = 1;
  const client = new FakeClient("ready");
  // Only the hub boundary is scripted. Decoding, reconciliation, selectors,
  // row projection, menus and workspace navigation are the production code.
  client.on("evener/navigation/read", (params) => {
    let data: unknown;
    if (params.resource === "manifest") {
      data = manifest({
        revision,
        sources: [{ id: "local", label: "This host", kind: "local", online: true }],
        sections: { live: { count: 1 }, needs_you: { count: parent.ask_pending ? 1 : 0 }, pin_sections: { count: 0 } },
      });
    } else if (params.resource === "section") {
      data = { sessions: params.section === "live" || parent.ask_pending ? [parent] : [], remaining: 0 };
    } else if (params.resource === "catalog") {
      data = { projects: [], remaining: 0 };
    } else if (params.resource === "pin_catalog") {
      data = { pin_sections: [], remaining: 0 };
    } else {
      throw new Error(`Unexpected status-journey resource: ${params.resource}`);
    }
    return wireSnapshot(params, data, `"status-${revision}"`, revision);
  });
  const initialize = { ...(await client.connect()), navigation: capability() };
  await act(async () => initNavigation(client, capability()));
  render(
    <ClientProvider client={client}>
      <Rail />
    </ClientProvider>,
  );

  const row = () => {
    const found = document.querySelector<HTMLElement>('[data-session-ref="local:parent"]');
    if (!found) throw new Error("Parent work has not rendered");
    return found;
  };
  const status = async (label: string | null) => {
    await waitFor(() => {
      const rendered = within(row());
      if (label === null) expect(rendered.queryByTestId("rail-row-signal")).toBeNull();
      else expect(rendered.getByRole("img", { name: label })).toBeTruthy();
      expect(rendered.queryByRole("img", { name: "Broken" })).toBeNull();
    });
  };
  const settled = async () => {
    await waitFor(() => {
      for (const section of ["live", "needs_you"] as const) {
        const resource = navigationStore
          .getState()
          .resources.get(keyID({ kind: "section", section, offset: 0, limit: 50 }));
        if (section === "needs_you" && !parent.ask_pending && !resource) continue;
        expect(resource?.loadedRevision).toBe(revision);
        expect(resource?.loading).toBe(false);
        expect(resource?.error).toBeNull();
      }
    });
  };
  const update = async (change: Partial<NavigationSessionSummary>) => {
    await act(async () => {
      parent = { ...parent, ...change };
      revision++;
      client.emitNotification(
        navigationInvalidatedNotification({
          generationId: "generation_test",
          sequence: revision - 1,
          targets: [
            { kind: "manifest", revision },
            { kind: "section", section: "live", revision },
            { kind: "section", section: "needs_you", revision },
          ],
        }),
      );
    });
    await settled();
  };

  await settled();
  await status("Running");
  await update({ state: "awaiting", ask_pending: true });
  await status("Needs you");
  await update({ state: "idle", ask_pending: false });
  await status("Running");
  await update({ subagents: { running: 0, failed: 1, done: 1 } });
  await status(null);

  // An explicit refresh and a reconnect preserve the settled quiet status.
  await update({});
  await status(null);
  await act(async () => {
    client.emitStateChange("reconnecting");
    revision++;
    client.emitReady({ ...initialize, navigation: { ...capability(), sequence: revision - 1 } });
  });
  await settled();
  await status(null);

  const panel = hoverForTooltip(within(row()).getByTestId("rail-row-title"));
  expect(within(panel).getByText("1 failed, 1 done")).toBeTruthy();
  fireEvent.click(within(row()).getByRole("button", { name: "Actions for Parent work" }));
  fireEvent.click(screen.getByRole("menuitem", { name: "Overview" }));
  await waitFor(() => {
    expect(activitySidebarStore.getState().open).toBe(true);
    expect(activitySidebarStore.getState().ref).toBe("local:parent");
    expect(activitySidebarStore.getState().tab).toBe("agents");
  });
  expect(workspaceStore.getState().panes).toEqual(
    expect.arrayContaining([expect.objectContaining({ type: "session", params: { ref: "local:parent" } })]),
  );
});
