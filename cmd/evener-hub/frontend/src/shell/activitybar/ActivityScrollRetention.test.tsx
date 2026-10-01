import { activityNodeID, buildWatchRows } from "@evener/appwire-client";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { MotionProvider } from "../../motion";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import {
  activityClient,
  activityContext,
  activityDelegate,
  activityJob,
  activityWatch,
} from "../../stores/sessionActivityTestUtils";
import { resetDisclosureStoreForTests, setDisclosureOpen } from "../../widgets/disclosure/disclosureStore";
import { installFocusedScope } from "../statusbar/scopeTestUtils";
import { resetWorkspaceStoreForTests } from "../workspace";
import { ActivitySidebar } from "./ActivitySidebar";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitySidebarStore";

const ref = "remote:owner";
const ROW_HEIGHT = 48;
const VIEW_HEIGHT = 100;
const jobs = Array.from({ length: 30 }, (_, index) =>
  activityJob({ jobId: `job-${index}`, description: `History ${index}` }),
);
const target = jobs[22];
if (!target) throw new Error("fixture target missing");
const anchor = { id: activityNodeID({ ...target, kind: "shell" }), offset: -12 };

function viewport(): HTMLElement {
  const body = screen.getByTestId("activity-sidebar").lastElementChild;
  if (!(body instanceof HTMLElement)) throw new Error("activity body missing");
  return body;
}

function rows(body: HTMLElement) {
  return [...body.querySelectorAll<HTMLButtonElement>("button")].filter((row) => row.textContent?.includes("History"));
}

// jsdom has no layout or visibility engine. Supply those browser boundaries,
// keeping real sidebar components, store lifetimes and typed transport reads.
function installGeometry() {
  const nativeRect = HTMLElement.prototype.getBoundingClientRect;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    const body = this.closest('[data-testid="activity-sidebar"]')?.lastElementChild;
    if (!(body instanceof HTMLElement)) return nativeRect.call(this);
    if (this === body) return new DOMRect(0, 0, 320, VIEW_HEIGHT);
    const index = rows(body).indexOf(this as HTMLButtonElement);
    const top = (index >= 0 ? index : rows(body).length) * ROW_HEIGHT - body.scrollTop;
    return new DOMRect(0, top, 320, ROW_HEIGHT);
  });
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(VIEW_HEIGHT);
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(function (this: HTMLElement) {
    return rows(this).length * ROW_HEIGHT + ROW_HEIGHT;
  });
  const tops = new WeakMap<HTMLElement, number>();
  vi.spyOn(HTMLElement.prototype, "scrollTop", "get").mockImplementation(function (this: HTMLElement) {
    return tops.get(this) ?? 0;
  });
  vi.spyOn(HTMLElement.prototype, "scrollTop", "set").mockImplementation(function (this: HTMLElement, value) {
    tops.set(this, Math.max(0, Math.min(value, this.scrollHeight - this.clientHeight)));
  });
}

class Visibility {
  static observers: Visibility[] = [];
  private callback: IntersectionObserverCallback;
  target?: Element;
  constructor(callback: IntersectionObserverCallback) {
    this.callback = callback;
    Visibility.observers.push(this);
  }
  observe(target: Element) {
    this.target = target;
  }
  disconnect() {}
  emit(visible: boolean) {
    this.callback(
      [{ target: this.target, isIntersecting: visible } as IntersectionObserverEntry],
      this as unknown as IntersectionObserver,
    );
  }
  static latest() {
    const observer = Visibility.observers.at(-1);
    if (!observer) throw new Error("no active page boundary observer");
    return observer;
  }
}

function mount() {
  return render(
    <MotionProvider>
      <ActivitySidebar />
    </MotionProvider>,
  );
}

function resetLive() {
  cleanup();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests({ preserveStorage: true });
  resetDisclosureStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
  Visibility.observers = [];
}

function prepareRetainedAnchor() {
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("jobs");
  activitySidebarStore.getState().setCategoryView(ref, "jobs", { anchor });
}

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  installGeometry();
  vi.stubGlobal("IntersectionObserver", Visibility);
});
afterEach(() => {
  resetLive();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

test("a real scroll survives reload and restores its semantic row through fresh paced page visibility", async () => {
  const warm = activityClient();
  warm.on("evener/thread/jobs/list", () => ({
    context: activityContext(),
    scope: "session",
    jobs,
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(warm);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("jobs");
  mount();
  await screen.findByRole("button", { name: /History 22/ });
  viewport().scrollTop = 22 * ROW_HEIGHT + 12;
  fireEvent.scroll(viewport());
  resetLive();

  const cold = activityClient();
  const inserted = activityJob({ jobId: "new", description: "History inserted" });
  const current = [inserted, ...jobs];
  cold.on("evener/thread/jobs/list", ({ cursor }) => {
    const offset = cursor ? Number(cursor) : 0;
    return {
      context: activityContext(),
      scope: "session",
      jobs: current.slice(offset, offset + 10),
      page: { complete: offset >= 20, issues: [], ...(offset < 20 ? { nextCursor: String(offset + 10) } : {}) },
    };
  });
  connectionStore.getState().connect(cold);
  installFocusedScope(ref);
  mount();
  await screen.findByRole("button", { name: /History inserted/ });
  expect(viewport().scrollTop).toBeGreaterThan(0);
  expect(cold.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(1);
  const firstBoundary = Visibility.latest();
  await act(async () => firstBoundary.emit(true));
  await screen.findByRole("button", { name: /History 18/ });
  expect(cold.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(2);
  act(() => firstBoundary.emit(true));
  expect(cold.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(2);
  await act(async () => Visibility.latest().emit(true));
  const restored = await screen.findByRole("button", { name: /History 22/ });
  expect(restored.getBoundingClientRect().top).toBe(-12);
  expect(viewport().scrollTop).toBe(23 * ROW_HEIGHT + 12);
  expect(cold.calls.filter((call) => call.method === "evener/thread/jobs/list").map((call) => call.params)).toEqual([
    { ref, scope: "session" },
    { ref, scope: "session", cursor: "10" },
    { ref, scope: "session", cursor: "20" },
  ]);
});

test("reload retains a clamped anchor until existing page demand supplies its trailing extent", async () => {
  prepareRetainedAnchor();
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ cursor }) => ({
    context: activityContext(),
    scope: "session",
    jobs: cursor ? jobs.slice(23) : jobs.slice(0, 23),
    page: { complete: Boolean(cursor), issues: [], ...(!cursor ? { nextCursor: "remaining" } : {}) },
  }));
  connectionStore.getState().connect(client);
  mount();
  const row = await screen.findByRole("button", { name: /History 22/ });
  expect(row.getBoundingClientRect().top).toBe(4);
  expect(viewport().scrollTop).toBe(viewport().scrollHeight - VIEW_HEIGHT);
  fireEvent.scroll(viewport());
  await act(async () => Visibility.latest().emit(true));
  await screen.findByRole("button", { name: /History 29/ });
  expect(row.getBoundingClientRect().top).toBe(anchor.offset);
  expect(client.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(2);
});

test("an authoritative complete short collection settles at its available extent", async () => {
  prepareRetainedAnchor();
  const client = activityClient();
  client.on("evener/thread/jobs/list", () => ({
    context: activityContext(),
    scope: "session",
    jobs: jobs.slice(0, 23),
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  mount();
  const row = await screen.findByRole("button", { name: /History 22/ });
  expect(row.getBoundingClientRect().top).toBe(4);
  expect(viewport().scrollTop).toBe(viewport().scrollHeight - VIEW_HEIGHT);
  expect(client.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(1);
});

test.each([false, true])(
  "missing anchor clears only when the loaded collection is authoritative complete: %s",
  async (complete) => {
    prepareRetainedAnchor();
    const client = activityClient();
    client.on("evener/thread/jobs/list", () => ({
      context: activityContext(),
      scope: "session",
      jobs: jobs.slice(0, 10),
      page: { complete, issues: complete ? [] : [{ ref, code: "unavailable" }] },
    }));
    connectionStore.getState().connect(client);
    mount();
    await screen.findByRole("button", { name: /History 0/ });
    expect(activitySidebarStore.getState().views.get(ref)?.categories.jobs?.anchor).toEqual(
      complete ? undefined : anchor,
    );
    expect(client.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(1);
  },
);

test("the shared store's empty-page progress reaches the retained anchor without another paging owner", async () => {
  prepareRetainedAnchor();
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ cursor }) => ({
    context: activityContext(),
    scope: "session",
    jobs: cursor ? jobs : [],
    page: { complete: !!cursor, issues: [], ...(!cursor ? { nextCursor: "after-empty" } : {}) },
  }));
  connectionStore.getState().connect(client);
  mount();
  expect((await screen.findByRole("button", { name: /History 22/ })).getBoundingClientRect().top).toBe(-12);
  expect(client.calls.filter((call) => call.method === "evener/thread/jobs/list").map((call) => call.params)).toEqual([
    { ref, scope: "session" },
    { ref, scope: "session", cursor: "after-empty" },
  ]);
});

test("a saved anchor waits for its successful-history disclosure to hydrate on a cold complete page", async () => {
  const connect = () => {
    const client = activityClient();
    client.on("evener/thread/jobs/list", () => ({
      context: activityContext(),
      scope: "session",
      jobs: jobs.map((job) => ({ ...job, terminal: true, outcome: "success", status: "completed" })),
      page: { complete: true, issues: [] },
    }));
    connectionStore.getState().connect(client);
  };
  connect();
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("jobs");
  mount();
  fireEvent.click(await screen.findByText("30 completed jobs"));
  viewport().scrollTop = 22 * ROW_HEIGHT + 12;
  fireEvent.scroll(viewport());
  resetLive();
  connect();
  installFocusedScope(ref);
  mount();
  const restored = await screen.findByRole("button", { name: /History 22/ });
  await waitFor(() => expect(restored.getBoundingClientRect().top).toBe(-12));
});

test.each(["close", "tab", "scope"])(
  "%s cancels old view restoration and fences obsolete visibility callbacks",
  async (change) => {
    prepareRetainedAnchor();
    const client = activityClient();
    let finish: (() => void) | undefined;
    client.on("evener/thread/jobs/list", async ({ ref: requested, cursor }) => {
      if (cursor)
        await new Promise<void>((resolve) => {
          finish = resolve;
        });
      return {
        context: activityContext(requested),
        scope: "session",
        jobs: requested === ref ? (cursor ? jobs.slice(10, 20) : jobs.slice(0, 10)) : [],
        page:
          requested === ref
            ? { complete: false, nextCursor: cursor ? "last" : "next", issues: [] }
            : { complete: true, issues: [] },
      };
    });
    connectionStore.getState().connect(client);
    mount();
    await screen.findByRole("button", { name: /History 0/ });
    const obsolete = Visibility.latest();
    await act(async () => obsolete.emit(true));
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    act(() => {
      if (change === "close") activitySidebarStore.getState().close();
      if (change === "tab") activitySidebarStore.getState().setTab("watches");
      if (change === "scope") installFocusedScope("remote:other");
    });
    await act(async () => {
      finish?.();
      obsolete.emit(true);
    });
    expect(
      client.calls.filter(
        (call) => call.method === "evener/thread/jobs/list" && (call.params as { ref?: string }).ref === ref,
      ),
    ).toHaveLength(2);
    expect(activitySidebarStore.getState().views.get(ref)?.categories.jobs?.anchor).toEqual(anchor);
    if (change !== "close") expect(viewport().scrollTop).toBe(0);
  },
);

test("a scroll inside expanded watch details retains that watch instead of the next summary", async () => {
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(1400);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    const body = this.closest('[data-testid="activity-sidebar"]')?.lastElementChild;
    if (!(body instanceof HTMLElement)) return new DOMRect();
    if (this === body) return new DOMRect(0, 0, 320, VIEW_HEIGHT);
    const details = this.closest("details") ?? this.querySelector("details");
    const index = details ? [...body.querySelectorAll("details")].indexOf(details) : -1;
    const wholeRow = this === details || this.querySelector("details") !== null;
    return new DOMRect(0, Math.max(index, 0) * 900 - body.scrollTop, 320, wholeRow ? 900 : ROW_HEIGHT);
  });
  const watches = [
    activityWatch({ id: "long", note: "Long condition", outputMatch: "ready" }),
    activityWatch({ id: "next", note: "Next condition" }),
  ];
  const connect = () => {
    const client = activityClient();
    client.on("evener/thread/watches/list", () => ({
      context: activityContext(),
      scope: "session",
      watches,
      page: { complete: true, issues: [] },
    }));
    connectionStore.getState().connect(client);
  };
  connect();
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("watches");
  mount();
  fireEvent.click(await screen.findByText("Long condition"));
  viewport().scrollTop = 500;
  fireEvent.scroll(viewport());
  expect(activitySidebarStore.getState().views.get(ref)?.categories.watches?.anchor).toEqual({
    id: buildWatchRows(watches)[0]?.id,
    offset: -500,
  });
  resetLive();
  connect();
  installFocusedScope(ref);
  mount();
  await screen.findByTestId("watch-facts");
  await waitFor(() => expect(viewport().scrollTop).toBe(500));
});

test.each([false, true])(
  "retained agent anchor reveals local history only while restoration remains active; cancelled=%s",
  async (cancelled) => {
    const delegates = Array.from({ length: 30 }, (_, index) =>
      activityDelegate({
        delegateId: `delegate-${index}`,
        childRef: `remote:child-${index}`,
        description: `History agent ${index}`,
        terminal: true,
        status: "completed",
        outcome: "success",
      }),
    );
    const target = delegates[24];
    if (!target) throw new Error("agent target missing");
    installFocusedScope(ref);
    activitySidebarStore.getState().openWith("agents");
    activitySidebarStore.getState().setCategoryView(ref, "agents", {
      anchor: { id: activityNodeID({ ...target, kind: "delegate" }), offset: 0 },
      shown: 20,
    });
    setDisclosureOpen(`${ref}\0inactive-delegates`, true);
    const client = activityClient();
    let finish: (() => void) | undefined;
    client.on("evener/thread/delegates/list", async ({ cursor }) => {
      if (cursor)
        await new Promise<void>((resolve) => {
          finish = resolve;
        });
      return {
        context: activityContext(),
        scope: "session",
        delegates: cursor ? delegates.slice(10) : delegates.slice(0, 10),
        page: { complete: !!cursor, issues: [], ...(!cursor ? { nextCursor: "next" } : {}) },
      };
    });
    connectionStore.getState().connect(client);
    mount();
    await screen.findByRole("button", { name: /History agent 0/ });
    await act(async () => Visibility.latest().emit(true));
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    if (cancelled) {
      fireEvent.wheel(viewport(), { deltaY: -100 });
      viewport().scrollTop = 0;
      fireEvent.scroll(viewport());
    }
    await act(async () => finish?.());
    const targetRow = screen.queryByRole("button", { name: /History agent 24/ });
    if (cancelled) expect(targetRow).toBeNull();
    else expect(targetRow).toBeTruthy();
    expect(activitySidebarStore.getState().views.get(ref)?.categories.agents?.shown).toBe(cancelled ? 20 : 40);
  },
);

test.each(["Tab", "Shift", "Control", "Alt", "Meta", "a"])(
  "%s without activation or scrolling preserves the pending row and offset through a deferred page",
  async (key) => {
    prepareRetainedAnchor();
    const client = activityClient();
    let finish: (() => void) | undefined;
    client.on("evener/thread/jobs/list", async ({ cursor }) => {
      if (cursor)
        await new Promise<void>((resolve) => {
          finish = resolve;
        });
      return {
        context: activityContext(),
        scope: "session",
        jobs: cursor ? jobs.slice(10) : jobs.slice(0, 10),
        page: { complete: !!cursor, issues: [], ...(!cursor ? { nextCursor: "next" } : {}) },
      };
    });
    connectionStore.getState().connect(client);
    mount();
    const first = await screen.findByRole("button", { name: /History 0/ });
    await act(async () => Visibility.latest().emit(true));
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    fireEvent.keyDown(first, { key });
    await act(async () => finish?.());
    const restored = await screen.findByRole("button", { name: /History 22/ });
    expect(restored.getBoundingClientRect().top).toBe(anchor.offset);
    expect(client.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(2);
  },
);

test("keyboard activation of a history disclosure cancels its pending anchor before the deferred page arrives", async () => {
  prepareRetainedAnchor();
  setDisclosureOpen(`${ref}\0completed-jobs`, true);
  const client = activityClient();
  let finish: (() => void) | undefined;
  client.on("evener/thread/jobs/list", async ({ cursor }) => {
    if (cursor)
      await new Promise<void>((resolve) => {
        finish = resolve;
      });
    return {
      context: activityContext(),
      scope: "session",
      jobs: (cursor ? jobs.slice(10) : jobs.slice(0, 10)).map((job) => ({
        ...job,
        terminal: true,
        outcome: "success",
        status: "completed",
      })),
      page: { complete: !!cursor, issues: [], ...(!cursor ? { nextCursor: "next" } : {}) },
    };
  });
  connectionStore.getState().connect(client);
  mount();
  const disclosure = (await screen.findByText("10 completed jobs")).closest("summary");
  if (!disclosure) throw new Error("completed history disclosure missing");
  await screen.findByRole("button", { name: /History 0/ });
  await act(async () => Visibility.latest().emit(true));
  await waitFor(() => expect(finish).toBeTypeOf("function"));
  act(() => disclosure.focus());
  const user = userEvent.setup();
  await user.keyboard("{Enter}");
  // jsdom has no native summary activation; supply its keyboard-generated
  // click, as the Disclosure component's own keyboard tests do.
  fireEvent.click(disclosure, { detail: 0 });
  expect(disclosure.closest("details")?.open).toBe(false);
  expect(activitySidebarStore.getState().views.get(ref)?.categories.jobs?.anchor?.id).not.toBe(anchor.id);
  const position = viewport().scrollTop;
  await act(async () => finish?.());
  await screen.findByText("30 completed jobs");
  expect(screen.queryByRole("button", { name: /History 22/ })).toBeNull();
  expect(viewport().scrollTop).toBe(position);
  expect(disclosure.closest("details")?.open).toBe(false);
});

test.each(["wheel", "touch", "pointer", "key", "scroll"])(
  "user %s cancels pending anchor restoration while normal collection demand remains usable",
  async (gesture) => {
    prepareRetainedAnchor();
    const client = activityClient();
    let finish: (() => void) | undefined;
    client.on("evener/thread/jobs/list", async ({ cursor }) => {
      if (cursor)
        await new Promise<void>((resolve) => {
          finish = resolve;
        });
      return {
        context: activityContext(),
        scope: "session",
        jobs: cursor ? jobs.slice(10) : jobs.slice(0, 10),
        page: { complete: !!cursor, issues: [], ...(!cursor ? { nextCursor: "next" } : {}) },
      };
    });
    connectionStore.getState().connect(client);
    mount();
    await screen.findByRole("button", { name: /History 0/ });
    await act(async () => Visibility.latest().emit(true));
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    const body = viewport();
    if (gesture === "wheel") fireEvent.wheel(body, { deltaY: -100 });
    if (gesture === "touch") fireEvent.touchStart(body);
    if (gesture === "pointer") fireEvent.pointerDown(body);
    if (gesture === "key") fireEvent.keyDown(body, { key: "Home" });
    if (gesture !== "key") {
      body.scrollTop = 0;
      fireEvent.scroll(body);
    }
    const position = body.scrollTop;
    await act(async () => finish?.());
    await screen.findByRole("button", { name: /History 22/ });
    expect(body.scrollTop).toBe(position);
    expect(activitySidebarStore.getState().views.get(ref)?.categories.jobs?.anchor?.id).not.toBe(anchor.id);
  },
);
