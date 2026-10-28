import type { Thread, ThreadReadResponse } from "@evener/appwire-client";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { lazy } from "react";
import { afterAll, afterEach, beforeEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../shell/clientContext";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { registerPaneForTests } from "../../shell/paneRegistry";
import {
  conversationOpenParentRef,
  registerDockviewApi,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { answerActivityRead } from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { connectReadyFakeClient } from "../../testing/connectionTestUtils";
import { ALL_CAPABILITIES as CAPABILITIES } from "../../threadTestUtils";
import virtualStyles from "../../widgets/virtuallist/virtuallist.module.css";
import { resetSubagentModuleStoreForTests } from "../session/transcript/tools/subagentModuleStore";
import { resetTranscriptPagingForTests } from "../session/transcript/useTranscript";
import { returnFromAgentCascade } from "../zoom/actions";
import { installJobLogGeometry, jobLogCommandText, jobLogOutputText } from "./JobLogTestUtils";
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
  return connectReadyFakeClient((fake) => {
    fake.on("evener/thread/activity/read", answerActivityRead);
  });
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

test("a legacy session ref recovers as its live conversation", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  // The read-only session transcript pane is gone; a stale layout or URL
  // that still names one reopens the conversation's content — resolved to
  // its title here — never a dead surface.
  await waitFor(() => expect(screen.getByText("ref_a")).toBeTruthy());
});

test("a legacy session-ref transcript pane mounts a usable composer", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  // The pane record migrates from the removed transcript type to the live
  // session type, so Session resolves the record's lifetime: the conversation
  // is not just history, it keeps its live composer.
  await screen.findByRole("textbox", { name: "Message" });
  expect(workspaceStore.getState().panes.find((pane) => pane.id === "p1")?.type).toBe("session");
});

test("a legacy transcript pane's parent context survives the migration", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_child"));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_child", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  // The migrated conversation records the legacy params' parentRef as its
  // open-parent edge, so its Back walk still reaches the pane it was opened
  // from — the same edge the saved-layout migration records.
  await screen.findByRole("textbox", { name: "Message" });
  const migrated = workspaceStore.getState().panes.find((pane) => pane.id === "p1");
  expect(migrated?.type).toBe("session");
  expect(migrated && conversationOpenParentRef(migrated)).toBe("ref_parent");
});

// A legacy cascade saved without an inspection origin Returns by retyping its
// pane back to the source descriptor it was opened from (actions.ts's
// intentFor keeps a legacy source's own type, "transcript"), not by closing to
// a bound origin. When that source conversation is ALSO already open beside
// it, the retyped pane is the ref's second conversation view: the migration
// guard leaves it typed "transcript" (migrating would put two panes on one
// conversation), and Session resolves no lifetime for a transcript record, so
// the selected view has no composer. Closing the conflicting conversation must
// let the migration re-evaluate its workspace membership and recover the view.
test("a legacy cascade Returned beside a live conversation recovers when the duplicate closes", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  const liveId = workspaceStore.getState().openPane("session", { ref: "ref_a" });
  const returnedId = workspaceStore
    .getState()
    .openPane(
      "sessionZoom",
      { ref: "ref_child", source: { type: "transcript", params: { ref: "ref_a" } }, edges: [] },
      { slot: "secondary" },
    );
  returnFromAgentCascade(returnedId);
  // The Return left the pane as the legacy source conversation it named,
  // beside the live one that already holds the ref.
  const returned = workspaceStore.getState().panes.find((pane) => pane.id === returnedId);
  expect(returned?.type).toBe("transcript");
  if (!returned) throw new Error("returned pane missing");
  // The pane's lifetime carries its retained work (composer draft, read view);
  // the close-triggered migration must keep it, not replace the record.
  const lifetime = conversationPaneLifetime(returned);

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId={returnedId} focused={false} />
    </ClientProvider>,
  );

  // Beside the live conversation the retyped pane stays a transcript with no
  // composer: it is not the ref's unit view, so it must not migrate.
  expect(workspaceStore.getState().panes.find((pane) => pane.id === returnedId)?.type).toBe("transcript");
  expect(screen.queryByRole("textbox", { name: "Message" })).toBeNull();

  // The conflicting conversation closing is the migration's cue to re-run; the
  // selected view then recovers as the live conversation with its composer.
  act(() => workspaceStore.getState().closePane(liveId));
  await screen.findByRole("textbox", { name: "Message" });
  const migrated = workspaceStore.getState().panes.find((pane) => pane.id === returnedId);
  expect(migrated?.type).toBe("session");
  expect(migrated && conversationPaneLifetime(migrated)).toBe(lifetime);
});

// --- "Open this shell transcript in a pane": a "job:<id>" ref is a shell
// job's output log, not a thread. The pane serves it through evener/jobs/output
// against the owning session (parentRef), never through thread/read. --------

function jobOutputText() {
  return jobLogOutputText(screen.getByTestId("joblog-content"));
}

// The job log's missing-interval row of one kind: bytes never fetched
// ("unloaded") or bytes the daemon no longer keeps ("pruned").
function jobLogGap(kind: "unloaded" | "pruned"): HTMLElement | null {
  return screen.getByTestId("joblog-content").querySelector(`[data-joblog-kind="${kind}"]`);
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
  await waitFor(() =>
    expect(jobLogCommandText(screen.getByTestId("joblog-command"))).toBe("go test ./... -run Foo -count=1"),
  );
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
  expect(jobLogCommandText(screen.getByTestId("joblog-command"))).toBe("make verify-release");
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
  expect(jobLogCommandText(await screen.findByTestId("joblog-command"))).toBe("echo first-owner");

  await act(async () => rerender(view("second-owner")));
  expect(screen.queryByTestId("joblog-command")).toBeNull();
  expect(screen.queryByTestId("joblog-status")).toBeNull();
  expect(screen.queryByRole("heading", { name: "First owner's job" })).toBeNull();
  await act(async () => finishSecond(metadata("second-owner", "Second owner's job")));
  expect(await screen.findByRole("heading", { name: "Second owner's job" })).toBeTruthy();
  expect(jobLogCommandText(screen.getByTestId("joblog-command"))).toBe("echo second-owner");
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
  expect(screen.getByTestId("joblog-partial")).toBeTruthy();
  expect(screen.getByTestId("joblog-content").getAttribute("data-joblog-source-bytes")).toBe("65536");
  const pruned = jobLogGap("pruned");
  expect(pruned?.getAttribute("data-source-start")).toBe("0");
  expect(pruned?.getAttribute("data-source-end")).toBe("4464");
  expect(jobLogGap("unloaded")).toBeNull();
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
  expect(screen.getByTestId("joblog-partial")).toBeTruthy();
  expect(screen.getByTestId("joblog-content").getAttribute("data-joblog-source-bytes")).toBe("8");
  expect(jobLogGap("unloaded")?.getAttribute("data-source-end")).toBe("2");

  scrollJobToHead();
  await act(async () => releaseSecond());
  await waitFor(() => expect(jobOutputText()).toBe("0123456789"));
  // The whole log is on screen, with no missing interval.
  expect(screen.queryByRole("button", { name: /load earlier/i })).toBeNull();
  expect(jobLogGap("unloaded")).toBeNull();
  expect(jobLogGap("pruned")).toBeNull();
  expect(screen.queryByTestId("joblog-partial")).toBeNull();
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
  expect((await screen.findByRole("status")).textContent).not.toBe("");
  expect(jobOutputText()).toBe("6789");
  expect(jobLogGap("unloaded")?.getAttribute("data-source-end")).toBe("6");
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
