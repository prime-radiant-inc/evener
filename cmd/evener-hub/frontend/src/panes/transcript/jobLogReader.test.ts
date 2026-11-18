import { AppwireClient, type JobOutputPage } from "@evener/appwire-client";
import { connectJobOutputPeer, JobOutputPeer } from "@evener/appwire-client/testing/jobOutputPeer";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore, resetConnectionStoreForTests } from "../../stores/connection";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { jobOutputMetadata } from "./JobLogTestUtils";
import { type JobLogReader, type JobLogReaderSnapshot, retainedJobLogReader } from "./jobLogReader";

const OUTPUT = "evener/jobs/output";
const META = "evener/jobs/get";
const OWNER = "local:session_output";
const JOB = "job_output";
const clients: AppwireClient[] = [];
let peer: JobOutputPeer;
let pane: OpenPaneRecord;
let reader: JobLogReader;

function page(offsetBytes = 100, data = "TAIL\n", totalBytes = 105, retainedStartBytes = 0): JobOutputPage {
  return { offsetBytes, bytesReturned: data.length, totalBytes, retainedStartBytes, encoding: "utf8", data };
}

function metadata(terminal = false) {
  return jobOutputMetadata({
    jobId: JOB,
    ownerSessionId: "session_output",
    ownerRef: OWNER,
    background: true,
    command: "paging-producer",
    terminal,
    status: terminal ? "completed" : "running",
    ...(terminal ? { outcome: "succeeded" as const, endedAt: "2026-10-05T00:00:01Z" } : {}),
  });
}

function observed(
  predicate: (snapshot: JobLogReaderSnapshot) => boolean,
  source = reader,
): Promise<JobLogReaderSnapshot> {
  if (predicate(source.getSnapshot())) return Promise.resolve(source.getSnapshot());
  return new Promise((resolve) => {
    const unsubscribe = source.subscribe(() => {
      const snapshot = source.getSnapshot();
      if (!predicate(snapshot)) return;
      unsubscribe();
      resolve(snapshot);
    });
  });
}

async function wire() {
  const connected = await connectJobOutputPeer();
  clients.push(connected.client);
  connectionStore.getState().connect(connected.client);
  return connected.peer;
}

async function initial(value = page()) {
  peer.reply(await peer.request(META), metadata());
  peer.reply(await peer.request(OUTPUT), value);
  return observed((snapshot) => snapshot.windows.live !== null && snapshot.job !== null);
}

beforeEach(async () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-05T00:00:00Z"));
  installLocalStorage(new MemoryStorage());
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  peer = await wire();
  pane = { id: "output-pane", type: "transcript", params: { ref: `job:${JOB}`, parentRef: OWNER }, slot: "secondary" };
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  reader = retainedJobLogReader(conversationPaneLifetime(pane), OWNER, JOB);
  reader.setMounted(true);
});

afterEach(() => {
  resetWorkspaceStoreForTests();
  for (const client of clients.splice(0)) client.close();
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  expect(vi.getTimerCount()).toBe(0);
  vi.useRealTimers();
});

test("output arrives independently of failed metadata and without transcript subscriptions", async () => {
  peer.fail(await peer.request(META), "metadata unavailable");
  peer.reply(await peer.request(OUTPUT), page());
  const snapshot = await observed((state) => state.windows.live !== null && state.metadataError !== null);
  expect(snapshot.windows.live?.bytes).toEqual(new Uint8Array([84, 65, 73, 76, 10]));
  expect(snapshot.metadataError).toContain("metadata unavailable");
  expect(snapshot.outputError).toBeNull();
  expect(peer.sent.map((raw) => JSON.parse(raw).method).filter((method) => method.startsWith("thread/"))).toEqual([]);
  expect(peer.sent.map((raw) => JSON.parse(raw).method).filter((method) => method.includes("subscribe"))).toEqual([]);
});

test("settleForTests joins the live output read it starts", async () => {
  await initial();
  reader.refresh();
  // The previous live read paced the next one, so let the fake clock reach it.
  await vi.advanceTimersByTimeAsync(1000);
  const live = await peer.request(OUTPUT, 1);
  let settled = false;
  const joined = reader.settleForTests().then(() => {
    settled = true;
  });
  await Promise.resolve();
  // The join must not resolve before the page it is watching arrives.
  expect(settled).toBe(false);
  peer.reply(live, page());
  await joined;
  expect(settled).toBe(true);
  expect(reader.getSnapshot().windows.live).not.toBeNull();
});

test("holds one output slot while demand and running ticks accumulate", async () => {
  await initial();
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  const held = await peer.request(OUTPUT, 1);
  reader.demand({ direction: "backward", boundaryBytes: 90, limitBytes: 105 }, "start");
  await vi.advanceTimersByTimeAsync(3000);
  expect(peer.requests(OUTPUT)).toHaveLength(2);
  expect(reader.getSnapshot().pendingDemand?.boundaryBytes).toBe(90);
  peer.reply(held, page(90, "OLDER_ROW\n", 105));
  const live = await peer.request(OUTPUT, 2);
  expect(live.params.beforeBytes).toBeUndefined();
  expect(reader.getSnapshot().pendingDemand?.boundaryBytes).toBe(90);
  peer.reply(live, page());
  const history = await peer.request(OUTPUT, 3);
  expect(history.params.beforeBytes).toBe(90);
});

test("retries unresolved history after one second without losing cache", async () => {
  const first = await initial();
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  peer.fail(await peer.request(OUTPUT, 1), "temporary output failure");
  const failed = await observed((snapshot) => snapshot.outputError !== null);
  expect(failed.windows.live).toBe(first.windows.live);
  expect(failed.pendingDemand?.boundaryBytes).toBe(100);
  await vi.advanceTimersByTimeAsync(999);
  expect(peer.requests(OUTPUT)).toHaveLength(2);
  await vi.advanceTimersByTimeAsync(1);
  const retry = await peer.request(OUTPUT, 2);
  expect(retry.params.beforeBytes).toBe(100);
  peer.reply(retry, page(90, "OLDER_ROW\n"));
  await observed((snapshot) => snapshot.windows.older !== null && snapshot.outputError === null);
});

test("a latest reply to a history request preserves bytes and retries without a tight loop", async () => {
  const first = await initial();
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  const history = await peer.request(OUTPUT, 1);
  expect(history.params.beforeBytes).toBe(100);
  peer.reply(history, page());
  const rejected = await observed((snapshot) => snapshot.outputError !== null || snapshot.windows.older !== null);
  expect(rejected.outputError).not.toBeNull();
  expect(rejected.windows.live).toBe(first.windows.live);
  expect(rejected.windows.older).toBeNull();
  expect(rejected.pendingDemand?.boundaryBytes).toBe(100);
  await vi.advanceTimersByTimeAsync(999);
  expect(peer.requests(OUTPUT)).toHaveLength(2);
  await vi.advanceTimersByTimeAsync(1);
  const retry = await peer.request(OUTPUT, 2);
  expect(retry.params.beforeBytes).toBe(100);
  peer.reply(retry, page(90, "OLDER_ROW\n"));
  const recovered = await observed((snapshot) => snapshot.windows.older !== null && snapshot.outputError === null);
  expect(recovered.windows.older?.rows[0]?.line[0]?.text).toBe("OLDER_ROW");
});

test("an empty running EOF still polls and publishes a later append", async () => {
  await initial(page(0, "", 0));
  await vi.advanceTimersByTimeAsync(999);
  expect(peer.requests(OUTPUT)).toHaveLength(1);
  await vi.advanceTimersByTimeAsync(1);
  peer.reply(await peer.request(OUTPUT, 1), page(0, "NEW\n", 4));
  const snapshot = await observed((state) => state.windows.totalBytes === 4);
  expect(snapshot.windows.live?.bytes).toEqual(new Uint8Array([78, 69, 87, 10]));
});

test("terminal observation creates a fresh drain even when initial output is empty EOF", async () => {
  const first = await peer.request(OUTPUT);
  peer.reply(await peer.request(META), metadata(true));
  await observed((snapshot) => snapshot.job?.terminal === true);
  expect(reader.getSnapshot().drainPending).toBe(true);
  peer.reply(first, page(0, "", 0));
  const drain = await peer.request(OUTPUT, 1);
  peer.fail(drain, "temporarily unavailable");
  await observed((snapshot) => snapshot.outputError !== null);
  await vi.advanceTimersByTimeAsync(999);
  expect(peer.requests(OUTPUT)).toHaveLength(2);
  await vi.advanceTimersByTimeAsync(1);
  peer.reply(await peer.request(OUTPUT, 2), page(0, "FINAL_MARKER\n", 13));
  const snapshot = await observed((state) => state.windows.totalBytes === 13 && !state.drainPending);
  expect(snapshot.windows.live?.bytes).toEqual(new Uint8Array([70, 73, 78, 65, 76, 95, 77, 65, 82, 75, 69, 82, 10]));
  await vi.advanceTimersByTimeAsync(5000);
  expect(peer.requests(OUTPUT)).toHaveLength(3);
});

test("inactive-tab remount keeps the same reader, older reading bytes, demand and capture", async () => {
  await initial();
  reader.capture({ byteOffset: 100, pixelOffset: -3, following: false });
  const cached = reader.getSnapshot().windows.older;
  reader.setMounted(false);
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  await vi.advanceTimersByTimeAsync(5000);
  const remounted = retainedJobLogReader(conversationPaneLifetime(pane), OWNER, JOB);
  expect(remounted).toBe(reader);
  expect(remounted.getSnapshot().windows.older).toBe(cached);
  expect(remounted.getSnapshot().scrollCapture).toEqual({ byteOffset: 100, pixelOffset: -3, following: false });
  expect(peer.requests(OUTPUT)).toHaveLength(1);
  remounted.setMounted(true);
  const history = await peer.request(OUTPUT, 1);
  expect(history.params.beforeBytes).toBe(100);
});

test.each(["hide", "unmount", "connection"] as const)(
  "%s fences held output without releasing its slot early",
  async (pause) => {
    const first = await initial();
    reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
    const held = await peer.request(OUTPUT, 1);
    const view = [...conversationPaneLifetime(pane).readViews.values()][0];
    if (!view) throw new Error("missing output read view");
    if (pause === "hide") view.setReadable(false);
    else if (pause === "unmount") reader.setMounted(false);
    else connectionStore.setState({ state: "reconnecting" });
    await vi.advanceTimersByTimeAsync(2000);
    if (pause === "hide") view.setReadable(true);
    else if (pause === "unmount") reader.setMounted(true);
    else connectionStore.setState({ state: "ready" });
    expect(peer.requests(OUTPUT)).toHaveLength(2);
    peer.reply(held, page(90, "STALE_ROW\n"));
    const retry = await peer.request(OUTPUT, 2);
    expect(reader.getSnapshot().windows.live).toBe(first.windows.live);
    expect(reader.getSnapshot().windows.older).toBeNull();
    expect(retry.params.beforeBytes).toBe(100);
    peer.reply(retry, page(90, "OLDER_ROW\n"));
    const recovered = await observed((snapshot) => snapshot.windows.older !== null);
    expect(recovered.windows.older?.rows[0]?.line[0]?.text).toBe("OLDER_ROW");
  },
);

test("a replaced connection cannot publish its held output into the surviving reader", async () => {
  await initial();
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  const held = await peer.request(OUTPUT, 1);
  const replacement = await wire();
  expect(replacement.requests(OUTPUT)).toHaveLength(0);
  peer.reply(held, page(90, "STALE_ROW\n"));
  const request = await replacement.request(OUTPUT);
  expect(request.params.beforeBytes).toBe(100);
  expect(reader.getSnapshot().windows.older).toBeNull();
  replacement.reply(request, page(90, "OLDER_ROW\n"));
  await observed((snapshot) => snapshot.windows.older !== null);
});

// A replacement reader inherits the pane's single output slot from the reader it
// replaced, so its first output request cannot be sent until that read settles.
// Its metadata can arrive first and report an unconfirmed row; if that parks the
// deadline, releasing the inherited slot finds no work due and the pane sits on
// its loading indicator with no request ever made. The initial tail is owed
// until it has actually been attempted, so it must still be sent.
test("a replacement reader still sends its first tail after unconfirmed metadata", async () => {
  await initial();
  // The previous reader's next tail is in flight when a DIFFERENT job takes the
  // pane, so the replacement inherits the pane's only output slot. A reader for
  // the same owner and job would be returned as-is rather than replaced.
  await vi.advanceTimersByTimeAsync(1000);
  peer.reply(await peer.request(META, 1), metadata());
  const held = await peer.request(OUTPUT, 1);
  const replacementReader = retainedJobLogReader(conversationPaneLifetime(pane), OWNER, "other_job");
  replacementReader.setMounted(true);

  // The replacement's metadata arrives first and reports the row unconfirmed,
  // before it has ever sent an output request of its own.
  peer.reply(await peer.request(META, 2), {
    ...jobOutputMetadata({
      jobId: "other_job",
      ownerSessionId: "session_output",
      ownerRef: OWNER,
      background: true,
      command: "other-producer",
      terminal: false,
      status: "running",
    }),
    statusUnconfirmed: true,
  });
  await observed((snapshot) => snapshot.job?.statusUnconfirmed === true, replacementReader);
  expect(peer.requests(OUTPUT)).toHaveLength(2);

  // The inherited slot frees, and the initial tail the replacement still owes is
  // sent rather than parked behind the unconfirmed status.
  peer.reply(held, page());
  const tail = await peer.request(OUTPUT, 2);
  expect(tail.params.beforeBytes).toBeUndefined();
  peer.reply(tail, page());
  await observed((snapshot) => snapshot.windows.live !== null, replacementReader);
});

test.each(["hide", "disconnect"] as const)("terminal drain survives %s and malformed replies", async (pause) => {
  const held = await peer.request(OUTPUT);
  peer.reply(await peer.request(META), metadata(true));
  await observed((snapshot) => snapshot.drainPending);
  if (pause === "hide") reader.setMounted(false);
  else connectionStore.setState({ state: "reconnecting" });
  peer.reply(held, page(0, "", 0));
  await vi.advanceTimersByTimeAsync(1000);
  expect(peer.requests(OUTPUT)).toHaveLength(1);
  expect(reader.getSnapshot().drainPending).toBe(true);
  if (pause === "hide") reader.setMounted(true);
  else connectionStore.setState({ state: "ready" });
  peer.reply(await peer.request(OUTPUT, 1), { ...page(), bytesReturned: 99 });
  await observed((snapshot) => snapshot.outputError !== null);
  expect(reader.getSnapshot().drainPending).toBe(true);
  await vi.advanceTimersByTimeAsync(1000);
  peer.reply(await peer.request(OUTPUT, 2), page(0, "FINAL_MARKER\n", 13));
  await observed((snapshot) => !snapshot.drainPending && snapshot.windows.totalBytes === 13);
});

test("same-job Refresh preserves older pages and requests exactly one new terminal settlement", async () => {
  await initial();
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  peer.reply(await peer.request(OUTPUT, 1), page(90, "OLDER_ROW\n"));
  await observed((snapshot) => snapshot.windows.older !== null);
  reader.capture({ byteOffset: 90, pixelOffset: -2, following: false });
  const older = reader.getSnapshot().windows.older;
  reader.refresh();
  const fresh = await peer.request(OUTPUT, 2);
  peer.reply(await peer.request(META, 1), metadata(true));
  await observed((snapshot) => snapshot.drainPending);
  peer.reply(fresh, page());
  peer.reply(await peer.request(OUTPUT, 3), page(100, "TAIL\n", 105, 95));
  const settled = await observed((snapshot) => !snapshot.drainPending && snapshot.windows.retainedStartBytes === 95);
  expect(settled.windows.older).toBe(older);
  expect(settled.scrollCapture).toEqual({ byteOffset: 90, pixelOffset: -2, following: false });
  await vi.advanceTimersByTimeAsync(5000);
  expect(peer.requests(OUTPUT)).toHaveLength(4);
  reader.demand({ direction: "backward", boundaryBytes: 90, limitBytes: 105 }, "start");
  expect(reader.getSnapshot().pendingDemand).toBeNull();
  expect(peer.requests(OUTPUT)).toHaveLength(4);
  reader.refresh();
  expect(reader.getSnapshot().drainPending).toBe(true);
  peer.reply(await peer.request(META, 2), metadata(true));
  peer.reply(await peer.request(OUTPUT, 4), page(100, "TAIL\n", 105, 95));
  await observed((snapshot) => !snapshot.drainPending);
  await vi.advanceTimersByTimeAsync(5000);
  expect(peer.requests(OUTPUT)).toHaveLength(5);
});

test("terminal jobs still read demanded history after live polling settles", async () => {
  peer.reply(await peer.request(META), metadata(true));
  await observed((snapshot) => snapshot.job?.terminal === true);
  peer.reply(await peer.request(OUTPUT), page());
  peer.reply(await peer.request(OUTPUT, 1), page());
  await observed((snapshot) => !snapshot.drainPending && snapshot.windows.totalBytes === 105);
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  const history = await peer.request(OUTPUT, 2);
  expect(history.params.beforeBytes).toBe(100);
  peer.reply(history, page(90, "OLDER_ROW\n"));
  await observed((snapshot) => snapshot.windows.older !== null);
  await vi.advanceTimersByTimeAsync(5000);
  expect(peer.requests(OUTPUT)).toHaveLength(3);
});

test("typed pruning reconciles the floor while keeping cached reading bytes", async () => {
  await initial();
  reader.capture({ byteOffset: 100, pixelOffset: 0, following: false });
  const reading = reader.getSnapshot().windows.older;
  reader.refresh();
  const fresh = await peer.request(OUTPUT, 1);
  reader.demand({ direction: "forward", boundaryBytes: 105, limitBytes: 200 }, "end");
  peer.reply(fresh, page(195, "TAIL\n", 200));
  await observed((snapshot) => snapshot.windows.totalBytes === 200);
  const request = await peer.request(OUTPUT, 2);
  expect(request.params).toMatchObject({ beforeBytes: 200, maxBytes: 95 });
  peer.fail(request, "pruned", { evenerErrorInfo: "jobOutputPruned", retainedStartBytes: 150, totalBytes: 200 });
  await observed((snapshot) => snapshot.windows.retainedStartBytes === 150);
  expect(reader.getSnapshot().windows.older).toBe(reading);
  await vi.advanceTimersByTimeAsync(1000);
  const retry = await peer.request(OUTPUT, 3);
  expect(retry.params).toMatchObject({ beforeBytes: 200, maxBytes: 50 });
});

test.each(["close", "owner", "job"] as const)(
  "%s replacement disposes the old reader and its read view",
  async (replacement) => {
    await initial();
    reader.setMounted(false);
    const lifetime = conversationPaneLifetime(pane);
    const view = [...lifetime.readViews.values()][0];
    if (replacement === "close") workspaceStore.getState().closePane(pane.id);
    else {
      const next = retainedJobLogReader(
        lifetime,
        replacement === "owner" ? "local:other" : OWNER,
        replacement === "job" ? "other_job" : JOB,
      );
      expect(next).not.toBe(reader);
      expect(lifetime.jobOutputReads.size).toBe(1);
    }
    reader.setMounted(true);
    reader.refresh();
    await vi.advanceTimersByTimeAsync(3000);
    expect(view?.alive).toBe(false);
    expect(peer.requests(OUTPUT)).toHaveLength(1);
    if (replacement === "close") expect(lifetime.jobOutputReads.size).toBe(0);
  },
);

test("closing a pane while connecting never dispatches its deferred reads", async () => {
  reader.setMounted(false);
  const heldPeer = new JobOutputPeer();
  const client = new AppwireClient({ url: "ws://job-output.test/rpc", socketFactory: () => heldPeer });
  clients.push(client);
  connectionStore.getState().connect(client);
  const connecting = client.connect();
  reader.setMounted(true);
  workspaceStore.getState().closePane(pane.id);
  heldPeer.open();
  await connecting;
  await vi.advanceTimersByTimeAsync(1000);
  expect(heldPeer.requests(OUTPUT)).toEqual([]);
  expect(heldPeer.requests(META)).toEqual([]);
});

test.each(["owner", "job"] as const)(
  "%s replacement waits for the pane's held output before dispatching",
  async (kind) => {
    const held = await peer.request(OUTPUT);
    peer.reply(await peer.request(META), metadata());
    await observed((snapshot) => snapshot.job !== null);
    const owner = kind === "owner" ? "local:other" : OWNER;
    const jobId = kind === "job" ? "other_job" : JOB;
    const replacement = retainedJobLogReader(conversationPaneLifetime(pane), owner, jobId);
    replacement.setMounted(true);
    peer.reply(await peer.request(META, 1), jobOutputMetadata({ jobId, ownerRef: owner }));
    await observed((snapshot) => snapshot.job?.jobId === jobId, replacement);
    expect(peer.requests(OUTPUT)).toHaveLength(1);
    peer.reply(held, page(0, "STALE\n", 6));
    const next = await peer.request(OUTPUT, 1);
    expect(next.params).toMatchObject({ ref: owner, jobId });
    expect(replacement.getSnapshot().windows.live).toBeNull();
    peer.reply(next, page(0, "FRESH\n", 6));
    const snapshot = await observed((state) => state.windows.live !== null, replacement);
    expect(snapshot.windows.live?.rows[0]?.line[0]?.text).toBe("FRESH");
  },
);

test("history completed before a live deadline does not consume the next demand's priority", async () => {
  await initial();
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  peer.reply(await peer.request(OUTPUT, 1), page(90, "OLDER_ROW\n"));
  await observed((snapshot) => snapshot.windows.older !== null);
  reader.setMounted(false);
  reader.demand({ direction: "backward", boundaryBytes: 90, limitBytes: 105 }, "start");
  await vi.advanceTimersByTimeAsync(1000);
  reader.setMounted(true);
  const history = await peer.request(OUTPUT, 2);
  expect(history.params.beforeBytes).toBe(90);
  peer.reply(history, page(80, "FIRST_ROW\n"));
  const live = await peer.request(OUTPUT, 3);
  expect(live.params.beforeBytes).toBeUndefined();
});

test("control-only history advances the byte frontier until a visible row arrives", async () => {
  await initial();
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  peer.reply(await peer.request(OUTPUT, 1), page(95, "\u001b[31m"));
  const continued = await peer.request(OUTPUT, 2);
  expect(continued.params.beforeBytes).toBe(95);
  expect(reader.getSnapshot().windows.older?.rows).toEqual([]);
  peer.reply(continued, page(85, "OLDER_ROW\n"));
  const visible = await observed(
    (snapshot) => snapshot.pendingDemand === null && snapshot.windows.older?.offsetBytes === 85,
  );
  expect(visible.windows.older?.rows[0]?.line[0]?.text).toBe("OLDER_ROW");
});

test("reconnect preserves a visible later-page row and reconciles the new floor", async () => {
  await initial();
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  peer.reply(await peer.request(OUTPUT, 1), page(90, "OLDER_ROW\n"));
  await observed((snapshot) => snapshot.windows.older?.offsetBytes === 90);
  reader.demand({ direction: "backward", boundaryBytes: 90, limitBytes: 105 }, "start");
  peer.reply(await peer.request(OUTPUT, 2), page(80, "FIRST_ROW\n"));
  await observed((snapshot) => snapshot.windows.older?.offsetBytes === 80);
  reader.capture({ byteOffset: 80, pixelOffset: -4, following: false });
  const older = reader.getSnapshot().windows.older;
  const replacement = await wire();
  replacement.reply(await replacement.request(META), metadata());
  replacement.reply(await replacement.request(OUTPUT), page(100, "TAIL\n", 105, 95));
  const recovered = await observed((snapshot) => snapshot.windows.retainedStartBytes === 95);
  expect(recovered.windows.older).toBe(older);
  expect(recovered.windows.older?.rows[0]?.line[0]?.text).toBe("FIRST_ROW");
  expect(recovered.scrollCapture).toEqual({ byteOffset: 80, pixelOffset: -4, following: false });
});

// A retained row whose nonterminal status is unconfirmed describes the intact
// prefix, not the job: the finish may be the line that failed to decode. Polling
// it at the live rate asks an unanswerable question once a second, forever. This
// asserts the SCHEDULE — no further metadata or output read is requested — and
// not the wording a surface renders.
test("an unconfirmed nonterminal status schedules no further live reads", async () => {
  const first = await peer.request(OUTPUT);
  peer.reply(await peer.request(META), {
    ...metadata(false),
    diagnostics: ["journal_scan_incomplete"],
    statusUnconfirmed: true,
  });
  peer.reply(first, page());
  await observed((snapshot) => snapshot.windows.live !== null && snapshot.job !== null);

  // Both initial replies have settled and the row is known to be unconfirmed, so
  // the count is read HERE rather than after the clock moves. A baseline taken
  // later would hide an extra poll sent in the window between the two replies,
  // because the fake peer holds a request open and the count would already
  // include it.
  const before = { metadata: peer.requests(META).length, output: peer.requests(OUTPUT).length };
  await vi.advanceTimersByTimeAsync(5000);

  expect(peer.requests(META)).toHaveLength(before.metadata);
  expect(peer.requests(OUTPUT)).toHaveLength(before.output);
});

// A failed output attempt must not leave the read due. The metadata may already
// have settled on an unconfirmed row, in which case only the failure was pacing
// the retry — and a reader would see the same error re-firing every second for
// a job whose journal cannot answer.
test("a failed output attempt stops automatic tail reads for an unconfirmed job", async () => {
  peer.reply(await peer.request(META), { ...metadata(false), statusUnconfirmed: true });
  peer.fail(await peer.request(OUTPUT), "output unavailable");
  await observed((snapshot) => snapshot.outputError !== null && snapshot.job !== null);

  const before = { metadata: peer.requests(META).length, output: peer.requests(OUTPUT).length };
  await vi.advanceTimersByTimeAsync(5000);

  expect(peer.requests(OUTPUT)).toHaveLength(before.output);
  expect(peer.requests(META)).toHaveLength(before.metadata);
});

// The counterpart, so the stop above cannot be satisfied by never retrying: a
// healthy live job whose output read failed retries at the normal cadence.
test("a failed output attempt for a confirmed running job still retries", async () => {
  peer.reply(await peer.request(META), metadata());
  peer.fail(await peer.request(OUTPUT), "output unavailable");
  const failed = await observed((snapshot) => snapshot.outputError !== null);
  expect(failed.outputError).not.toBeNull();

  const before = peer.requests(OUTPUT).length;
  await vi.advanceTimersByTimeAsync(1500);
  expect(peer.requests(OUTPUT).length).toBeGreaterThan(before);
});

// The stop for an unconfirmed job must not close the output slot. With a page
// already cached, a failed tail must leave paging able to use the slot; parking
// the slot's own deadline would strand an older-page request forever.
test("paging still runs after a failed tail on an unconfirmed job", async () => {
  await initial();
  // The next metadata read is paced, so the clock has to reach it before the
  // request exists to answer.
  await vi.advanceTimersByTimeAsync(1000);
  peer.reply(await peer.request(META, 1), { ...metadata(false), statusUnconfirmed: true });
  await observed((snapshot) => snapshot.job?.statusUnconfirmed === true);
  await vi.advanceTimersByTimeAsync(1000);
  peer.fail(await peer.request(OUTPUT, 1), "output unavailable");
  await observed((snapshot) => snapshot.outputError !== null);

  reader.capture({ byteOffset: 100, pixelOffset: -3, following: false });
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  await vi.advanceTimersByTimeAsync(1000);
  const history = await peer.request(OUTPUT, 2);
  expect(history.params.beforeBytes).toBe(100);
});

// An explicit refresh asks for a tail read even when the row is unconfirmed. If
// an older page holds the slot, metadata that arrives first and still reports
// the row unconfirmed must not cancel the read the person asked for; it only
// stops the RECURRING cadence.
test("an explicit refresh tail survives unconfirmed metadata while a page holds the slot", async () => {
  await initial();
  reader.capture({ byteOffset: 100, pixelOffset: -3, following: false });
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  const held = await peer.request(OUTPUT, 1);

  // Refresh while the history page holds the slot, and answer the metadata
  // first, still unconfirmed.
  const metaBefore = peer.requests(META).length;
  reader.refresh();
  peer.reply(await peer.request(META, metaBefore), { ...metadata(false), statusUnconfirmed: true });
  await observed((snapshot) => snapshot.job?.statusUnconfirmed === true);

  // The held page finishing frees the slot, and the owed tail read still runs.
  peer.reply(held, page(90, "OLDER_ROW\n", 105));
  await vi.advanceTimersByTimeAsync(1000);
  const tail = await peer.request(OUTPUT, 2);
  expect(tail.params.beforeBytes).toBeUndefined();
});

// The counterpart: a healthy live row keeps the normal cadence, so the stop
// above cannot be satisfied by simply stopping for every job.
test("a confirmed running status keeps polling at the live rate", async () => {
  await initial();
  const before = peer.requests(META).length;
  await vi.advanceTimersByTimeAsync(1000);
  expect(peer.requests(META).length).toBeGreaterThan(before);
});

// A refresh issued while a tail read is still in flight, whose metadata arrives
// BEFORE that old tail fails, must still send the tail it owes. The old failure
// starts the slot's one-second backoff and parks the live deadline, so a wakeup
// computed from the live deadline alone is never armed — the owed read waits
// forever and the pane keeps stale output or the old error. This asserts the
// schedule: after the backoff expires the requested tail is requested exactly
// once, and the recurring poll stays stopped while the row is unconfirmed.
test("a refresh tail owed when the previous tail fails is still sent", async () => {
  await initial();

  // A second live tail is in flight when Refresh is pressed.
  await vi.advanceTimersByTimeAsync(1000);
  const inFlight = await peer.request(OUTPUT, 1);
  reader.refresh();

  // The refresh's metadata lands first and still reports the row unconfirmed.
  peer.reply(await peer.request(META, 1), { ...metadata(false), statusUnconfirmed: true });
  await observed((snapshot) => snapshot.job?.statusUnconfirmed === true);

  // The old tail then fails: this parks the live deadline and sets the backoff.
  peer.fail(inFlight, "output unavailable");
  await observed((snapshot) => snapshot.outputError !== null);

  // Once the backoff expires the owed tail is requested, without the recurring
  // poll resuming.
  await vi.advanceTimersByTimeAsync(1000);
  const owed = await peer.request(OUTPUT, 2);
  expect(owed.params.beforeBytes).toBeUndefined();
  peer.reply(owed, page());
  await reader.settleForTests();

  const after = { metadata: peer.requests(META).length, output: peer.requests(OUTPUT).length };
  await vi.advanceTimersByTimeAsync(5000);
  expect(peer.requests(META)).toHaveLength(after.metadata);
  expect(peer.requests(OUTPUT)).toHaveLength(after.output);
});

// A terminal row's tail is the read that captures its final bytes, so a
// transient failure there must not park the deadline. The reconnect that asked
// for the read has already been served, and unchanged terminal metadata creates
// no fresh drain, so nothing else would ask again: the pane would keep its old
// output and error until some unrelated refresh. One retry, then the schedule
// settles again once the terminal page arrives.
test("a failed reconnect tail on a settled terminal job is retried", async () => {
  const first = await peer.request(OUTPUT);
  peer.reply(await peer.request(META), metadata(true));
  peer.reply(first, page(0, "FINAL\n", 6));
  // The terminal observation arms a drain of its own; settling it is what leaves
  // the reader with nothing due, which is the state the reconnect starts from.
  peer.reply(await peer.request(OUTPUT, 1), page(6, "", 6));
  await observed((state) => state.job?.terminal === true && !state.drainPending && state.windows.live !== null);
  const settled = peer.requests(OUTPUT).length;

  connectionStore.setState({ state: "reconnecting" });
  connectionStore.setState({ state: "ready" });
  peer.fail(await peer.request(OUTPUT, settled), "temporarily unavailable");
  await observed((state) => state.outputError !== null);

  // Unchanged terminal metadata lands BEFORE the backoff expires. Reconciliation
  // sees a settled row and parks the deadline, so the retry has to be owed
  // independently of it or the failed read is lost.
  peer.reply(await peer.request(META, 1), metadata(true));
  await observed((state) => state.metadataError === null && state.job?.terminal === true);

  await vi.advanceTimersByTimeAsync(1000);
  expect(peer.requests(OUTPUT)).toHaveLength(settled + 2);
  const retry = peer.requests(OUTPUT)[settled + 1];
  if (!retry) throw new Error("missing retry request");
  expect(retry.params.beforeBytes).toBeUndefined();
  peer.reply(retry, page(6, "AFTER\n", 12));
  const snapshot = await observed((state) => state.windows.totalBytes === 12);
  expect(snapshot.outputError).toBeNull();

  const after = peer.requests(OUTPUT).length;
  await vi.advanceTimersByTimeAsync(5000);
  expect(peer.requests(OUTPUT)).toHaveLength(after);
});

// A refresh asks for the row as it is NOW. A metadata read already in flight
// when the person pressed answers a question about the row BEFORE the press, so
// its unconfirmed reply may publish what it learned but must not settle the
// schedule: parking the deadline on it leaves the pane showing the old status
// while the refreshed tail sits behind a deadline nothing will re-arm. One read
// issued after the press decides whether recurring polling continues.
test("an in-flight metadata reply does not settle a refresh that started after it", async () => {
  await initial();

  // The paced metadata read is in flight when Refresh is pressed.
  await vi.advanceTimersByTimeAsync(1000);
  const inFlight = await peer.request(META, 1);
  reader.refresh();

  peer.reply(inFlight, { ...metadata(false), statusUnconfirmed: true });
  await vi.advanceTimersByTimeAsync(2500);
  expect(peer.requests(META).length).toBeGreaterThan(2);

  // The post-press reply is the one that settles the schedule, and it sees the
  // finish the stale reply could not.
  const fresh = await peer.request(META, 2);
  peer.reply(fresh, metadata(true));
  const snapshot = await observed((state) => state.job?.terminal === true);
  expect(snapshot.job?.statusUnconfirmed).not.toBe(true);
});

// Hiding the pane discards the reply of a tail that was still in flight, and a
// reply nobody can use has not done the work it was sent for. Starting the read
// had already cleared the explicit obligation, and an earlier settled tail left
// the initial load no longer owed, so nothing re-armed it: the pane came back to
// its old output and error and waited for some unrelated refresh. The pane keeps
// its single output slot; the replacement tail is the one the press asked for.
test("a tail invalidated by hiding the pane is owed again when it returns", async () => {
  await initial();
  reader.refresh();
  const held = await peer.request(OUTPUT, 1);

  // The refresh's metadata lands first, reports the row unconfirmed, and parks
  // the live deadline while that tail is still in flight.
  peer.reply(await peer.request(META, 1), { ...metadata(false), statusUnconfirmed: true });
  await observed((snapshot) => snapshot.job?.statusUnconfirmed === true);

  const view = [...conversationPaneLifetime(pane).readViews.values()][0];
  if (!view) throw new Error("missing output read view");
  view.setReadable(false);
  view.setReadable(true);

  // The held reply now belongs to an older generation, so it publishes nothing
  // and settling it must still leave a tail owed.
  peer.reply(held, page(100, "STALE\n"));
  await vi.advanceTimersByTimeAsync(2000);
  expect(peer.requests(OUTPUT).length).toBeGreaterThan(2);
  expect(reader.getSnapshot().windows.live?.bytes).toEqual(new Uint8Array([84, 65, 73, 76, 10]));

  const replacement = await peer.request(OUTPUT, 2);
  expect(replacement.params.beforeBytes).toBeUndefined();
  peer.reply(replacement, page(105, "FRESH", 110));
  const snapshot = await observed((state) => state.windows.totalBytes === 110);
  expect(snapshot.windows.live?.bytes).toEqual(new Uint8Array([84, 65, 73, 76, 10, 70, 82, 69, 83, 72]));
});
