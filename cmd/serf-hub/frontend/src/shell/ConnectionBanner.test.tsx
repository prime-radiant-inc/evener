import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { SIGN_IN_PROMPT_MESSAGE } from "../auth";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import { AppwireClient, type ConnectionState } from "../protocol/client";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { InitializeResponse } from "../protocol/types.gen";
import { connectionStore } from "../stores/connection";
import { ConnectionBanner } from "./ConnectionBanner";
import { NOT_BUILT_MESSAGE } from "./chrome/webNotBuilt";

// The "defaults to constructing a real AppwireClient..." test below (and any
// other in this file that lets handleRetry's default createClient run) dials
// a REAL AppwireClient - no test client is injected there. Once "closed", it
// runs its own real setTimeout-based exponential-backoff reconnect loop
// (protocol/client.ts) that close() is the only thing that cancels - setting
// connectionStore's client to null in afterEach below detaches THIS store's
// reference but leaves that client object's own live timer running for the
// rest of the worker's life under isolate:false (same hazard App.test.tsx's
// own closeStaleClient guards against).
//
// Nulls connectionStore's client reference BEFORE closing it: connection.ts's
// own client->store state mirror only republishes while
// `connectionStore.getState().client === client` still holds (its own
// guard), and close() synchronously fires that client's "closed" state
// change. Closing while the reference is still current lets that mirror
// republish state through it, re-running THIS component's own [state,
// client]-keyed effect with a stale client reference mid-test (see
// App.test.tsx's identical fix for the same hazard against threads.ts's
// wiring).
function closeStaleClient(): void {
  const client = connectionStore.getState().client;
  if (!(client instanceof AppwireClient)) return;
  connectionStore.setState({ client: null });
  client.close();
}

const ALL_FEATURES_OFF = {
  threadList: false,
  threadTurnsList: false,
  turnStart: false,
  turnSteer: false,
  threadClear: false,
  threadShutdown: false,
  forkFromTurn: false,
  tasks: false,
  transcriptList: false,
  modelList: false,
  directoryComplete: false,
  auth: false,
};

// Every "closed" render kicks off checkAuthStatus() + checkWebNotBuilt()
// (both fetch "/" - see ../auth.ts and ./chrome/webNotBuilt.ts), so any
// test rendering that state needs fetch stubbed or it would attempt a real
// network call. 200 satisfies both "authenticated" and "not not-built" in
// one stub, matching a healthy hub - the generic-closed describe block
// below relies on exactly that; other blocks override it per case.
beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 200 })));
});

afterEach(() => {
  cleanup();
  closeStaleClient();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  // Whichever earlier test file in this isolate:false worker first imported
  // AppShell.tsx armed notifications/index.ts's connectionStore subscriber
  // (its "reconnect" detector, sawReady) for the rest of the worker's life -
  // this file has SEVERAL tests above (and in "clicking Retry" below) that
  // each connect a FRESH client straight to "ready", the exact transition
  // that detector watches for. Left unreset, the first such test here arms
  // sawReady and every later one in this file reads as a spurious reconnect,
  // firing an extra, unscripted treeStore.refresh() -> fetch("/api/tree")
  // that can consume one of a later test's own scripted fetchMock slots (the
  // "clicking Retry > a retry re-probes..." flake this reset fixes - see
  // AppShell.test.tsx's identical reset for the fuller writeup). Reset+reinit
  // (not just reset) for the same reason as there: initNotifications() only
  // ever fires once per worker, so leaving it merely reset would starve
  // every later file's own attention/title wiring for the rest of the run.
  // Run before vi.unstubAllGlobals() below so initNotifications()'s baseline
  // ensureLoaded() fetch still hits a stub (this test's own, still active)
  // instead of a real network call.
  resetNotificationsForTests();
  initNotifications();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const QUIET_STATES: ConnectionState[] = ["idle", "connecting", "ready"];

for (const state of QUIET_STATES) {
  test(`renders nothing while connection state is "${state}"`, () => {
    const { container } = render(<ConnectionBanner state={state} />);
    expect(container.textContent).toBe("");
  });
}

describe('state "reconnecting"', () => {
  test("shows a quiet, informative message", () => {
    render(<ConnectionBanner state="reconnecting" />);
    expect(screen.getByText(/reconnecting to the server/i)).toBeTruthy();
  });

  // The client's OWN auto-reconnect (protocol/client.ts's exponential
  // backoff) is already handling this - "Retry now" is a quiet nudge that
  // short-circuits the current wait via AppwireClient.retryNow(), not a
  // second, independent reconnect mechanism racing the one already in
  // flight (retryNow() itself is a no-op while a dial it started, or the
  // backoff timer's own, is already in flight - see
  // protocol/reconnect.test.ts).
  test('offers a quiet "Retry now" action, wired to the currently-connected client', async () => {
    const fake = new FakeClient("reconnecting");
    connectionStore.getState().connect(fake);
    const user = userEvent.setup();

    render(<ConnectionBanner state="reconnecting" />);
    await user.click(screen.getByRole("button", { name: "Retry now" }));

    expect(fake.retryNowCalls).toBe(1);
  });

  // connectionStore's wired client can be SWAPPED mid-session (this very
  // component's own handleRetry does exactly that from "closed", below) -
  // "Retry now" must reach whichever client is wired at CLICK time, not one
  // captured when this component first mounted or rendered.
  test('"Retry now" targets whichever client is currently wired, not a stale one from an earlier render', async () => {
    const stale = new FakeClient("closed");
    connectionStore.getState().connect(stale);
    const { rerender } = render(<ConnectionBanner state="closed" />);

    const fresh = new FakeClient("reconnecting");
    connectionStore.getState().connect(fresh);
    rerender(<ConnectionBanner state="reconnecting" />);

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Retry now" }));

    expect(fresh.retryNowCalls).toBe(1);
    expect(stale.retryNowCalls).toBe(0);
  });
});

describe('state "closed" - generic (neither probe matches)', () => {
  test("shows the generic message immediately (not gated behind the probe), and it stays that way once both probes resolve without a match", async () => {
    render(<ConnectionBanner state="closed" />);
    expect(screen.getByText("Connection closed.")).toBeTruthy(); // synchronous: no dead spinner while probing
    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalledTimes(2)); // let checkAuthStatus + checkWebNotBuilt settle before the test ends
    expect(screen.getByText("Connection closed.")).toBeTruthy();
  });

  test('offers a "Retry" action', () => {
    render(<ConnectionBanner state="closed" />);
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  });
});

describe('state "closed" - unauthenticated (401)', () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 401 })));
  });

  test("shows the sign-in prompt instead of the generic closed message", async () => {
    render(<ConnectionBanner state="closed" />);
    await screen.findByText(SIGN_IN_PROMPT_MESSAGE);
    expect(screen.queryByText("Connection closed.")).toBeNull();
  });

  test("still offers Retry - the user may authorize in another tab and come back", async () => {
    render(<ConnectionBanner state="closed" />);
    await screen.findByText(SIGN_IN_PROMPT_MESSAGE);
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  });
});

describe('state "closed" - web app not built (503)', () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 503 })));
  });

  test("shows the not-built message instead of the generic closed message", async () => {
    render(<ConnectionBanner state="closed" />);
    await screen.findByText(NOT_BUILT_MESSAGE);
    expect(screen.queryByText("Connection closed.")).toBeNull();
  });
});

// window.location.reload is explicitly NOT used anywhere in this file's
// retry path (the previous design's stopgap - see git history) -
// AppwireClient.connect() caches a single connectPromise for its whole
// lifetime and never resets it, so retrying by calling connect() again on
// the SAME (dead) client just replays its original rejection. The fix is a
// FRESH client every retry - proven throughout this describe block via
// connectionStore.getState().client identity, never via a reload spy.
describe("clicking Retry", () => {
  test("constructs a fresh client, connects it, and wires it into connectionStore", async () => {
    const original = new FakeClient("closed");
    connectionStore.getState().connect(original);
    const fresh = new FakeClient("ready");

    const user = userEvent.setup();
    render(<ConnectionBanner state="closed" createClient={() => fresh} />);
    await user.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(connectionStore.getState().client).toBe(fresh));
    expect(connectionStore.getState().client).not.toBe(original);
    expect(connectionStore.getState().state).toBe("ready");
  });

  test("never calls window.location.reload", async () => {
    const reloadSpy = vi.fn();
    vi.stubGlobal("location", { ...window.location, reload: reloadSpy });
    const fresh = new FakeClient("ready");

    const user = userEvent.setup();
    render(<ConnectionBanner state="closed" createClient={() => fresh} />);
    await user.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(connectionStore.getState().client).toBe(fresh));
    expect(reloadSpy).not.toHaveBeenCalled();
  });

  test("still wires the fresh client into connectionStore even when its own connect() rejects", async () => {
    const fresh = new FakeClient("closed");
    fresh.scriptConnect(() => {
      throw new Error("handshake failed");
    });

    const user = userEvent.setup();
    render(<ConnectionBanner state="closed" createClient={() => fresh} />);
    await user.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(connectionStore.getState().client).toBe(fresh));
    expect(connectionStore.getState().state).toBe("closed");
  });

  test("populates serverInfo from the fresh client's connect() response, same as AppShell's own initial boot", async () => {
    const fresh = new FakeClient("ready");
    const scripted: InitializeResponse = {
      serverInfo: { name: "serf-hub-retry-test", version: "9.9.9" },
      protocolVersion: "1",
      sourceId: "src_retry",
      features: ALL_FEATURES_OFF,
    };
    fresh.scriptConnect(() => scripted);

    const user = userEvent.setup();
    render(<ConnectionBanner state="closed" createClient={() => fresh} />);
    await user.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => {
      expect(connectionStore.getState().serverInfo).toEqual({ name: "serf-hub-retry-test", version: "9.9.9" });
    });
  });

  test("disables the button while a retry is in flight", async () => {
    const fresh = new FakeClient("ready");
    fresh.scriptConnect(() => new Promise<InitializeResponse>(() => {})); // never resolves - holds "in flight" for this test's assertion window

    const user = userEvent.setup();
    render(<ConnectionBanner state="closed" createClient={() => fresh} />);
    await user.click(screen.getByRole("button", { name: "Retry" }));

    expect(screen.getByRole("button").hasAttribute("disabled")).toBe(true);
  });

  // Mirrors AppShell.tsx's own real-vs-injected split and its identical
  // MODE === "test" rationale: jsdom implements a global WebSocket that
  // would otherwise dial the page's own origin for real. This proves the
  // DEFAULT factory (no createClient override, the production path
  // AppShell's unmodified call site exercises) really does construct a
  // real AppwireClient - not silently a fake forever.
  test("defaults to constructing a real AppwireClient when createClient is not overridden", async () => {
    const user = userEvent.setup();
    render(<ConnectionBanner state="closed" />);
    await user.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(connectionStore.getState().client).toBeInstanceOf(AppwireClient));
  });

  // Holding `state` fixed at "closed" throughout isolates the mechanic this
  // proves - connectionStore's CLIENT reference changing is what re-triggers
  // the closed-reason probe, not the state STRING (which a real retry can
  // leave unchanged, e.g. the WS layer recovers while auth or build status
  // is what actually changed) - see ConnectionBanner.tsx's effect dependency
  // array. In the real app AppShell re-renders with a fresh `state` prop too
  // whenever connectionStore.state changes; this test's fixed prop is a
  // deliberate simplification to isolate the OTHER trigger.
  test("a retry re-probes the closed reason even though the state prop string does not change", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(null, { status: 401 })) // initial closed: auth check
      .mockResolvedValueOnce(new Response(null, { status: 401 })) // initial closed: not-built check
      .mockResolvedValueOnce(new Response(null, { status: 200 })) // post-retry: auth check
      .mockResolvedValueOnce(new Response(null, { status: 200 })); // post-retry: not-built check
    vi.stubGlobal("fetch", fetchMock);
    const fresh = new FakeClient("ready");

    render(<ConnectionBanner state="closed" createClient={() => fresh} />);
    await screen.findByText(SIGN_IN_PROMPT_MESSAGE);

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Retry" }));

    await screen.findByText("Connection closed.");
    expect(screen.queryByText(SIGN_IN_PROMPT_MESSAGE)).toBeNull();
    expect(fetchMock).toHaveBeenCalledTimes(4);
  });
});
