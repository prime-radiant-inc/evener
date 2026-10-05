// The real pane, threads store and AppwireClient consume literal external
// JSON-RPC frames. The original command/identity assertions are preserved.

import type { AppwireClient } from "@evener/appwire-client";
import { connectJobOutputPeer, type JobOutputPeer } from "@evener/appwire-client/testing/jobOutputPeer";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { chromeStore, resetChromeStoreForTests } from "../../shell/chromeStore";
import { connectionStore } from "../../stores/connection";
import { threadsStore } from "../../stores/threads";
import { JobLog } from "./JobLog";
import { jobOutputMetadata } from "./JobLogTestUtils";

let client: AppwireClient;
let peer: JobOutputPeer;

beforeEach(async () => {
  ({ client, peer } = await connectJobOutputPeer());
  connectionStore.setState({ state: "ready", client });
});

afterEach(() => {
  cleanup();
  client.close();
  resetChromeStoreForTests();
  connectionStore.setState({ state: "idle", client: null });
});

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
    render(<JobLog jobRef="job:job_x" parentRef="ref_root" />);
    await respond(
      { offsetBytes: 0, bytesReturned: 3, totalBytes: 3, retainedStartBytes: 0, encoding: "utf8", data: "ok\n" },
      jobOutputMetadata({ command: "go test ./... -run Foo -count=1" }),
    );
    expect((await screen.findByTestId("joblog-command")).textContent).toBe("go test ./... -run Foo -count=1");
    expect((await screen.findByTestId("joblog-content")).textContent).toContain("ok");
    expect(peer.requests("evener/jobs/output").map((request) => request.params)).toEqual([
      { ref: "ref_root", jobId: "job_x" },
    ]);
  });

  test("shows the command even when the job has written no output yet", async () => {
    render(<JobLog jobRef="job:job_x" parentRef="ref_root" />);
    await respond(EMPTY, jobOutputMetadata({ command: "make lint", hasOutput: false }));
    expect((await screen.findByTestId("joblog-command")).textContent).toBe("make lint");
    expect(await screen.findByText("No output yet")).toBeTruthy();
  });

  test("renders the log with no command line when the job read is unavailable", async () => {
    render(<JobLog jobRef="job:job_x" parentRef="ref_root" />);
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
    render(<JobLog jobRef="job:job_x" parentRef="ref_root" />);
    await respond(HELLO, { jobId: "job_x" });
    expect(await screen.findByTestId("joblog-content")).toBeTruthy();
    expect(screen.queryByTestId("joblog-command")).toBeNull();
  });

  test("never shows another job's command while the new job's metadata is in flight", async () => {
    const { rerender } = render(<JobLog jobRef="job:job_x" parentRef="ref_root" />);
    await respond(HELLO, jobOutputMetadata({ command: "first command" }));
    expect((await screen.findByTestId("joblog-command")).textContent).toBe("first command");
    rerender(<JobLog jobRef="job:job_y" parentRef="ref_root" />);
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
    render(<JobLog jobRef="job:job_x" parentRef="ref_root" paneId="output-pane" />);
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
  render(<JobLog jobRef="job:job_x" parentRef="ref_root" />);
  await respond({
    offsetBytes: 2,
    bytesReturned: 3,
    totalBytes: 5,
    retainedStartBytes: 0,
    encoding: "base64",
    data: "mIAK",
  });
  expect((await screen.findByTestId("joblog-content")).textContent).toBe("��\n");
  fireEvent.click(screen.getByRole("button", { name: "Load earlier output" }));
  const earlier = await peer.request("evener/jobs/output", 1);
  expect(earlier.params).toEqual({ ref: "ref_root", jobId: "job_x", beforeBytes: 2 });
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
  expect((await screen.findByTestId("joblog-content")).textContent).toBe("😀\n");
});
