import { AppwireClient, type JobOutputPage } from "@evener/appwire-client";
import { connectJobOutputPeer, JobOutputPeer } from "@evener/appwire-client/testing/jobOutputPeer";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore, resetConnectionStoreForTests } from "../../stores/connection";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { jobOutputMetadata } from "./JobLogTestUtils";
import {
  type JobLogReader,
  type JobLogReaderSnapshot,
  retainedJobLogReader,
  UNCONFIRMED_RECHECK_MS,
} from "./jobLogReader";

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

function identifiedPage(
  logId: string,
  offsetBytes = 100,
  data = "TAIL\n",
  totalBytes = 105,
  retainedStartBytes = 0,
): JobOutputPage {
  return { ...page(offsetBytes, data, totalBytes, retainedStartBytes), logId };
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

test("a failing tail does not starve the history page queued behind it", async () => {
  await initial();
  // A page is queued, then the person presses Refresh while it is in flight.
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  const history = await peer.request(OUTPUT, 1);
  reader.refresh();
  // The page fails first, which gives the owed tail the next slot.
  peer.fail(history, "temporary output failure");
  await observed((snapshot) => snapshot.outputError !== null);
  await vi.advanceTimersByTimeAsync(1000);
  const tail = await peer.request(OUTPUT, 2);
  expect(tail.params.beforeBytes).toBeUndefined();
  // The tail fails too. A failed attempt is still an attempt, so the queued page
  // takes the next paced slot rather than the tail retrying forever.
  peer.fail(tail, "tail unavailable");
  await vi.advanceTimersByTimeAsync(1000);
  const retriedHistory = await peer.request(OUTPUT, 3);
  expect(retriedHistory.params.beforeBytes).toBe(100);
  peer.reply(retriedHistory, page(90, "OLDER_ROW\n"));
  const recovered = await observed((snapshot) => snapshot.windows.older !== null);
  expect(recovered.windows.older?.rows[0]?.line[0]?.text).toBe("OLDER_ROW");
});

test("a response naming a different log replaces the cached bytes instead of failing the overlap check", async () => {
  await initial(identifiedPage("scoped"));
  // A refresh re-reads the job after the owner's journal became consultable, so
  // the server now answers with the OWNER's log. It overlaps the cached range and
  // holds different bytes there, which is exactly what the overlap check rejects.
  reader.refresh();
  await vi.advanceTimersByTimeAsync(1000);
  const authoritative = await peer.request(OUTPUT, 1);
  // Same lifetime total, overlapping range, different bytes: the only thing that
  // distinguishes this file from the cached one is the identity it carries.
  peer.reply(authoritative, identifiedPage("owner", 90, "AUTHORITATIVE\n", 105));
  const settled = await observed(
    (snapshot) => snapshot.outputError !== null || snapshot.windows.live?.bytes.length === "AUTHORITATIVE\n".length,
  );
  // A rejection here is the defect: the page is the authoritative log, and the
  // only reason it can be refused is that it was compared against bytes from the
  // file it replaced.
  expect(settled.outputError).toBeNull();
  const switched = settled;
  // The pane shows the authoritative log's bytes, not the fallback's, and it is
  // not stuck reporting a mismatch with a log that is no longer being read.
  expect(switched.windows.live?.rows.map((row) => row.line[0]?.text)).toContain("AUTHORITATIVE");
  expect(switched.windows.live?.bytes.length).toBe(14);
});

// A production daemon omitted logId on its pages, so a pane could cache bytes
// from the daemon's default log with no identity at all. When the daemon exits
// the hub serves the OWNER's retained log instead, and the first reply that
// names a file cannot be matched against a cache that names nothing — so it must
// replace the bytes, or the overlap check refuses the authoritative page against
// the file it superseded.
test("an unnamed cache does not defeat the first identity the server states", async () => {
  // The daemon's page carries no logId, as production daemons' did.
  await initial(page(90, "DAEMON_DEFAULT\n", 105));
  expect(reader.getSnapshot().windows.live?.rows.map((row) => row.line[0]?.text)).toContain("DAEMON_DEFAULT");

  reader.refresh();
  await vi.advanceTimersByTimeAsync(1000);
  const authoritative = await peer.request(OUTPUT, 1);
  // The retained read names the owner's log. It overlaps the cached range and
  // holds different bytes there.
  peer.reply(authoritative, identifiedPage("owner-log", 90, "OWNER_RETAINED\n", 105));
  const settled = await observed(
    (snapshot) =>
      snapshot.outputError !== null ||
      snapshot.windows.live?.rows.some((row) => row.line[0]?.text === "OWNER_RETAINED") === true,
  );
  expect(settled.outputError).toBeNull();
  expect(settled.windows.live?.rows.map((row) => row.line[0]?.text)).toContain("OWNER_RETAINED");

  // And the identity was committed only because the page was accepted, so the
  // next page of that same log merges normally rather than being treated as
  // another replacement.
  reader.demand({ direction: "backward", boundaryBytes: 90, limitBytes: 105 }, "start");
  peer.reply(await peer.request(OUTPUT, 2), identifiedPage("owner-log", 82, "EARLIER\n", 105));
  const paged = await observed((snapshot) => snapshot.windows.older !== null || snapshot.outputError !== null);
  expect(paged.outputError).toBeNull();
  expect(paged.windows.older?.rows.map((row) => row.line[0]?.text)).toContain("EARLIER");
});

test("a failing history page does not starve the tail a refresh asked for", async () => {
  await initial();
  // A page is queued and takes the one output slot.
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  const history = await peer.request(OUTPUT, 1);
  expect(history.params.beforeBytes).toBe(100);
  // The person presses Refresh while that page is in flight, so a tail is
  // owed. The page then fails.
  reader.refresh();
  peer.fail(history, "temporary output failure");
  await observed((snapshot) => snapshot.outputError !== null);
  // The failing page must not keep winning the slot: the tail is the work that
  // was asked for, and it has to be ATTEMPTED on the next wakeup.
  await vi.advanceTimersByTimeAsync(1000);
  const tail = await peer.request(OUTPUT, 2);
  expect(tail.params.beforeBytes).toBeUndefined();
  peer.reply(tail, page());
  const served = await observed((snapshot) => snapshot.outputError === null && snapshot.windows.live !== null);
  // The page's cached bytes and its boundary survive, so the demand retries
  // rather than being discarded with the failure.
  expect(served.pendingDemand?.boundaryBytes).toBe(100);
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

// A history boundary measured against the cached fallback log can point past the
// EOF of the log the server is serving now. The server refuses it before reading
// anything, so retrying it asks the same unanswerable question; the pane would
// spend the rest of its life doing exactly that, because nothing else in a
// settled terminal state asks for output. The refused boundary is dropped and a
// latest read is owed instead: it carries no offset to refuse, and whichever log
// answers it establishes the identity that replaces the cache.
test("a refused history boundary asks for the latest page instead of retrying it", async () => {
  // A TERMINAL pane is the hard case: the recurring cadence stops once the
  // drain settles, so a boundary retried forever would be the only thing left
  // asking, and nothing would ever discover the replacement log. A refusal is
  // the server saying this offset does not exist, so the request has to become
  // a question about the log that does.
  peer.reply(await peer.request(META), metadata(true));
  peer.reply(await peer.request(OUTPUT), identifiedPage("fallback-log"));
  await observed((snapshot) => snapshot.windows.live !== null && snapshot.job?.terminal === true);
  reader.demand({ direction: "backward", boundaryBytes: 100, limitBytes: 105 }, "start");
  const history = await peer.request(OUTPUT, 1);
  expect(history.params.beforeBytes).toBe(100);

  // The server refuses the offset on its shape: it is outside the log the read
  // selected. Nothing was read.
  peer.fail(history, "invalid job output beforeBytes", undefined, -32602);
  const refused = await observed((snapshot) => snapshot.pendingDemand === null || snapshot.outputError !== null);
  expect(refused.pendingDemand).toBeNull();
  // The cached output stays on screen until a reply establishes its replacement.
  expect(refused.windows.live?.bytes.length).toBeGreaterThan(0);

  // One latest read, not the doomed boundary.
  const latest = await peer.request(OUTPUT, 2);
  expect(latest.params.beforeBytes).toBeUndefined();
  // The owner's log is SHORTER than the fallback's and carries different bytes.
  peer.reply(latest, identifiedPage("owner-log", 4, "NEW\n", 8));
  const replaced = await observed(
    (snapshot) => snapshot.windows.live?.rows.some((row) => row.line[0]?.text === "NEW") === true,
  );
  expect(replaced.windows.totalBytes).toBe(8);

  // And the schedule settles: no retry of the boundary, and no repeating latest
  // read either, because the reply that arrived was a successful tail.
  const settled = { metadata: peer.requests(META).length, output: peer.requests(OUTPUT).length };
  await vi.advanceTimersByTimeAsync(5000);
  expect(peer.requests(OUTPUT)).toHaveLength(settled.output);
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

// Unconfirmed means "the evidence cannot say YET", and for a torn tail the
// evidence can change: resuming the session completes the append or writes the
// finish, while the pane is still connected and still stopped. Stopping without
// ever looking again is a one-way door, so a recheckable row is re-read on a slow
// cadence and the finish it finds takes over as a normal terminal observation.
test("a torn-tail unconfirmed row is re-checked and follows a later finish", async () => {
  const first = await peer.request(OUTPUT);
  peer.reply(await peer.request(META), {
    ...metadata(false),
    diagnostics: ["journal_torn_tail"],
    statusUnconfirmed: true,
    statusRecheckable: true,
  });
  peer.reply(first, page());
  await observed((snapshot) => snapshot.job?.statusUnconfirmed === true && snapshot.windows.live !== null);
  const settled = { metadata: peer.requests(META).length, output: peer.requests(OUTPUT).length };

  // Not at the live rate: the row is being re-checked, not followed.
  await vi.advanceTimersByTimeAsync(2000);
  expect(peer.requests(META)).toHaveLength(settled.metadata);

  // The recheck lands on the repair: the journal now records the finish.
  await vi.advanceTimersByTimeAsync(14000);
  const recheck = peer.requests(META)[settled.metadata];
  if (!recheck)
    throw new Error(`no recheck request, meta=${peer.requests(META).length} want more than ${settled.metadata}`);
  peer.reply(recheck, metadata(true));
  const finished = await observed(
    (snapshot) => snapshot.job?.terminal === true && snapshot.job.statusUnconfirmed !== true,
  );
  expect(finished.job?.status).toBe("completed");

  // And the finish is drained, so the pane shows the final bytes. The reply
  // APPENDS to the cached range rather than rewriting it: a page that replaced
  // the cached bytes at the same offsets would be refused by the overlap guard,
  // and a predicate that only checked the total would pass on the initial page
  // while the drain was still pending.
  await vi.advanceTimersByTimeAsync(1000);
  const drain = await peer.request(OUTPUT, settled.output);
  peer.reply(drain, page(105, "DONE\n", 110));
  const drained = await observed((snapshot) => !snapshot.drainPending && snapshot.windows.totalBytes === 110);
  expect(drained.windows.live?.offsetBytes).toBe(100);
  expect(drained.windows.live?.bytes.length).toBe(10);
});

// The counterpart that makes the distinction real: durable corruption cannot be
// read past, so no waiting turns it into an answer and the reader must not keep
// asking. This is the case that stays stopped forever.
test("a durable-corruption unconfirmed row is never re-checked", async () => {
  const first = await peer.request(OUTPUT);
  peer.reply(await peer.request(META), {
    ...metadata(false),
    diagnostics: ["journal_scan_incomplete"],
    statusUnconfirmed: true,
    statusRecheckable: false,
  });
  peer.reply(first, page());
  await observed((snapshot) => snapshot.job?.statusUnconfirmed === true && snapshot.windows.live !== null);

  const settled = { metadata: peer.requests(META).length, output: peer.requests(OUTPUT).length };
  await vi.advanceTimersByTimeAsync(60000);
  expect(peer.requests(META)).toHaveLength(settled.metadata);
  expect(peer.requests(OUTPUT)).toHaveLength(settled.output);
});

// The recheck is about THIS row's evidence, not about the aggregate. An ancestor
// journal with a valid forwarded start followed by a malformed line leaves
// `journal_scan_incomplete` on the response, while the row itself came from the
// owner's torn tail — which completing the append repairs. Deriving the recheck
// from the aggregate cancelled it, and the pane missed the finish until a manual
// refresh.
test("an ancestor's durable corruption does not cancel a torn-tail row's recheck", async () => {
  const first = await peer.request(OUTPUT);
  // Both codes travel: the aggregate damage AND this record's recheckable state.
  peer.reply(await peer.request(META), {
    ...metadata(false),
    diagnostics: ["journal_scan_incomplete", "journal_torn_tail"],
    statusUnconfirmed: true,
    statusRecheckable: true,
  });
  peer.reply(first, page());
  await observed((snapshot) => snapshot.job?.statusUnconfirmed === true && snapshot.windows.live !== null);
  const settled = { metadata: peer.requests(META).length, output: peer.requests(OUTPUT).length };

  await vi.advanceTimersByTimeAsync(UNCONFIRMED_RECHECK_MS);
  const recheck = peer.requests(META)[settled.metadata];
  if (!recheck)
    throw new Error(
      `no recheck request, meta=${peer.requests(META).length} want more than ${settled.metadata}: ` +
        "the recheck follows the record's own evidence, not the aggregate diagnostics",
    );
  peer.reply(recheck, metadata(true));
  const finished = await observed(
    (snapshot) => snapshot.job?.terminal === true && snapshot.job.statusUnconfirmed !== true,
  );
  expect(finished.job?.status).toBe("completed");

  // The finish is drained, so the record's own evidence really did reach the
  // pane. Appended, not replaced: see the note in the recheck-and-follow test —
  // a same-offset rewrite is refused by the overlap guard, and a total-only
  // predicate would pass before the drain landed.
  await vi.advanceTimersByTimeAsync(1000);
  const drain = await peer.request(OUTPUT, settled.output);
  peer.reply(drain, page(105, "DONE\n", 110));
  const drained = await observed((snapshot) => !snapshot.drainPending && snapshot.windows.totalBytes === 110);
  expect(drained.windows.live?.offsetBytes).toBe(100);
  expect(drained.windows.live?.bytes.length).toBe(10);
});

// A tail that fails transiently while the row is provisional must not be dropped
// for good. The metadata says a later read could establish the status, so the
// pane is still waiting on evidence; a slow recheck that SUCCEEDS re-arms one
// tail, and the output arrives without any refresh or reconnect. Without that,
// the error stays on screen indefinitely after the log became readable.
//
// This drives the reader rather than asserting on a flag: the assertion is that
// the bytes reach the windows.
test("a successful slow recheck re-arms a tail that failed on a recheckable row", async () => {
  // The metadata lands first and says the row is provisional; the initial tail
  // then fails. That failure is the one the pane must not keep forever.
  peer.reply(await peer.request(META), {
    ...metadata(false),
    statusUnconfirmed: true,
    statusRecheckable: true,
  });
  peer.fail(await peer.request(OUTPUT), "output unavailable");
  await observed((snapshot) => snapshot.outputError !== null && snapshot.job?.statusUnconfirmed === true);

  // No retry at the live rate: the row is provisional, so the tail waits for
  // evidence rather than asking once a second.
  const afterFailure = peer.requests(OUTPUT).length;
  await vi.advanceTimersByTimeAsync(2000);
  expect(peer.requests(OUTPUT)).toHaveLength(afterFailure);

  // The slow recheck lands, still provisional. That is new evidence, and it owes
  // one tail — which succeeds, so the bytes arrive and the error clears.
  await vi.advanceTimersByTimeAsync(UNCONFIRMED_RECHECK_MS);
  peer.reply(await peer.request(META, peer.requests(META).length - 1), {
    ...metadata(false),
    statusUnconfirmed: true,
    statusRecheckable: true,
  });
  const retried = await peer.request(OUTPUT, afterFailure);
  peer.reply(retried, page(0, "RECOVERED\n", 10));
  const recovered = await observed((snapshot) => snapshot.outputError === null && snapshot.windows.live !== null);
  expect(recovered.outputError).toBeNull();
  expect(recovered.windows.live?.rows.length).toBeGreaterThan(0);
});

// A successful slow recheck re-arms only a tail that FAILED. The row stays
// unconfirmed and recheckable, so reconciliation runs on every slow tick, but
// the tail that already delivered its bytes is not owed again: re-arming it
// would fetch another output page and repeat the journal scans on every tick for
// a pane with nothing new to show.
test("a slow recheck does not re-arm a tail that already loaded", async () => {
  peer.reply(await peer.request(META), {
    ...metadata(false),
    statusUnconfirmed: true,
    statusRecheckable: true,
  });
  peer.reply(await peer.request(OUTPUT), page());
  await observed((snapshot) => snapshot.windows.live !== null && snapshot.outputError === null);
  const loaded = peer.requests(OUTPUT).length;

  // Two slow rechecks land, both still provisional and recheckable.
  for (let tick = 1; tick <= 2; tick += 1) {
    await vi.advanceTimersByTimeAsync(UNCONFIRMED_RECHECK_MS);
    peer.reply(await peer.request(META, peer.requests(META).length - 1), {
      ...metadata(false),
      statusUnconfirmed: true,
      statusRecheckable: true,
    });
    await vi.advanceTimersByTimeAsync(2000);
    expect(peer.requests(OUTPUT)).toHaveLength(loaded);
  }
});

// The counterpart the recovery case needs: a slow recheck can move the row from
// a forwarded fallback to the owner's own record. That is the same row with a
// different SOURCE, and the owner's log can be a different file — but the tail
// that already loaded left liveTailFailed false, so nothing is owed and the
// recurring cadence stays parked. The pane then shows the fallback's bytes under
// the recovered owner's metadata until something else asks for output.
//
// One tail read is owed for that transition, and the bytes that arrive are the
// proof: this asserts on the output, not on a flag.
test("a recheck that recovers the owner from a fallback re-reads the output", async () => {
  // The first read is fallback evidence: the scoped copy stood in for an owner
  // that could not be consulted, and its status is unconfirmed and recheckable.
  peer.reply(await peer.request(META), {
    ...metadata(false),
    diagnostics: ["forwarded_fallback", "owner_unavailable"],
    statusUnconfirmed: true,
    statusRecheckable: true,
  });
  peer.reply(await peer.request(OUTPUT), page(0, "SCOPED_COPY\n", 12));
  await observed((snapshot) => snapshot.windows.live !== null && snapshot.job?.statusUnconfirmed === true);
  const loaded = peer.requests(OUTPUT).length;

  // The recheck reaches the owner. The codes are gone and the row is still
  // provisional — the owner's finish is torn — so nothing about the status
  // schedule changes and no FAILED tail is owed.
  await vi.advanceTimersByTimeAsync(UNCONFIRMED_RECHECK_MS);
  peer.reply(await peer.request(META, peer.requests(META).length - 1), {
    ...metadata(false),
    diagnostics: ["journal_torn_tail"],
    statusUnconfirmed: true,
    statusRecheckable: true,
  });
  await observed((snapshot) => !(snapshot.job?.diagnostics ?? []).includes("forwarded_fallback"));

  // One tail read follows, and it delivers the owner's bytes. It is a demand
  // rather than a poll, so it goes out on the tick the recheck settled.
  await vi.advanceTimersByTimeAsync(1);
  expect(peer.requests(OUTPUT)).toHaveLength(loaded + 1);
  const reread = await peer.request(OUTPUT, loaded);
  expect(reread.params.beforeBytes).toBeUndefined();
  peer.reply(reread, identifiedPage("owner-log", 0, "OWNER_LOG\n", 10));
  const recovered = await observed(
    (snapshot) => snapshot.windows.live?.rows.some((row) => row.line[0]?.text === "OWNER_LOG") === true,
  );
  expect(recovered.windows.live?.rows.map((row) => row.line[0]?.text)).not.toContain("SCOPED_COPY");

  // And it is ONE read: the next slow tick, still provisional and with no failed
  // tail, re-arms nothing.
  const afterRecovery = peer.requests(OUTPUT).length;
  await vi.advanceTimersByTimeAsync(UNCONFIRMED_RECHECK_MS);
  peer.reply(await peer.request(META, peer.requests(META).length - 1), {
    ...metadata(false),
    diagnostics: ["journal_torn_tail"],
    statusUnconfirmed: true,
    statusRecheckable: true,
  });
  await vi.advanceTimersByTimeAsync(2000);
  expect(peer.requests(OUTPUT)).toHaveLength(afterRecovery);
});

// A background recheck that FAILS must stay on the cadence it was scheduled at.
// Escalating to the live rate because the requests are failing turns a server
// error into a load problem, and it cannot produce the status any sooner than
// the recheck already scheduled. A person asking again is still immediate.
test("a failed recheck on an unconfirmed row stays on the slow cadence", async () => {
  const first = await peer.request(OUTPUT);
  peer.reply(await peer.request(META), {
    ...metadata(false),
    diagnostics: ["journal_torn_tail"],
    statusUnconfirmed: true,
    statusRecheckable: true,
  });
  peer.reply(first, page());
  await observed((snapshot) => snapshot.job?.statusUnconfirmed === true && snapshot.windows.live !== null);

  await vi.advanceTimersByTimeAsync(UNCONFIRMED_RECHECK_MS);
  const recheck = peer.requests(META).length;
  peer.fail(await peer.request(META, recheck - 1), "metadata unavailable");
  await observed((snapshot) => snapshot.metadataError !== null);

  // The live rate would have asked twice by now.
  await vi.advanceTimersByTimeAsync(2000);
  expect(peer.requests(META)).toHaveLength(recheck);

  // The slow cadence still asks, so the row is not abandoned either.
  await vi.advanceTimersByTimeAsync(UNCONFIRMED_RECHECK_MS);
  expect(peer.requests(META).length).toBeGreaterThan(recheck);
  // Settle that attempt, so the metadata slot is free for the refresh below.
  peer.reply(await peer.request(META, peer.requests(META).length - 1), {
    ...metadata(false),
    statusUnconfirmed: true,
    statusRecheckable: true,
  });
  await observed((snapshot) => snapshot.metadataError === null && snapshot.job?.statusUnconfirmed === true);

  // And a refresh is still immediate, because a person asked for it.
  const beforeRefresh = peer.requests(META).length;
  reader.refresh();
  const requested = await peer.request(META, beforeRefresh);
  expect(requested.method).toBe(META);
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

// The same rule holds when the stale reply FAILS. A slow recheck issued before
// the press answers a question about the row before it, so its failure must not
// replace the refresh's deadline with the fifteen-second recheck backoff: the
// slot is free and the person is waiting on a request that has not been sent
// yet. Only an unattended recheck backs off.
test("a stale failed metadata reply does not delay the refreshed request", async () => {
  // The row is provisional, so its rechecks run on the slow cadence and a
  // failure there is the case that backs off for fifteen seconds.
  peer.reply(await peer.request(META), {
    ...metadata(false),
    statusUnconfirmed: true,
    statusRecheckable: true,
  });
  peer.reply(await peer.request(OUTPUT), page());
  await observed((snapshot) => snapshot.job?.statusUnconfirmed === true && snapshot.windows.live !== null);

  // The slow recheck is in flight when Refresh is pressed.
  await vi.advanceTimersByTimeAsync(UNCONFIRMED_RECHECK_MS);
  const inFlight = await peer.request(META, 1);
  reader.refresh();

  peer.fail(inFlight, "metadata unavailable");
  // The post-press request is due as soon as the slot frees. One live interval
  // is enough to reach it — the recheck backoff is fifteen seconds away, so a
  // failure that backed off would still be waiting here.
  await vi.advanceTimersByTimeAsync(1000);
  expect(peer.requests(META).length).toBe(3);
  const fresh = await peer.request(META, 2);
  expect(fresh.method).toBe(META);
  peer.reply(fresh, metadata(true));
  await observed((state) => state.job?.terminal === true);
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
