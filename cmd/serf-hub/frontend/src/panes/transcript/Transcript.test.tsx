import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { lazy } from "react";
import { afterAll, afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../protocol/types.gen";
import { ClientProvider } from "../../shell/clientContext";
import { registerPaneForTests } from "../../shell/paneRegistry";
import { registerDockviewApi, resetWorkspaceStoreForTests } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { resetThreadsStoreForTests } from "../../stores/threads";
import Transcript from "./Transcript";

// A minimal, test-only "session" pane registration - mirrors
// subagentModule.test.tsx's own precedent: real registerPane/paneFor/openPane
// machinery, without pulling in the actual (heavier) panes/session module.
afterAll(
  registerPaneForTests({
    id: "session",
    title: () => "test session",
    component: lazy(() => Promise.resolve({ default: () => null })),
  }),
);

// Full capability set: the read-only pane must ignore all of it (no composer/
// controls), so the fixture is deliberately permissive - a read-only render
// that leaked a control would still have every capability enabled to leak.
const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

// jsdom performs no real layout (offsetHeight is 0, no ResizeObserver), so
// @tanstack/react-virtual sees a 0px viewport and renders no rows - the same
// stub Session.test.tsx / the VirtualList suite use to exercise real rows.
const CONTAINER_HEIGHT = 500;
let offsetHeightDescriptor: PropertyDescriptor | undefined;

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: CONTAINER_HEIGHT });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  registerDockviewApi(null); // never leak a fake dockview host to another test
  if (offsetHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  }
  // The beforeEach above only resets threadsStore/workspaceStore BEFORE each
  // test - nothing restores them after the LAST test, so a pane this file
  // opened (pointing at a tracked "ref_parent" ref) stays open and focused
  // for whichever file runs next under isolate:false.
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
});

test("shows a loading placeholder before the thread hydrates", async () => {
  const fake = connectFakeClient();
  const box: { resolve: ((r: ThreadReadResponse) => void) | null } = { resolve: null };
  fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (box.resolve = resolve)));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  expect(screen.getByText(/loading transcript/i)).toBeTruthy();
  for (let i = 0; i < 20 && box.resolve === null; i += 1) await Promise.resolve();
  box.resolve?.(readResponse("ref_a"));
  await waitFor(() => expect(screen.queryByText(/loading transcript/i)).toBeNull());
});

test('shows "no turns yet" for a thread with an empty transcript', async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText(/no turns yet/i)).toBeTruthy());
});

test("renders the thread's turns through the shared VirtualList/TurnBlock engine", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_1",
              turnId: "turn_1",
              type: "userMessage",
              text: "hi from the observed thread",
              status: "completed",
            },
          ],
        },
      ],
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByTestId("turn-block")).toBeTruthy());
  expect(screen.getByText("hi from the observed thread")).toBeTruthy();
});

test("is read-only: renders no composer and no session-chrome footer, even for a fully capable thread", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "read me", status: "completed" }],
        },
      ],
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("read me")).toBeTruthy());
  // No composer input to type into...
  expect(screen.queryByRole("textbox")).toBeNull();
  // ...and no PaneScaffold footer (the session pane's only footer is its
  // SessionChrome; the read-only pane passes none).
  expect(screen.queryByTestId("pane-footer")).toBeNull();
});

test("falls back to the raw ref as the pane title when the thread has no name", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("ref_a")).toBeTruthy());
});

// --- "Open this shell transcript in a pane": a "job:<id>" ref is a shell
// job's output log, not a thread. The pane serves it through serf/jobs/output
// against the owning session (parentRef), never through thread/read. --------

test("a job: ref renders the shell job's output log via serf/jobs/output, never thread/read", async () => {
  const fake = connectFakeClient();
  fake.on("serf/jobs/output", () => ({
    data: { tail: "hello from the job", totalBytes: 18, retainedStart: 0, truncated: false },
  }));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("hello from the job")).toBeTruthy());
  const outputCalls = fake.calls.filter((call) => call.method === "serf/jobs/output");
  expect(outputCalls).toHaveLength(1);
  expect(outputCalls[0]?.params).toEqual({ ref: "ref_parent", jobId: "job_x" });
  expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(0);
});

test("job output renders ANSI SGR sequences as styled runs, not literal escape text", async () => {
  const fake = connectFakeClient();
  fake.on("serf/jobs/output", () => ({
    data: { tail: "plain \u001b[32m283 passed\u001b[39m done", totalBytes: 40, retainedStart: 0, truncated: false },
  }));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("283 passed").closest('[data-ansi-fg="green"]')).toBeTruthy());
  // The escape sequences themselves are consumed, never shown as text.
  expect(screen.getByTestId("joblog-content").textContent).toBe("plain 283 passed done");
});

test("a truncated job log says how much of the output is shown", async () => {
  const fake = connectFakeClient();
  fake.on("serf/jobs/output", () => ({
    data: { tail: "tail end", totalBytes: 70000, retainedStart: 4464, truncated: true },
  }));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("tail end")).toBeTruthy());
  expect(screen.getByText(/showing the last 65,?536 of 70,?000 bytes/i)).toBeTruthy();
  // No hasEarlier field (an older daemon's shape) means no paging affordance:
  // the note alone carries the truncation, exactly as before paging existed.
  expect(screen.queryByRole("button", { name: /load earlier/i })).toBeNull();
});

test("load earlier pages backwards through the job log until the head", async () => {
  const fake = connectFakeClient();
  fake.on("serf/jobs/output", (params) => {
    const before = (params as { beforeBytes?: number }).beforeBytes;
    if (before === undefined) {
      return { data: { tail: "6789", totalBytes: 10, retainedStart: 6, truncated: true, hasEarlier: true } };
    }
    if (before === 6) {
      return { data: { tail: "2345", totalBytes: 10, retainedStart: 2, truncated: true, hasEarlier: true } };
    }
    if (before === 2) {
      return { data: { tail: "01", totalBytes: 10, retainedStart: 0, truncated: true, hasEarlier: false } };
    }
    throw new Error(`unexpected beforeBytes ${before}`);
  });

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByTestId("joblog-content").textContent).toBe("6789"));

  fireEvent.click(screen.getByRole("button", { name: /load earlier/i }));
  await waitFor(() => expect(screen.getByTestId("joblog-content").textContent).toBe("23456789"));
  expect(screen.getByText(/showing the last 8 of 10 bytes/i)).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: /load earlier/i }));
  await waitFor(() => expect(screen.getByTestId("joblog-content").textContent).toBe("0123456789"));
  // The whole log is on screen: the button and the truncation note both go away.
  expect(screen.queryByRole("button", { name: /load earlier/i })).toBeNull();
  expect(screen.queryByText(/showing the last/i)).toBeNull();

  const calls = fake.calls.filter((call) => call.method === "serf/jobs/output");
  expect(calls.map((call) => (call.params as { beforeBytes?: number }).beforeBytes)).toEqual([undefined, 6, 2]);
});

test("a daemon that ignores beforeBytes stops paging instead of duplicating the tail", async () => {
  const fake = connectFakeClient();
  // Every request returns the same tail window, as a daemon that predates
  // beforeBytes would.
  fake.on("serf/jobs/output", () => ({
    data: { tail: "6789", totalBytes: 10, retainedStart: 6, truncated: true, hasEarlier: true },
  }));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByTestId("joblog-content").textContent).toBe("6789"));
  fireEvent.click(screen.getByRole("button", { name: /load earlier/i }));
  await waitFor(() => expect(screen.queryByRole("button", { name: /load earlier/i })).toBeNull());
  expect(screen.getByTestId("joblog-content").textContent).toBe("6789");
});

test("a job with no output yet says so instead of rendering an empty log", async () => {
  const fake = connectFakeClient();
  fake.on("serf/jobs/output", () => ({
    data: { tail: "", totalBytes: 0, retainedStart: 0, truncated: false },
  }));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText(/no output yet/i)).toBeTruthy());
});

test("a job: ref without a parentRef reports the transcript unavailable and issues no request", async () => {
  const fake = connectFakeClient();

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText(/unavailable/i)).toBeTruthy());
  expect(fake.calls.filter((call) => call.method === "serf/jobs/output")).toHaveLength(0);
});

test("the job log's refresh action refetches the tail", async () => {
  const fake = connectFakeClient();
  let calls = 0;
  fake.on("serf/jobs/output", () => ({
    data: { tail: `tail ${++calls}`, totalBytes: 6, retainedStart: 0, truncated: false },
  }));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("tail 1")).toBeTruthy());
  fireEvent.click(screen.getByRole("button", { name: /refresh/i }));
  await waitFor(() => expect(screen.getByText("tail 2")).toBeTruthy());
});

test("a failed job-output read surfaces the error, not a spinner forever", async () => {
  const fake = connectFakeClient();
  fake.on("serf/jobs/output", () => Promise.reject(new Error("job not found: job_x")));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText(/job not found: job_x/i)).toBeTruthy());
});
