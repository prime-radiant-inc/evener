// The real pane, threads store and AppwireClient consume literal external
// JSON-RPC frames. The original command/identity assertions are preserved.

import type { AppwireClient } from "@evener/appwire-client";
import { connectJobOutputPeer, type JobOutputPeer } from "@evener/appwire-client/testing/jobOutputPeer";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { StubResizeObserver } from "../../resizeObserverTestUtils";
import { chromeStore, resetChromeStoreForTests } from "../../shell/chromeStore";
import { DockHost } from "../../shell/DockHost";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import virtualStyles from "../../widgets/virtuallist/virtuallist.module.css";
import { installJobLogGeometry, jobLogOutputText, jobOutputMetadata } from "./JobLogTestUtils";
import Transcript from "./Transcript";

let client: AppwireClient;
let peer: JobOutputPeer;
let restoreGeometry: () => void;

beforeEach(async () => {
  installLocalStorage(new MemoryStorage());
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  restoreGeometry = installJobLogGeometry();
  ({ client, peer } = await connectJobOutputPeer());
  connectionStore.setState({ state: "ready", client });
});

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  client.close();
  resetChromeStoreForTests();
  connectionStore.setState({ state: "idle", client: null });
  resetThreadsStoreForTests();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  restoreGeometry();
  vi.useRealTimers();
});

function mountJob(jobRef = "job:job_x", parentRef = "ref_root", paneId = "output-pane") {
  const pane = {
    id: paneId,
    type: "transcript" as const,
    params: { ref: jobRef, parentRef },
    slot: "secondary" as const,
  };
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  return render(<Transcript params={pane.params} paneId={pane.id} focused />);
}

function outputText() {
  return jobLogOutputText(screen.getByTestId("joblog-content"));
}

function port() {
  const scroller = screen.getByTestId("joblog-content").querySelector<HTMLElement>(`.${virtualStyles.root}`);
  if (!scroller) throw new Error("JobLog did not mount the real VirtualList scroll port");
  return scroller;
}

function scroll(scroller: HTMLElement, top: number) {
  fireEvent.scroll(scroller, { target: { scrollTop: top } });
}

function textPage(offsetBytes: number, data: string, totalBytes = offsetBytes + data.length, retainedStartBytes = 0) {
  return {
    offsetBytes,
    bytesReturned: new TextEncoder().encode(data).length,
    totalBytes,
    retainedStartBytes,
    encoding: "utf8",
    data,
  };
}

async function respond(data: unknown, metadata: unknown = jobOutputMetadata()) {
  const output = await peer.request("evener/jobs/output");
  const get = await peer.request("evener/jobs/get");
  await act(async () => {
    peer.reply(get, metadata);
    peer.reply(output, data);
  });
}

const EMPTY = { offsetBytes: 0, bytesReturned: 0, totalBytes: 0, retainedStartBytes: 0, encoding: "utf8", data: "" };
const HELLO = {
  offsetBytes: 0,
  bytesReturned: 6,
  totalBytes: 6,
  retainedStartBytes: 0,
  encoding: "utf8",
  data: "hello\n",
};

describe("JobLog", () => {
  test("shows the job's full command above its output", async () => {
    mountJob();
    await respond(
      { offsetBytes: 0, bytesReturned: 3, totalBytes: 3, retainedStartBytes: 0, encoding: "utf8", data: "ok\n" },
      jobOutputMetadata({ command: "go test ./... -run Foo -count=1" }),
    );
    expect((await screen.findByTestId("joblog-command")).textContent).toBe("go test ./... -run Foo -count=1");
    expect((await screen.findByTestId("joblog-content")).textContent).toContain("ok");
    expect(peer.requests("evener/jobs/output").map((request) => request.params)).toEqual([
      { ref: "ref_root", jobId: "job_x", maxBytes: 65536 },
    ]);
  });

  test("shows the command even when the job has written no output yet", async () => {
    mountJob();
    await respond(EMPTY, jobOutputMetadata({ command: "make lint", hasOutput: false }));
    expect((await screen.findByTestId("joblog-command")).textContent).toBe("make lint");
    expect(await screen.findByText("No output yet")).toBeTruthy();
  });

  test("renders the log with no command line when the job read is unavailable", async () => {
    mountJob();
    const output = await peer.request("evener/jobs/output");
    const get = await peer.request("evener/jobs/get");
    await act(async () => {
      peer.fail(get, "job not available");
      peer.reply(output, HELLO);
    });
    expect((await screen.findByTestId("joblog-content")).textContent).toContain("hello");
    expect(screen.queryByTestId("joblog-command")).toBeNull();
  });

  test("shows no command line when the job payload is malformed", async () => {
    mountJob();
    await respond(HELLO, { jobId: "job_x" });
    expect(await screen.findByTestId("joblog-content")).toBeTruthy();
    expect(screen.queryByTestId("joblog-command")).toBeNull();
  });

  test("never shows another job's command while the new job's metadata is in flight", async () => {
    const { rerender } = mountJob();
    await respond(HELLO, jobOutputMetadata({ command: "first command" }));
    expect((await screen.findByTestId("joblog-command")).textContent).toBe("first command");
    act(() =>
      workspaceStore.setState({
        panes: [
          {
            id: "output-pane",
            type: "transcript",
            params: { ref: "job:job_y", parentRef: "ref_root" },
            slot: "secondary",
          },
        ],
      }),
    );
    rerender(<Transcript params={{ ref: "job:job_y", parentRef: "ref_root" }} paneId="output-pane" focused />);
    const secondGet = await peer.request("evener/jobs/get", 1);
    const secondOutput = await peer.request("evener/jobs/output", 1);
    expect(secondGet.params).toEqual({ ref: "ref_root", jobId: "job_y" });
    expect(screen.queryByTestId("joblog-command")).toBeNull();
    await act(async () => {
      peer.reply(secondGet, jobOutputMetadata({ jobId: "job_y", command: "second command" }));
      peer.reply(secondOutput, HELLO);
    });
    expect((await screen.findByTestId("joblog-command")).textContent).toBe("second command");
  });
});

test.each(["unavailable", "unnamed"])(
  "%s metadata preserves job identity while real output reads succeed",
  async (kind) => {
    mountJob();
    const get = await peer.request("evener/jobs/get");
    const output = await peer.request("evener/jobs/output");
    await act(async () => {
      if (kind === "unavailable") peer.fail(get, "metadata unavailable");
      else peer.reply(get, jobOutputMetadata({ description: "", command: "" }));
      peer.reply(output, {
        offsetBytes: 0,
        bytesReturned: 19,
        totalBytes: 19,
        retainedStartBytes: 0,
        encoding: "utf8",
        data: "IDENTIFIABLE_OUTPUT",
      });
    });
    expect((await screen.findByTestId("joblog-content")).textContent).toContain("IDENTIFIABLE_OUTPUT");
    expect(screen.getByRole("heading", { name: "job_x" })).toBeTruthy();
    expect(chromeStore.getState().paneTitles.get("output-pane")).toBe("job_x");
    expect(peer.requests("evener/jobs/get").map((request) => request.params)).toEqual([
      { ref: "ref_root", jobId: "job_x" },
    ]);
  },
);

test("forwards an explicit zero selector rather than reading latest output", async () => {
  const pending = threadsStore.getState().jobOutput("ref_root", "job_x", 0, 1);
  const request = await peer.request("evener/jobs/output");
  peer.reply(request, EMPTY);
  await pending;
  expect(request.params).toEqual({ ref: "ref_root", jobId: "job_x", beforeBytes: 0, maxBytes: 1 });
});

test("decodes split UTF8 bytes only after contiguous earlier output is joined", async () => {
  mountJob();
  await respond({
    offsetBytes: 2,
    bytesReturned: 3,
    totalBytes: 5,
    retainedStartBytes: 0,
    encoding: "base64",
    data: "mIAK",
  });
  expect(outputText()).toBe("��\n");
  scroll(port(), 0);
  const earlier = await peer.request("evener/jobs/output", 1);
  expect(earlier.params).toEqual({ ref: "ref_root", jobId: "job_x", beforeBytes: 2, maxBytes: 2 });
  await act(async () =>
    peer.reply(earlier, {
      offsetBytes: 0,
      bytesReturned: 2,
      totalBytes: 5,
      retainedStartBytes: 0,
      encoding: "base64",
      data: "8J8=",
    }),
  );
  expect(outputText()).toBe("😀\n");
});

test("scrolling to the unloaded prefix automatically fetches adjacent source bytes", async () => {
  mountJob();
  await respond(textPage(100, "TAIL\n"));
  const scroller = port();
  scroll(scroller, 0);
  const history = await peer.request("evener/jobs/output", 1);
  expect(history.params).toEqual({ ref: "ref_root", jobId: "job_x", beforeBytes: 100, maxBytes: 100 });
  await act(async () => peer.reply(history, textPage(90, "OLDER_ROW\n", 105, 90)));
  expect(screen.getByText("OLDER_ROW")).toBeTruthy();
  expect(screen.getByText("TAIL")).toBeTruthy();
  expect(screen.getByText("OLDER_ROW").closest("[data-source-start]")?.getAttribute("data-source-start")).toBe("90");
});

test("running output follows growth without a Refresh click", async () => {
  vi.useFakeTimers();
  mountJob();
  await respond(HELLO);
  await act(async () => vi.advanceTimersByTimeAsync(1000));
  expect(peer.requests("evener/jobs/output")).toHaveLength(2);
  const growth = await peer.request("evener/jobs/output", 1);
  await act(async () => peer.reply(growth, textPage(0, "hello\nLIVE_GROWTH_MARKER\n")));
  expect(screen.getByText("LIVE_GROWTH_MARKER")).toBeTruthy();
  expect(screen.getByTestId("joblog-content").getAttribute("data-joblog-live-bytes")).toBe("25");
});

test("inactive pane unmount and remount retain loaded older output", async () => {
  const mounted = mountJob();
  await respond(textPage(100, "TAIL\n"));
  scroll(port(), 0);
  const history = await peer.request("evener/jobs/output", 1);
  await act(async () => peer.reply(history, textPage(90, "OLDER_ROW\n", 105, 90)));
  mounted.unmount();
  const pane = workspaceStore.getState().panes[0];
  if (!pane) throw new Error("workspace lost the inactive output pane");
  await act(async () => {
    render(<Transcript params={{ ref: "job:job_x", parentRef: "ref_root" }} paneId={pane.id} focused />);
  });
  expect(screen.getByText("OLDER_ROW")).toBeTruthy();
  expect(screen.getByText("TAIL")).toBeTruthy();
  expect(peer.requests("evener/jobs/output")).toHaveLength(2);
});

test("Refresh preserves older source rows while updating the live window", async () => {
  mountJob();
  await respond(textPage(100, "TAIL\n"));
  scroll(port(), 0);
  const history = await peer.request("evener/jobs/output", 1);
  await act(async () => peer.reply(history, textPage(90, "OLDER_ROW\n", 105, 90)));
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  const refreshed = await peer.request("evener/jobs/output", 2);
  await act(async () => peer.reply(refreshed, textPage(100, "TAIL\nFRESH\n", 111, 90)));
  expect(screen.getByText("OLDER_ROW")).toBeTruthy();
  expect(screen.getByText("FRESH")).toBeTruthy();
});

test("source row identities count a malformed byte rather than decoded string characters", async () => {
  mountJob();
  await respond({
    offsetBytes: 0,
    bytesReturned: 6,
    totalBytes: 6,
    retainedStartBytes: 0,
    encoding: "base64",
    data: "/wpST1cK",
  });
  expect(screen.getByText("ROW").closest("[data-source-start]")?.getAttribute("data-source-start")).toBe("2");
});

function largePage(label: string) {
  return Array.from({ length: 1024 }, (_, index) => `${`${label}_${index}`.padEnd(63, " ")}\n`).join("");
}

async function laterPage(mount = true) {
  if (mount) mountJob();
  await respond(textPage(196608, largePage("LIVE"), 262144));
  const scroller = port();
  expect(scroller.scrollTop).toBeGreaterThan(19000);
  await act(async () => scroll(scroller, 0));
  expect(peer.requests("evener/jobs/output")).toHaveLength(2);
  await act(async () =>
    peer.reply(await peer.request("evener/jobs/output", 1), textPage(131072, largePage("MIDDLE"), 262144)),
  );
  await act(async () => scroll(scroller, 0));
  const third = await peer.request("evener/jobs/output", 2);
  expect(third.params).toEqual({ ref: "ref_root", jobId: "job_x", beforeBytes: 131072, maxBytes: 65536 });
  const bytes = new TextEncoder().encode(largePage("LATER_PAGE_MARKER"));
  bytes[0] = 255;
  await act(async () =>
    peer.reply(third, {
      offsetBytes: 65536,
      bytesReturned: 65536,
      totalBytes: 262144,
      retainedStartBytes: 65536,
      encoding: "base64",
      data: btoa(Array.from(bytes, (byte) => String.fromCharCode(byte)).join("")),
    }),
  );
  await act(async () => scroll(scroller, 44));
  const anchor = scroller.querySelector<HTMLElement>('[data-joblog-kind="output"][data-source-start="65600"]');
  expect(anchor?.textContent).toContain("LATER_PAGE_MARKER_1");
  expect(anchor?.getBoundingClientRect().top).toBe(-4);
  return { scroller, anchor };
}

test("growth preserves the actual later-page visible byte row after three pages and malformed input", async () => {
  vi.useFakeTimers();
  const { scroller, anchor } = await laterPage();
  const before = anchor?.getBoundingClientRect().top ?? Number.NaN;
  await act(async () => vi.advanceTimersByTimeAsync(1000));
  const growth = await peer.request("evener/jobs/output", 3);
  const suffix = "LIVE_GROWTH_MARKER\n";
  const data = largePage("LIVE").slice(suffix.length) + suffix;
  await act(async () => peer.reply(growth, textPage(196608 + suffix.length, data, 262144 + suffix.length, 65536)));
  const after = scroller.querySelector<HTMLElement>('[data-source-start="65600"]');
  expect(after?.textContent).toContain("LATER_PAGE_MARKER_1");
  expect(Math.abs((after?.getBoundingClientRect().top ?? Number.NaN) - before)).toBeLessThanOrEqual(2);
  expect(scroller.querySelector('[data-joblog-kind="unloaded"]')).toBeNull();
});

test("bottom follows append and returning to bottom resumes following after older reading", async () => {
  vi.useFakeTimers();
  mountJob();
  const data = Array.from({ length: 40 }, (_, index) => `ROW_${index}\n`).join("");
  await respond(textPage(0, data));
  const scroller = port();
  expect(scroller.scrollTop).toBe(700);
  await act(async () => vi.advanceTimersByTimeAsync(1000));
  await act(async () => peer.reply(await peer.request("evener/jobs/output", 1), textPage(0, `${data}APPEND\n`)));
  expect(scroller.scrollTop).toBe(720);
  await act(async () => scroll(scroller, 105));
  const anchor = scroller.querySelector<HTMLElement>('[data-source-start="30"]');
  expect(anchor?.textContent).toContain("ROW_5");
  const before = anchor?.getBoundingClientRect().top;
  await act(async () => vi.advanceTimersByTimeAsync(1000));
  await act(async () => peer.reply(await peer.request("evener/jobs/output", 2), textPage(0, `${data}APPEND\nAWAY\n`)));
  expect(scroller.scrollTop).toBe(105);
  expect(scroller.querySelector<HTMLElement>('[data-source-start="30"]')?.getBoundingClientRect().top).toBe(before);
  await act(async () => scroll(scroller, scroller.scrollHeight - scroller.clientHeight));
  await act(async () => vi.advanceTimersByTimeAsync(1000));
  await act(async () =>
    peer.reply(await peer.request("evener/jobs/output", 3), textPage(0, `${data}APPEND\nAWAY\nRETURNED\n`)),
  );
  expect(scroller.scrollTop).toBe(760);
  expect(screen.getByText("RETURNED")).toBeTruthy();
});

async function separatedWindows(following: boolean, ansi = false) {
  mountJob();
  const initial = largePage("OLD_LIVE");
  await respond(textPage(524288, ansi ? `${initial.slice(0, 58)}\x1b[31m${initial.slice(63)}` : initial, 589824));
  const scroller = port();
  await act(async () => scroll(scroller, 0));
  expect(peer.requests("evener/jobs/output")).toHaveLength(2);
  const history = await peer.request("evener/jobs/output", 1);
  await act(async () => peer.reply(history, textPage(458752, largePage("OLDER"), 589824, 458752)));
  await act(async () => scroll(scroller, following ? scroller.scrollHeight - 100 : 100));
  await act(async () => vi.advanceTimersByTimeAsync(1000));
  expect(peer.requests("evener/jobs/output")).toHaveLength(3);
  const latest = await peer.request("evener/jobs/output", 2);
  await act(async () => peer.reply(latest, textPage(1048576, largePage("NEW_LIVE"), 1114112, 458752)));
  expect(screen.getByTestId("joblog-content").getAttribute("data-joblog-older-bytes")).toBe("131072");
  return scroller;
}

test("forward gap paging holds the loaded side through a controls-only page", async () => {
  vi.useFakeTimers();
  const scroller = await separatedWindows(false);
  // The frozen initial page and the older page make 2048 rows before the gap.
  await act(async () => scroll(scroller, 41000));
  expect(peer.requests("evener/jobs/output")).toHaveLength(4);
  const request = await peer.request("evener/jobs/output", 3);
  expect(request.params).toEqual({ ref: "ref_root", jobId: "job_x", beforeBytes: 655360, maxBytes: 65536 });
  const viewport = scroller.getBoundingClientRect();
  const visibleLive = Array.from(scroller.querySelectorAll<HTMLElement>('[data-joblog-kind="output"]')).filter(
    (element) =>
      Number(element.dataset.sourceStart) >= 1048576 && element.getBoundingClientRect().top < viewport.bottom,
  );
  expect(visibleLive).toHaveLength(0);
  const controls = `\x1b]0;${"x".repeat(65531)}\x07`;
  await act(async () => peer.reply(request, textPage(589824, controls, 1114112, 458752)));
  expect(scroller.querySelector('[data-joblog-kind="unloaded"]')).not.toBeNull();
  const next = await peer.request("evener/jobs/output", 4);
  expect(next.params).toEqual({ ref: "ref_root", jobId: "job_x", beforeBytes: 720896, maxBytes: 65536 });
  await act(async () => peer.reply(next, textPage(655360, largePage("FORWARD_ADJACENT"), 1114112, 458752)));
  expect(screen.getAllByText(/^FORWARD_ADJACENT_/).length).toBeGreaterThan(0);
});

test("backward gap paging requests the live-adjacent bytes and keeps its independent ANSI range", async () => {
  vi.useFakeTimers();
  const scroller = await separatedWindows(true, true);
  expect(scroller.querySelector('[data-ansi-fg="red"]')).toBeNull();
  await act(async () => scroll(scroller, 41000));
  expect(peer.requests("evener/jobs/output")).toHaveLength(4);
  const request = await peer.request("evener/jobs/output", 3);
  expect(request.params).toEqual({ ref: "ref_root", jobId: "job_x", beforeBytes: 1048576, maxBytes: 65536 });
  await act(async () => peer.reply(request, textPage(983040, `\x1b]0;${"x".repeat(65531)}\x07`, 1114112, 458752)));
  expect(scroller.querySelector('[data-joblog-kind="unloaded"]')).not.toBeNull();
  expect(peer.requests("evener/jobs/output")).toHaveLength(5);
  const next = await peer.request("evener/jobs/output", 4);
  expect(next.params).toEqual({ ref: "ref_root", jobId: "job_x", beforeBytes: 983040, maxBytes: 65536 });
  await act(async () => peer.reply(next, textPage(917504, largePage("BACKWARD_ADJACENT"), 1114112, 458752)));
  expect(screen.getAllByText(/^BACKWARD_ADJACENT_/).length).toBeGreaterThan(0);
  expect(scroller.querySelector('[data-ansi-fg="red"]')).toBeNull();
  const pruned = scroller.querySelector('[data-joblog-kind="pruned"]');
  expect(pruned?.getAttribute("data-source-start")).toBe("0");
  expect(pruned?.getAttribute("data-source-end")).toBe("458752");
  const unloaded = scroller.querySelector('[data-joblog-kind="unloaded"]');
  expect(unloaded?.getAttribute("data-source-start")).toBe("458752");
  expect(unloaded?.getAttribute("data-source-end")).toBe("917504");
});

test("a real inactive DockHost tab remounts the later-page byte row at the same pixel", async () => {
  vi.stubGlobal("ResizeObserver", StubResizeObserver);
  await import("../welcome");
  await import("./index");
  workspaceStore.getState().openPane("welcome", {});
  const first = workspaceStore
    .getState()
    .openPane("transcript", { ref: "job:job_x", parentRef: "ref_root" }, { slot: "secondary" });
  await act(async () => {
    render(<DockHost />);
  });
  const { anchor } = await laterPage(false);
  const before = anchor?.getBoundingClientRect().top ?? Number.NaN;
  const count = peer.requests("evener/jobs/output").length;
  await act(async () =>
    workspaceStore
      .getState()
      .openPane("transcript", { ref: "job:job_y", parentRef: "ref_root" }, { slot: "secondary" }),
  );
  expect(screen.queryByText("LATER_PAGE_MARKER_1")).toBeNull();
  const second = await peer.request("evener/jobs/output", count);
  expect(second.params).toEqual({ ref: "ref_root", jobId: "job_y", maxBytes: 65536 });
  await act(async () => workspaceStore.getState().focusPane(first));
  const restored = port().querySelector<HTMLElement>('[data-source-start="65600"]');
  expect(restored?.textContent).toContain("LATER_PAGE_MARKER_1");
  expect(Math.abs((restored?.getBoundingClientRect().top ?? Number.NaN) - before)).toBeLessThanOrEqual(2);
  expect(workspaceStore.getState().focusedPaneId).toBe(first);
  expect(screen.getByRole("heading", { name: "shell job" })).toBeTruthy();
  expect(chromeStore.getState().paneTitles.get(first)).toBe("shell job");
});

test("reconnect preserves the later-page row and resumes owner-scoped reads", async () => {
  const { scroller, anchor } = await laterPage();
  const before = anchor?.getBoundingClientRect().top ?? Number.NaN;
  await act(async () => connectionStore.setState({ state: "reconnecting", client: null }));
  expect(scroller.querySelector('[data-source-start="65600"]')?.textContent).toContain("LATER_PAGE_MARKER_1");
  client.close();
  ({ client, peer } = await connectJobOutputPeer());
  await act(async () => connectionStore.setState({ state: "ready", client }));
  const output = await peer.request("evener/jobs/output");
  const metadata = await peer.request("evener/jobs/get");
  expect(output.params).toEqual({ ref: "ref_root", jobId: "job_x", maxBytes: 65536 });
  await act(async () => {
    peer.reply(metadata, jobOutputMetadata());
    peer.reply(output, textPage(196608, largePage("RECONNECTED_LIVE"), 262144, 65536));
  });
  const restored = scroller.querySelector<HTMLElement>('[data-source-start="65600"]');
  expect(restored?.textContent).toContain("LATER_PAGE_MARKER_1");
  expect(Math.abs((restored?.getBoundingClientRect().top ?? Number.NaN) - before)).toBeLessThanOrEqual(2);
});

test("partial-line replacement restores a captured byte rather than its vanished row key", async () => {
  mountJob();
  const data = Array.from({ length: 40 }, (_, index) => `ROW_${index}\n`).join("");
  await respond(textPage(100, data));
  const scroller = port();
  await act(async () => scroll(scroller, 25));
  const before = scroller.querySelector<HTMLElement>('[data-source-start="100"]');
  expect(before?.textContent).toContain("ROW_0");
  const pixel = before?.getBoundingClientRect().top ?? Number.NaN;
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  const next = await peer.request("evener/jobs/output", 1);
  // A previously unknown prefix has no newline before ROW_0. Its first row's
  // immutable byte start changes, while its captured byte stays readable.
  await act(async () => peer.reply(next, textPage(95, `PART_${data}`, 370, 95)));
  expect(scroller.querySelector('[data-source-start="100"]')).toBeNull();
  const after = scroller.querySelector<HTMLElement>('[data-source-start="95"]');
  expect(after?.textContent).toContain("PART_ROW_0");
  expect(Math.abs((after?.getBoundingClientRect().top ?? Number.NaN) - pixel)).toBeLessThanOrEqual(2);
});

test("forward history trim preserves a visible byte row when controls evict the old prefix", async () => {
  vi.useFakeTimers();
  mountJob();
  await respond(textPage(1048576, largePage("INITIAL"), 1114112, 589824));
  const scroller = port();
  for (let page = 0; page < 7; page++) {
    await act(async () => scroll(scroller, 20));
    expect(peer.requests("evener/jobs/output")).toHaveLength(page + 2);
    const earlier = await peer.request("evener/jobs/output", page + 1);
    await act(async () =>
      peer.reply(earlier, textPage(983040 - page * 65536, largePage(`HISTORY_${page}`), 1114112, 589824)),
    );
  }
  await act(async () => scroll(scroller, 100));
  await act(async () => vi.advanceTimersByTimeAsync(1000));
  await act(async () =>
    peer.reply(await peer.request("evener/jobs/output", 8), textPage(1966080, largePage("LIVE"), 2031616, 589824)),
  );
  await act(async () => scroll(scroller, 163860));
  const forward = await peer.request("evener/jobs/output", 9);
  expect(forward.params).toEqual({ ref: "ref_root", jobId: "job_x", beforeBytes: 1179648, maxBytes: 65536 });
  const anchor = scroller.querySelector<HTMLElement>('[data-source-start="1113856"]');
  expect(anchor?.textContent).toContain("INITIAL_1020");
  const before = anchor?.getBoundingClientRect().top ?? Number.NaN;
  await act(async () => peer.reply(forward, textPage(1114112, `\x1b]0;${"x".repeat(65531)}\x07`, 2031616, 589824)));
  const after = scroller.querySelector<HTMLElement>('[data-source-start="1113856"]');
  expect(after?.textContent).toContain("INITIAL_1020");
  expect(Math.abs((after?.getBoundingClientRect().top ?? Number.NaN) - before)).toBeLessThanOrEqual(2);
  expect(screen.getByTestId("joblog-content").getAttribute("data-joblog-older-bytes")).toBe("524288");
});

test("document visibility preserves later-page capture and pauses new output requests", async () => {
  vi.useFakeTimers();
  const { scroller, anchor } = await laterPage();
  const before = anchor?.getBoundingClientRect().top ?? Number.NaN;
  const visibility = vi.spyOn(document, "visibilityState", "get");
  visibility.mockReturnValue("hidden");
  await act(async () => document.dispatchEvent(new Event("visibilitychange")));
  await act(async () => vi.advanceTimersByTimeAsync(5000));
  expect(peer.requests("evener/jobs/output")).toHaveLength(3);
  visibility.mockReturnValue("visible");
  await act(async () => document.dispatchEvent(new Event("visibilitychange")));
  const output = await peer.request("evener/jobs/output", 3);
  await act(async () => peer.reply(output, textPage(196608, largePage("RESUMED"), 262144, 65536)));
  const after = scroller.querySelector<HTMLElement>('[data-source-start="65600"]');
  expect(after?.textContent).toContain("LATER_PAGE_MARKER_1");
  expect(Math.abs((after?.getBoundingClientRect().top ?? Number.NaN) - before)).toBeLessThanOrEqual(2);
});

test("failed terminal output keeps its true status, full command and exit code", async () => {
  mountJob();
  await respond(
    textPage(0, "FAILURE_OUTPUT\n"),
    jobOutputMetadata({
      status: "failed",
      terminal: true,
      exitCode: 2,
      command: "go test ./... -run Failure -count=1",
    }),
  );
  expect(screen.getByTestId("joblog-status").textContent).toBe("failed · Exit code 2");
  expect(screen.getByTestId("joblog-command").textContent).toBe("go test ./... -run Failure -count=1");
  expect(screen.getByText("FAILURE_OUTPUT")).toBeTruthy();
});
