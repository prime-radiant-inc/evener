import type { NavigationSessionSummary } from "@evener/appwire-client";
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
import { sessionReadProgress } from "../../stores/readProgress";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { resetToastStoreForTests } from "../../widgets/toast/store";
import { toastKinds } from "../../widgets/toast/toastTestUtils";
import { resetActivitySidebarStoreForTests } from "../activitybar/activitySidebarStore";
import { ClientProvider } from "../clientContext";
import { NeedsYouProvider } from "../NeedsYouProvider";
import { resetWorkspaceStoreForTests } from "../workspace";
import { liveBandRefs, railLiveBands } from "./liveBands";
import { Rail } from "./Rail";

beforeEach(() => {
  localStorage.clear();
  sessionReadProgress.reset();
  resetNavigationStoreForTests();
  resetPrefsStoreForTests();
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
  resetToastStoreForTests();
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
// A needs-you section served page by page, offset -> rows and what the hub
// said was left after them. `fail` scripts that page's read to be refused by
// the hub, the way an RPC error arrives. Absent, every section answers one
// page with nothing remaining.
type NeedsYouPage = { sessions: NavigationSessionSummary[]; remaining: number; fail?: boolean };
type NeedsYouPages = ReadonlyMap<number, NeedsYouPage>;
async function mountRail(
  initial: { live: NavigationSessionSummary[]; needsYou?: NavigationSessionSummary[] },
  props: { revealTarget?: string; onRevealConsumed?: () => void } = {},
  // The phone renders the rail inside the Sessions drawer, itself a modal.
  frame: (rail: ReactElement) => ReactElement = (rail) => rail,
  needsYouPages?: NeedsYouPages,
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
          needs_you: {
            count: needsYouPages
              ? [...needsYouPages.values()].reduce((total, page) => total + page.sessions.length, 0)
              : hub.needsYou.length,
          },
          pin_sections: { count: 0 },
        },
      });
    } else if (params.resource === "section") {
      const page = params.section === "needs_you" ? needsYouPages?.get(params.offset ?? 0) : undefined;
      if (page?.fail) throw new Error("the hub refused the page");
      data = page
        ? { sessions: page.sessions, remaining: page.remaining }
        : { sessions: params.section === "live" ? hub.live : hub.needsYou, remaining: 0 };
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
      // A section the manifest counts empty is never read at all, and a
      // paged one holds more than its first page: every loaded page counts.
      for (const resource of state.resources.values()) {
        if (resource.key.kind !== "section") continue;
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
  // A dropped socket. `whileDown` runs with the client disconnected, which is
  // where the hub can move on unseen; the reconnect that follows carries the
  // hub's newer sequence, so the store re-reads every page it already holds
  // and the rows must show what the hub says now, not what the client kept.
  const dropConnection = async (whileDown: () => void = () => {}) => {
    await act(async () => {
      client.emitStateChange("reconnecting");
    });
    whileDown();
    revision++;
    await act(async () => {
      client.emitReady({
        serverInfo: { name: "fake", version: "1" },
        protocolVersion: "evener-appwire-v6",
        sourceId: "fake",
        features: {} as never,
        // The hub's newest sequence, one behind the revision it stamps - the
        // same pair announce() emits, so a reconnect looks like any other
        // forward move of the hub's counter.
        navigation: { version: 1, generationId: "generation_test", sequence: revision - 1, readVersions: [3] },
      });
    });
    await settled();
  };
  await settled();
  return { hub, announce, dropConnection, view };
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
// The overflow row a band holds stands for rows beyond it, not for one of
// them, so it sits inside the band's own group: folding the band hides it
// with the rows it speaks for.
function overflowInBand(name: "needsYou" | "working" | "idle"): HTMLElement | null {
  const group = band(name)?.nextElementSibling;
  if (group?.getAttribute("role") !== "group") return null;
  return group.querySelector<HTMLElement>('[data-testid="rail-row-overflow"]');
}
function bandCount(name: "needsYou" | "working" | "idle"): string | null {
  const header = band(name);
  return header ? within(header).getByTestId("rail-row-band-count").textContent : null;
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

test("a working row names its running job's intent, falling back to the command", async () => {
  await mountRail({
    live: [
      session("stated", {
        state: "active",
        running_job_count: 1,
        running_job_command: "go test ./agent/...",
        running_job_intent: "Checking the rail's row tests.",
      }),
      session("unstated", { state: "active", running_job_count: 1, running_job_command: "make test" }),
    ],
  });
  const why = (ref: string) => within(rowFor(ref)).queryByTestId("rail-row-why-text");
  expect(why("local:stated")?.textContent).toBe("Checking the rail's row tests.");
  expect(why("local:stated")?.dataset.whyKind).toBe("job");
  // A job that stated no intent still names the work, so an older daemon and a
  // model that omitted the argument both keep a useful row.
  expect(why("local:unstated")?.textContent).toContain("make test");
  expect(why("local:unstated")?.dataset.whyKind).toBe("job");
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

// The hub caps a section's page at 50 rows and reports the rest as
// `remaining` (hubcore's maxSidebarSessionsPerTier). With Live's loaded pages
// not also supplying those rows, the band's own first page is all the reader
// sees unless the band offers the rest - which it does with the same quiet
// "+N older" row every other capped list on the rail uses.
const needsYouAsking = (id: string, overrides: Partial<NavigationSessionSummary> = {}) =>
  session(id, { state: "awaiting", ask_pending: true, ...overrides });

test("Needs you offers the sessions its own first page left behind", async () => {
  await mountRail(
    { live: [] },
    {},
    (rail) => rail,
    new Map([
      [0, { sessions: [needsYouAsking("a1"), needsYouAsking("a2")], remaining: 40 }],
      [2, { sessions: [needsYouAsking("a3")], remaining: 39 }],
    ]),
  );
  expect(rowsOf("needsYou")).toEqual(["local:a1", "local:a2"]);
  // The count is the rows the band shows, and the overflow row speaks for the
  // rest without counting itself as one of them.
  expect(bandCount("needsYou")).toBe("2");
  // The row itself sits in the band's group; its count and page descriptor are
  // asserted on the node in liveBands.test.ts, not read back off this copy.
  expect(overflowInBand("needsYou")).not.toBeNull();
  // No other band gains an overflow row: this one is Needs you's own.
  expect(bandNames()).toEqual(["needsYou"]);
});

test("a needs-you row from a later page stays in the band across a refresh and a reconnect", async () => {
  const pages: Map<number, NeedsYouPage> = new Map([
    [0, { sessions: [needsYouAsking("a1"), needsYouAsking("a2")], remaining: 2 }],
    [2, { sessions: [needsYouAsking("a3"), needsYouAsking("a4")], remaining: 0 }],
  ]);
  const { announce, dropConnection } = await mountRail({ live: [] }, {}, (rail) => rail, pages);
  const pageRow = () => overflowInBand("needsYou");
  // The row the reader would activate to see the rest of the tier.
  expect(pageRow()).not.toBeNull();

  // A refused read leaves the list as it was and says so through the rail's
  // one overflow toast: the navigation store resolves with the failure on the
  // resource instead of rejecting, so the row has to check it. Tested here
  // because only the rendered row proves its own wiring.
  pages.set(2, { sessions: [], remaining: 2, fail: true });
  fireEvent.click(pageRow() as HTMLElement);
  await act(async () => undefined);
  expect(toastKinds()).toEqual(["error"]);
  expect(rowsOf("needsYou")).toEqual(["local:a1", "local:a2"]);
  // The row is still there, holding the same page: another activation retries.
  expect(pageRow()).not.toBeNull();

  // The hub recovers; activating the same row again loads page two.
  pages.set(2, { sessions: [needsYouAsking("a3"), needsYouAsking("a4")], remaining: 0 });
  fireEvent.click(pageRow() as HTMLElement);
  await act(async () => undefined);
  expect(rowsOf("needsYou")).toEqual(["local:a1", "local:a2", "local:a3", "local:a4"]);
  // Loaded to the end, the row that stood for the rest is gone.
  expect(pageRow()).toBeNull();
  expect(bandCount("needsYou")).toBe("4");

  // A page-two row changes class elsewhere: the band keeps it, now as failed,
  // which the band's own order puts first.
  pages.set(2, { sessions: [needsYouAsking("a3", { state: "errored" }), needsYouAsking("a4")], remaining: 0 });
  await announce();
  expect([...rowsOf("needsYou")].sort()).toEqual(["local:a1", "local:a2", "local:a3", "local:a4"]);
  expect(within(rowFor("local:a3")).getByTestId("rail-row-why-text").dataset.hue).toBe("danger");

  // The socket drops, and the page-two row changes again while the client is
  // away. A reconnect re-reads every page the reader had loaded, so the fresh
  // state must arrive - the row is not merely the cached copy, and it is not
  // dropped back to page one.
  await dropConnection(() => {
    pages.set(2, { sessions: [needsYouAsking("a3"), needsYouAsking("a4", { state: "errored" })], remaining: 0 });
  });
  expect([...rowsOf("needsYou")].sort()).toEqual(["local:a1", "local:a2", "local:a3", "local:a4"]);
  expect(within(rowFor("local:a4")).getByTestId("rail-row-why-text").dataset.hue).toBe("danger");
  // a3 is still a needs-you row for its own reason, but it is no longer the
  // failed one: the danger hue moved to a4 with the hub's fresh copy.
  expect(within(rowFor("local:a3")).getByTestId("rail-row-why-text").dataset.hue).not.toBe("danger");
  expect(pageRow()).toBeNull();
});

test("an unread session marks its row, and reading it clears the mark without leaving Needs you", async () => {
  const unreadTurn = "2026-11-22T04:00:00.000Z";
  const readTurn = "2026-11-22T03:00:00.000Z";
  const { hub, announce } = await mountRail({
    live: [
      session("new", { state: "awaiting", ask_pending: true, turn_ended_at: unreadTurn, unseen: true }),
      session("read", { state: "awaiting", ask_pending: true, turn_ended_at: readTurn }),
    ],
  });

  // Two sessions that need a person; only one has a reply the browser has not read.
  expect(within(rowFor("local:new")).getByTestId("rail-row-unread")).toBeTruthy();
  expect(within(rowFor("local:read")).queryByTestId("rail-row-unread")).toBeNull();

  // Opening the session records its read mark at once, before the hub's rows
  // catch up. The needs-you band does not move: reading is not answering.
  const unread = hub.live[0];
  if (unread === undefined || unread.turn_ended_at === undefined) throw new Error("fixture row lost its turn end");
  const seenThrough = Date.parse(unread.turn_ended_at);
  act(() => sessionReadProgress.markRead(unread, seenThrough));
  await waitFor(() => expect(within(rowFor("local:new")).queryByTestId("rail-row-unread")).toBeNull());
  expect(rowsOf("needsYou")).toContain("local:new");

  // The hub's next build carries the mark.
  hub.live[0] = { ...unread, unseen: false };
  await announce();
  expect(within(rowFor("local:new")).queryByTestId("rail-row-unread")).toBeNull();
  expect(rowsOf("needsYou")).toContain("local:new");
});
