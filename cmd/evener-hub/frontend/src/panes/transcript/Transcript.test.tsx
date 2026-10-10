import type { Thread, ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { lazy } from "react";
import { afterAll, afterEach, beforeEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../shell/clientContext";
import { registerPaneForTests } from "../../shell/paneRegistry";
import { registerDockviewApi, resetWorkspaceStoreForTests } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import { activityContext, activitySummary, answerActivityRead } from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { transcriptDisplayStore } from "../../stores/transcriptDisplay";
import virtualStyles from "../../widgets/virtuallist/virtuallist.module.css";
import { resetSubagentModuleStoreForTests } from "../session/transcript/tools/subagentModuleStore";
import { resetTranscriptPagingForTests } from "../session/transcript/useTranscript";
import { installJobLogGeometry, jobLogOutputText } from "./JobLogTestUtils";
import Transcript from "./testing/CommittedTranscript";

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
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
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
    source: "evener",
    evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("evener/thread/activity/read", answerActivityRead);
  connectionStore.getState().connect(fake);
  return fake;
}

let restoreGeometry: () => void;

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
  resetSubagentModuleStoreForTests();
  resetWorkspaceStoreForTests();
  restoreGeometry = installJobLogGeometry(500);
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  registerDockviewApi(null); // never leak a fake dockview host to another test
  resetWorkspaceStoreForTests();
  restoreGeometry();
});

test("shows a loading placeholder before the thread hydrates", async () => {
  const fake = connectFakeClient();
  const box: { resolve: ((r: ThreadReadResponse) => void) | null } = { resolve: null };
  const read = new Promise<ThreadReadResponse>((resolve) => {
    box.resolve = resolve;
  });
  fake.on("thread/read", async (params) => ({ ...(await read), requestGeneration: params.requestGeneration }));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  expect(screen.getByText(/loading transcript/i)).toBeTruthy();
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/read")).toBe(true));
  const resolve = box.resolve;
  if (!resolve) throw new Error("transcript read was not admitted");
  await act(async () => resolve(readResponse("ref_a")));
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

test.each([undefined, "remote:parent"])(
  "a child transcript shows proven parent context while preserving its chosen title; Back target=%s",
  async (parentRef) => {
    const fake = connectFakeClient();
    const ref = "remote:child";
    const box: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    const historyRead = new Promise<ThreadReadResponse>((resolve) => {
      box.resolve = resolve;
    });
    fake.on("thread/read", async (params) => ({
      ...(params.includeTurns ? await historyRead : readResponse(ref)),
      requestGeneration: params.requestGeneration,
    }));
    fake.on("thread/unsubscribe", () => ({}));
    fake.on("evener/thread/activity/read", ({ scope }) => ({
      ...activitySummary(ref, scope),
      context: {
        ...activityContext(ref),
        sessionId: "child",
        rootRef: "remote:root",
        parentRef: "remote:parent",
        ancestors: [
          { ref: "remote:root", sessionId: "root", title: "Proven root" },
          { ref: "remote:parent", sessionId: "parent", title: "Proven parent" },
        ],
      },
    }));
    render(
      <ClientProvider client={fake}>
        <Transcript params={{ ref, parentRef }} paneId="p1" focused />
      </ClientProvider>,
    );
    const scope = await screen.findByRole("navigation", { name: "Scope" });
    expect(within(scope).getByRole("button", { name: "Proven root" })).toBeTruthy();
    expect(within(scope).getByRole("button", { name: "Proven parent" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Jesse's chosen title" })).toBeNull();
    await waitFor(() =>
      expect(
        fake.calls.some(
          (call) => call.method === "thread/read" && (call.params as { includeTurns?: boolean }).includeTurns,
        ),
      ).toBe(true),
    );
    const resolve = box.resolve;
    if (!resolve) throw new Error("history read was not admitted");
    await act(async () => resolve(readResponse(ref, { name: "Jesse's chosen title" })));
    expect(await screen.findByRole("heading", { name: "Jesse's chosen title" })).toBeTruthy();
    const hydratedScope = screen.getByRole("navigation", { name: "Scope" });
    expect(within(hydratedScope).getByText("Jesse's chosen title").getAttribute("aria-current")).toBe("page");
    expect(
      new Set(
        fake.calls.filter((call) => call.method === "thread/read").map((call) => (call.params as { ref: string }).ref),
      ),
    ).toEqual(new Set([ref]));
    expect(fake.calls.filter((call) => call.method.endsWith("/list"))).toHaveLength(0);
  },
);

test("a child transcript does not invent ancestry from its Back target", async () => {
  const fake = connectFakeClient();
  const ref = "remote:child";
  fake.on("thread/read", () => readResponse(ref, { name: "Chosen child" }));
  fake.on("thread/unsubscribe", () => ({}));
  fake.on("evener/thread/activity/read", ({ scope }) => ({
    ...activitySummary(ref, scope),
    context: {
      ...activityContext(ref),
      ancestryKnown: false,
      ancestors: [{ ref: "remote:unproven", sessionId: "unproven", title: "Unproven parent" }],
    },
  }));
  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref, parentRef: "remote:return-target" }} paneId="p1" focused />
    </ClientProvider>,
  );
  await screen.findByRole("heading", { name: "Chosen child" });
  await waitFor(() => expect(sessionActivitySnapshot(fake, ref, "session")?.context?.ancestryKnown).toBe(false));
  expect(screen.queryByRole("navigation", { name: "Scope" })).toBeNull();
  expect(screen.queryByText("Unproven parent")).toBeNull();
});

test("a direct root transcript reads its context without displaying redundant ancestry", async () => {
  const fake = connectFakeClient();
  const ref = "remote:root";
  fake.on("thread/read", () => readResponse(ref, { name: "Chosen root" }));
  fake.on("thread/unsubscribe", () => ({}));
  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref }} paneId="p1" focused />
    </ClientProvider>,
  );
  await screen.findByRole("heading", { name: "Chosen root" });
  await waitFor(() => expect(sessionActivitySnapshot(fake, ref, "session")?.context?.ancestryKnown).toBe(true));
  expect(screen.queryByRole("navigation", { name: "Scope" })).toBeNull();
});

test("renders the thread's turns through the shared VirtualList/TurnBlock engine", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        diagnostics: {
          delegates: [
            {
              runGeneration: 1,
              delegateId: "dlg_observed",
              ownerSessionId: "sess_ref_a",
              rootSessionId: "sess_ref_a",
              childSessionId: "sess_child",
              transcriptRef: "",
              type: "delegate",
              lifecycle: "idle",
              phase: "idle",
              status: "idle",
              outcome: "completed",
              terminal: true,
              resumable: true,
              needsAttention: false,
              projectionRevision: 1,
            },
          ],
        },
      },
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
            {
              id: "item_delegate",
              turnId: "turn_1",
              type: "commandExecution",
              toolName: "delegate",
              callId: "call_delegate",
              description: "Observed delegate",
              argumentsJson: JSON.stringify({ prompt: "inspect the observed thread" }),
              output: JSON.stringify({ delegate_id: "dlg_observed", status: "running" }),
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
  expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy();
  expect(screen.getByText("hi from the observed thread")).toBeTruthy();
  await waitFor(() => expect(screen.getByTestId("subagent-row")).toBeTruthy());
  expect(within(screen.getByTestId("subagent-row")).getByText("Status: done")).toBeTruthy();
});

test("keeps transcript/history and projection announcements without standalone Detail or Verbosity controls", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({
    ...readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "read me", status: "completed" }],
        },
      ],
    }),
    olderCursor: "cursor_1",
  }));
  fake.on("thread/turns/list", () => {
    throw new Error("older history unavailable");
  });

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  expect(await screen.findByText("read me")).toBeTruthy();
  // Idle paging is silent now (no "Older turns" banner); the row is what must
  // remain reachable.
  expect(screen.getByTestId("load-older-row")).toBeTruthy();
  expect(screen.queryByRole("button", { name: /^Detail:/ })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: "Verbosity…" })).toBeNull();
  await act(async () => {
    transcriptDisplayStore.setState({ viewport: "desktop" });
    transcriptDisplayStore
      .getState()
      .setLocal("desktop", makeTranscriptDisplayConfig({ kind: "preset", level: "activity" }));
  });
  await waitFor(() =>
    expect(screen.getByTestId("transcript-view-announcement").textContent).toContain("Transcript detail: Activity"),
  );
  const status = screen.getByTestId("transcript-view-announcement");
  await act(async () => {
    transcriptDisplayStore
      .getState()
      .setLocal("desktop", makeTranscriptDisplayConfig({ kind: "preset", level: "activity" }, { roundTimings: true }));
  });
  await waitFor(() => expect(status.textContent).toContain("Transcript detail: Activity · 1 advanced"));
  await act(async () => {
    transcriptDisplayStore
      .getState()
      .setLocal("desktop", makeTranscriptDisplayConfig({ kind: "preset", level: "activity" }, { tokenCounts: true }));
  });
  await waitFor(() => expect(status.textContent).toContain("Transcript detail: Activity · 1 advanced"));
  expect(screen.getByTestId("transcript-view-announcement")).toBe(status);
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
// job's output log, not a thread. The pane serves it through evener/jobs/output
// against the owning session (parentRef), never through thread/read. --------

function jobOutputText() {
  return jobLogOutputText(screen.getByTestId("joblog-content"));
}

function scrollJobToHead() {
  const port = screen.getByTestId("joblog-content").querySelector<HTMLElement>(`.${virtualStyles.root}`);
  if (!port) throw new Error("job output scroll port is missing");
  fireEvent.scroll(port, { target: { scrollTop: 0 } });
}

test("a job: ref renders the shell job's output log via evener/jobs/output, never thread/read", async () => {
  const fake = connectFakeClient();
  fake.on("evener/jobs/output", () => ({
    data: {
      offsetBytes: 0,
      bytesReturned: 18,
      totalBytes: 18,
      retainedStartBytes: 0,
      encoding: "utf8",
      data: "hello from the job",
    },
  }));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("hello from the job")).toBeTruthy());
  const outputCalls = fake.calls.filter((call) => call.method === "evener/jobs/output");
  expect(outputCalls).toHaveLength(1);
  expect(outputCalls[0]?.params).toEqual({ ref: "ref_parent", jobId: "job_x", maxBytes: 65536 });
  expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(0);
});

test("a job log shows the job's full command above its output", async () => {
  const fake = connectFakeClient();
  fake.on("evener/jobs/get", () => ({
    data: {
      jobId: "job_x",
      ownerSessionId: "sess_ref_parent",
      ownerRef: "ref_parent",
      type: "shell",
      status: "running",
      terminal: false,
      background: false,
      hasOutput: true,
      description: "shell job",
      startedAt: "2026-08-05T15:00:00Z",
      outputBytes: 3,
      command: "go test ./... -run Foo -count=1",
    },
  }));
  fake.on("evener/jobs/output", () => ({
    data: { offsetBytes: 0, bytesReturned: 3, totalBytes: 3, retainedStartBytes: 0, encoding: "utf8", data: "ok\n" },
  }));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  // The command rides its own read, against the same owning session.
  await waitFor(() => expect(screen.getByTestId("joblog-command").textContent).toBe("go test ./... -run Foo -count=1"));
  expect(screen.getByTestId("joblog-content").textContent).toContain("ok");
  const commandCalls = fake.calls.filter((call) => call.method === "evener/jobs/get");
  expect(commandCalls).toHaveLength(1);
  expect(commandCalls[0]?.params).toEqual({ ref: "ref_parent", jobId: "job_x" });
});

test.each([
  { status: "command_exited_nonzero", reason: "exit_nonzero", exitCode: 7, label: "Command failed" },
  { status: "completed", reason: "exit_zero", exitCode: 0, label: "completed" },
])("a job log identifies its result: $status", async ({ status, reason, exitCode, label }) => {
  const fake = connectFakeClient();
  fake.on("evener/jobs/get", () => ({
    data: {
      jobId: "job_x",
      ownerSessionId: "sess_ref_parent",
      ownerRef: "ref_parent",
      type: "shell",
      status,
      reason,
      exitCode,
      terminal: true,
      background: true,
      hasOutput: true,
      description: "Verify the release package",
      command: "make verify-release",
      startedAt: "2026-08-05T15:00:00Z",
      outputBytes: 9,
    },
  }));
  fake.on("evener/jobs/output", () => ({
    data: {
      offsetBytes: 0,
      bytesReturned: 9,
      totalBytes: 9,
      retainedStartBytes: 0,
      encoding: "utf8",
      data: "finished\n",
    },
  }));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  expect(await screen.findByRole("heading", { name: "Verify the release package" })).toBeTruthy();
  const result = screen.getByTestId("joblog-status");
  expect(result.textContent).toContain(label);
  expect(result.textContent).toContain(`Exit code ${exitCode}`);
  expect(screen.getByTestId("joblog-command").textContent).toBe("make verify-release");
  expect(await screen.findByText("finished")).toBeTruthy();
});

test("a job log does not borrow metadata from another owner's equal job id", async () => {
  const fake = connectFakeClient();
  const metadata = (ownerRef: string, description: string) => ({
    data: {
      jobId: "shared-id",
      ownerSessionId: ownerRef,
      ownerRef,
      type: "shell",
      status: "completed",
      terminal: true,
      background: true,
      hasOutput: false,
      description,
      command: `echo ${ownerRef}`,
      startedAt: "2026-08-05T15:00:00Z",
      outputBytes: 0,
      exitCode: 0,
    },
  });
  let finishSecond: (value: ReturnType<typeof metadata>) => void = () => {};
  const second = new Promise<ReturnType<typeof metadata>>((resolve) => {
    finishSecond = resolve;
  });
  fake.on("evener/jobs/get", ({ ref }) => (ref === "first-owner" ? metadata(ref, "First owner's job") : second));
  fake.on("evener/jobs/output", () => ({
    data: { offsetBytes: 0, bytesReturned: 0, totalBytes: 0, retainedStartBytes: 0, encoding: "utf8", data: "" },
  }));
  const view = (ownerRef: string) => (
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:shared-id", parentRef: ownerRef }} paneId="p1" focused={false} />
    </ClientProvider>
  );
  const { rerender } = render(view("first-owner"));
  expect((await screen.findByTestId("joblog-command")).textContent).toBe("echo first-owner");

  await act(async () => rerender(view("second-owner")));
  expect(screen.queryByTestId("joblog-command")).toBeNull();
  expect(screen.queryByTestId("joblog-status")).toBeNull();
  expect(screen.queryByRole("heading", { name: "First owner's job" })).toBeNull();
  await act(async () => finishSecond(metadata("second-owner", "Second owner's job")));
  expect(await screen.findByRole("heading", { name: "Second owner's job" })).toBeTruthy();
  expect(screen.getByTestId("joblog-command").textContent).toBe("echo second-owner");
});

test("a job log whose metadata read fails still renders the output", async () => {
  const fake = connectFakeClient();
  fake.on("evener/jobs/get", () => Promise.reject(new Error("job not available")));
  fake.on("evener/jobs/output", () => ({
    data: {
      offsetBytes: 0,
      bytesReturned: 18,
      totalBytes: 18,
      retainedStartBytes: 0,
      encoding: "utf8",
      data: "hello from the job",
    },
  }));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("hello from the job")).toBeTruthy());
  expect(screen.queryByTestId("joblog-command")).toBeNull();
});

test("job output renders ANSI SGR sequences as styled runs, not literal escape text", async () => {
  const fake = connectFakeClient();
  fake.on("evener/jobs/output", () => ({
    data: {
      offsetBytes: 0,
      bytesReturned: 31,
      totalBytes: 31,
      retainedStartBytes: 0,
      encoding: "utf8",
      data: "plain \u001b[32m283 passed\u001b[39m done",
    },
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

test("a truncated job log identifies the shown source bytes and true pruned prefix", async () => {
  const fake = connectFakeClient();
  fake.on("evener/jobs/output", () => ({
    data: {
      offsetBytes: 4464,
      bytesReturned: 65536,
      totalBytes: 70000,
      retainedStartBytes: 4464,
      encoding: "utf8",
      data: `${" ".repeat(65528)}tail end`,
    },
  }));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("tail end")).toBeTruthy());
  expect(screen.getByText(/loaded 65,?536 of 70,?000 output bytes/i)).toBeTruthy();
  expect(screen.getByTestId("joblog-content").getAttribute("data-joblog-source-bytes")).toBe("65536");
  const pruned = screen.getByText("Output no longer retained");
  expect(pruned.getAttribute("data-source-start")).toBe("0");
  expect(pruned.getAttribute("data-source-end")).toBe("4464");
  expect(screen.queryByText("Output not loaded")).toBeNull();
  // The page starts at the storage floor, so earlier bytes are no longer
  // retained and there is no earlier-page affordance.
  expect(screen.queryByRole("button", { name: /load earlier/i })).toBeNull();
});

test("scroll pages backwards through the job log until the head", async () => {
  const fake = connectFakeClient();
  let releaseFirst: () => void = () => {};
  const first = new Promise<void>((resolve) => {
    releaseFirst = resolve;
  });
  let releaseSecond: () => void = () => {};
  const second = new Promise<void>((resolve) => {
    releaseSecond = resolve;
  });
  fake.on("evener/jobs/output", async (params) => {
    const before = (params as { beforeBytes?: number }).beforeBytes;
    if (before === undefined) {
      return {
        data: {
          offsetBytes: 6,
          bytesReturned: 4,
          totalBytes: 10,
          retainedStartBytes: 0,
          encoding: "utf8",
          data: "6789",
        },
      };
    }
    if (before === 6) {
      await first;
      return {
        data: {
          offsetBytes: 2,
          bytesReturned: 4,
          totalBytes: 10,
          retainedStartBytes: 0,
          encoding: "utf8",
          data: "2345",
        },
      };
    }
    if (before === 2) {
      await second;
      return {
        data: { offsetBytes: 0, bytesReturned: 2, totalBytes: 10, retainedStartBytes: 0, encoding: "utf8", data: "01" },
      };
    }
    throw new Error(`unexpected beforeBytes ${before}`);
  });

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(jobOutputText()).toBe("6789"));

  scrollJobToHead();
  await act(async () => releaseFirst());
  await waitFor(() => expect(jobOutputText()).toBe("23456789"));
  expect(screen.getByText(/loaded 8 of 10 output bytes/i)).toBeTruthy();
  expect(screen.getByTestId("joblog-content").getAttribute("data-joblog-source-bytes")).toBe("8");
  expect(screen.getByText("Output not loaded").getAttribute("data-source-end")).toBe("2");

  scrollJobToHead();
  await act(async () => releaseSecond());
  await waitFor(() => expect(jobOutputText()).toBe("0123456789"));
  // The whole log is on screen, with no missing interval.
  expect(screen.queryByRole("button", { name: /load earlier/i })).toBeNull();
  expect(screen.queryByText("Output not loaded")).toBeNull();
  expect(screen.queryByText("Output no longer retained")).toBeNull();
  expect(screen.queryByText(/loaded .* output bytes/i)).toBeNull();
  expect(screen.getByTestId("joblog-content").getAttribute("data-joblog-source-bytes")).toBe("10");

  const calls = fake.calls.filter((call) => call.method === "evener/jobs/output");
  expect(calls.map((call) => (call.params as { beforeBytes?: number }).beforeBytes)).toEqual([undefined, 6, 2]);
});

test("a daemon that ignores beforeBytes preserves the output and surfaces the boundary error", async () => {
  const fake = connectFakeClient();
  // Every request incorrectly returns the same latest page. It cannot be
  // prepended because it does not end at the requested boundary.
  fake.on("evener/jobs/output", () => ({
    data: { offsetBytes: 6, bytesReturned: 4, totalBytes: 10, retainedStartBytes: 0, encoding: "utf8", data: "6789" },
  }));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(jobOutputText()).toBe("6789"));
  scrollJobToHead();
  expect(await screen.findByRole("status")).toHaveProperty(
    "textContent",
    "output page does not match the requested boundary",
  );
  expect(jobOutputText()).toBe("6789");
  expect(screen.getByText("Output not loaded").getAttribute("data-source-end")).toBe("6");
  expect(fake.calls.filter((call) => call.method === "evener/jobs/output")).toHaveLength(2);
});

test("a job with no output yet says so instead of rendering an empty log", async () => {
  const fake = connectFakeClient();
  fake.on("evener/jobs/output", () => ({
    data: { offsetBytes: 0, bytesReturned: 0, totalBytes: 0, retainedStartBytes: 0, encoding: "utf8", data: "" },
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
  expect(fake.calls.filter((call) => call.method === "evener/jobs/output")).toHaveLength(0);
});

test("the job log's refresh action refetches the tail", async () => {
  const fake = connectFakeClient();
  let calls = 0;
  fake.on("evener/jobs/output", () => ({
    data: {
      offsetBytes: 0,
      bytesReturned: ++calls * 7,
      totalBytes: calls * 7,
      retainedStartBytes: 0,
      encoding: "utf8",
      data: Array.from({ length: calls }, (_, index) => `tail ${index + 1}\n`).join(""),
    },
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
  fake.on("evener/jobs/output", () => Promise.reject(new Error("job not found: job_x")));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "job:job_x", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText(/job not found: job_x/i)).toBeTruthy());
});
