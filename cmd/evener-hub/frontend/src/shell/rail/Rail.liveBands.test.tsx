import type { NavigationSessionSummary } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { capability, manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { navigationInvalidatedNotification } from "@evener/appwire-client/testing/notifications";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import "../../panes/session";
import { resetConnectionStoreForTests } from "../../stores/connection";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetPrefsStoreForTests } from "../../stores/prefs";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { resetActivitySidebarStoreForTests } from "../activitybar/activitySidebarStore";
import { ClientProvider } from "../clientContext";
import { NeedsYouProvider } from "../NeedsYouProvider";
import { resetWorkspaceStoreForTests } from "../workspace";
import { liveBandRefs, railLiveBands } from "./liveBands";
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
  resetConnectionStoreForTests();
});

function session(id: string, overrides: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary {
  return {
    ref: `local:${id}`,
    host_id: "local",
    session_id: id,
    title: `Session ${id}`,
    project: "",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}

// Only the hub boundary is scripted: the Live and needs-you sections it
// answers with. Decoding, the store, the band split and the rows are the
// production code. `hub` is mutable so a test can move a session and announce
// it the way the hub does.
async function mountRail(
  initial: { live: NavigationSessionSummary[]; needsYou?: NavigationSessionSummary[] },
  props: { revealTarget?: string; onRevealConsumed?: () => void } = {},
  // The phone renders the rail inside the Sessions drawer, itself a modal.
  frame: (rail: ReactElement) => ReactElement = (rail) => rail,
) {
  const hub = { live: initial.live, needsYou: initial.needsYou ?? [] };
  let revision = 1;
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    let data: unknown;
    if (params.resource === "manifest") {
      data = manifest({
        revision,
        sources: [{ id: "local", label: "This host", kind: "local", online: true }],
        sections: {
          live: { count: hub.live.length },
          needs_you: { count: hub.needsYou.length },
          pin_sections: { count: 0 },
        },
      });
    } else if (params.resource === "section") {
      data = { sessions: params.section === "live" ? hub.live : hub.needsYou, remaining: 0 };
    } else if (params.resource === "catalog") {
      data = { projects: [], remaining: 0 };
    } else if (params.resource === "pin_catalog") {
      data = { pin_sections: [], remaining: 0 };
    } else {
      throw new Error(`Unexpected live-bands resource: ${params.resource}`);
    }
    return wireSnapshot(params, data, `"bands-${revision}"`, revision);
  });
  await client.connect();
  await act(async () => initNavigation(client, capability()));
  const view = render(
    <ClientProvider client={client}>
      <NeedsYouProvider>{frame(<Rail {...props} />)}</NeedsYouProvider>
    </ClientProvider>,
  );
  const settled = async () => {
    await waitFor(() => {
      const state = navigationStore.getState();
      expect(state.manifest?.loadedRevision).toBe(revision);
      // A section the manifest counts empty is never read at all.
      for (const section of ["live", "needs_you"] as const) {
        const resource = state.resources.get(keyID({ kind: "section", section, offset: 0, limit: 50 }));
        if (!resource) continue;
        expect(resource.loadedRevision).toBe(revision);
        expect(resource.loading).toBe(false);
      }
    });
  };
  const announce = async () => {
    await act(async () => {
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
  return { hub, announce, view };
}

function band(name: "needsYou" | "working" | "idle"): HTMLElement | null {
  const header = document.querySelector<HTMLElement>(`[data-testid="rail-row-band"][data-band="${name}"]`);
  return header?.closest<HTMLElement>('[role="treeitem"]') ?? null;
}
// The Tree widget renders a branch's rows in a group that follows its
// treeitem, so a band's rows are the refs inside that group.
function rowsOf(name: "needsYou" | "working" | "idle"): string[] {
  const group = band(name)?.nextElementSibling;
  return group?.getAttribute("role") === "group" ? refsIn(group) : [];
}
function refsIn(root: ParentNode): string[] {
  return [...root.querySelectorAll<HTMLElement>("[data-session-ref]")].map((row) => row.dataset.sessionRef ?? "");
}
function rowFor(ref: string): HTMLElement {
  const row = document.querySelector<HTMLElement>(`[data-session-ref="${ref}"]`);
  if (!row) throw new Error(`${ref} has not rendered`);
  return row;
}
function bandNames(): string[] {
  return [...document.querySelectorAll<HTMLElement>('[data-testid="rail-row-band"]')].map(
    (header) => header.dataset.band ?? "",
  );
}

const fleet = () => ({
  live: [
    session("failed", { state: "errored" }),
    session("asking", { state: "awaiting", ask_pending: true }),
    session("working", { state: "active" }),
    session("building", { state: "active", running_job_command: "make test" }),
    session("idle"),
  ],
  // The hub's needs-you section also holds a session Live's loaded page
  // does not.
  needsYou: [session("approving", { state: "active", approval_pending: true })],
});

test("Live reads top to bottom as who needs you, who is working, and who is resting", async () => {
  const { hub } = await mountRail(fleet());
  expect(bandNames()).toEqual(["needsYou", "working", "idle"]);
  expect([...rowsOf("needsYou")].sort()).toEqual(["local:approving", "local:asking", "local:failed"]);
  expect(rowsOf("needsYou")[0]).toBe("local:failed");
  expect(rowsOf("working")).toEqual(["local:working", "local:building"]);
  expect(rowsOf("idle")).toEqual(["local:idle"]);
  // The keyboard's Live order is the order on screen.
  expect(liveBandRefs(railLiveBands(hub.live, hub.needsYou))).toEqual(refsIn(document));
});

test("a row says why it is in its band only where the band and its dot have not already said it", async () => {
  await mountRail(fleet());
  const why = (ref: string) => within(rowFor(ref)).queryByTestId("rail-row-why-text");
  // Needs you: which kind of attention, in the hue of the row's own dot.
  expect(why("local:failed")?.dataset.hue).toBe("danger");
  expect(why("local:asking")?.dataset.hue).toBe("attention");
  expect(why("local:approving")?.dataset.hue).toBe("attention");
  expect(why("local:asking")?.textContent).not.toBe(why("local:approving")?.textContent);
  // Working: what it is running, when it is running something nameable.
  expect(why("local:building")?.dataset.whyKind).toBe("job");
  expect(why("local:working")).toBeNull();
  expect(why("local:idle")).toBeNull();
});

test("a session that starts needing you moves to Needs you, and back out when it is answered", async () => {
  const { hub, announce } = await mountRail({ live: [session("a", { state: "active" }), session("b")] });
  expect(bandNames()).toEqual(["working", "idle"]);

  hub.live = [session("a", { state: "awaiting", ask_pending: true }), session("b")];
  hub.needsYou = [hub.live[0] as NavigationSessionSummary];
  await announce();
  await waitFor(() => expect(bandNames()).toEqual(["needsYou", "idle"]));
  expect(rowsOf("needsYou")).toEqual(["local:a"]);

  hub.live = [session("a", { state: "active" }), session("b")];
  hub.needsYou = [];
  await announce();
  await waitFor(() => expect(bandNames()).toEqual(["working", "idle"]));
  expect(rowsOf("working")).toEqual(["local:a"]);
});

test("folding a band hides its rows, leaves the others alone, and survives a remount", async () => {
  const { view } = await mountRail(fleet());
  const header = document.querySelector<HTMLElement>('[data-testid="rail-row-band"][data-band="idle"]');
  if (!header) throw new Error("no Idle band");
  fireEvent.click(within(header).getByTestId("rail-chevron"));
  expect(band("idle")?.getAttribute("aria-expanded")).toBe("false");
  expect(document.querySelector('[data-session-ref="local:idle"]')).toBeNull();
  expect(rowsOf("working")).toEqual(["local:working", "local:building"]);
  view.unmount();
  await mountRail(fleet());
  expect(band("idle")?.getAttribute("aria-expanded")).toBe("false");
  expect(document.querySelector('[data-session-ref="local:idle"]')).toBeNull();
});

test("revealing a session inside a folded band opens the band", async () => {
  const first = await mountRail(fleet());
  const header = document.querySelector<HTMLElement>('[data-testid="rail-row-band"][data-band="idle"]');
  if (!header) throw new Error("no Idle band");
  fireEvent.click(within(header).getByTestId("rail-chevron"));
  first.view.unmount();

  const scrolled = vi.fn();
  const original = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollIntoView");
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
    configurable: true,
    writable: true,
    value: scrolled,
  });
  try {
    const consumed = vi.fn();
    await mountRail(fleet(), { revealTarget: "local:idle", onRevealConsumed: consumed });
    await waitFor(() => expect(consumed).toHaveBeenCalled());
    expect(band("idle")?.getAttribute("aria-expanded")).toBe("true");
    expect(rowFor("local:idle")).toBeTruthy();
    expect(scrolled).toHaveBeenCalled();
  } finally {
    if (original) Object.defineProperty(HTMLElement.prototype, "scrollIntoView", original);
    else delete (HTMLElement.prototype as { scrollIntoView?: unknown }).scrollIntoView;
  }
});

test("a rename in progress survives its session changing band, and the row moves once the dialog closes", async () => {
  const { hub, announce } = await mountRail({ live: [session("a", { state: "active", rename: true }), session("b")] });
  fireEvent.click(within(rowFor("local:a")).getByRole("button", { name: /Actions for/ }));
  fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
  const draft = () => screen.getByRole("textbox") as HTMLInputElement;
  fireEvent.change(draft(), { target: { value: "Half-typed name" } });

  // The turn ends on a question while the dialog is open.
  hub.live = [session("a", { state: "awaiting", ask_pending: true, rename: true }), session("b")];
  hub.needsYou = [hub.live[0] as NavigationSessionSummary];
  await announce();
  await waitFor(() => expect(within(rowFor("local:a")).queryByTestId("rail-row-why-text")).toBeTruthy());
  expect(draft().value).toBe("Half-typed name");
  expect(rowsOf("working")).toEqual(["local:a"]);

  fireEvent.keyDown(draft(), { key: "Escape" });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await waitFor(() => expect(rowsOf("needsYou")).toEqual(["local:a"]));
});

test("inside the phone's Sessions drawer, itself a modal, a session that starts needing you still moves", async () => {
  const { hub, announce } = await mountRail({ live: [session("a", { state: "active" }), session("b")] }, {}, (rail) => (
    <div role="dialog" aria-modal="true" aria-label="Sessions">
      {rail}
    </div>
  ));
  hub.live = [session("a", { state: "awaiting", ask_pending: true }), session("b")];
  hub.needsYou = [hub.live[0] as NavigationSessionSummary];
  await announce();
  await waitFor(() => expect(rowsOf("needsYou")).toEqual(["local:a"]));
});

test("a rename survives its session being the only row and leaving the sections mid-edit", async () => {
  const asking = session("only", { state: "awaiting", ask_pending: true, rename: true });
  const { hub, announce } = await mountRail({ live: [], needsYou: [asking] });
  fireEvent.click(within(rowFor("local:only")).getByRole("button", { name: /Actions for/ }));
  fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
  const draft = () => screen.getByRole("textbox") as HTMLInputElement;
  fireEvent.change(draft(), { target: { value: "Half-typed name" } });

  // Answered elsewhere: the section empties before anything else has loaded.
  hub.needsYou = [];
  await announce();
  expect(draft().value).toBe("Half-typed name");
  expect(rowFor("local:only")).toBeTruthy();
});
