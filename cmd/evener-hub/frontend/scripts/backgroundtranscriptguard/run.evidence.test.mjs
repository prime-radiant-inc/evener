// Evidence tests for backgroundtranscriptguard — second browser-baseline review
// findings 8 (trace correlation: malformed/complete input, byte-window boundary)
// and 7 (resource sampling: measurement validation, budget bounds, and the
// gate-controlled producer sampling lifetime).
//
// They import the driver's exported seam directly (run.mjs guards its entry
// point with `import.meta.url === …`, so importing neither launches Chrome nor
// exits). No driver logic is duplicated.
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterAll, describe, expect, test } from "vitest";
import {
  BUDGET_LIMITS,
  budgetFailures,
  collectProductionPeak,
  countTargetRequestsInWindow,
  EVIDENCE_EXPR,
  narrowViewportContentPreserved,
  readTraceFrames,
  reduceProducedPeak,
  samplePeaksOnce,
  targetPagingEvidence,
  traceBytes,
} from "./run.mjs";

const dir = mkdtempSync(path.join(tmpdir(), "bgtrace-evidence-"));
afterAll(() => rmSync(dir, { recursive: true, force: true }));

const B2H = "browser_to_hub";
const H2B = "hub_to_browser";
const ZERO = {
  residentBytes: 0,
  pendingBytes: 0,
  workerBytes: 0,
  networkHydrations: 0,
  committedWrites: 0,
  producedBytes: 0,
};
const rec = ({ connection = "c1", direction, event = "frame", frame }) =>
  JSON.stringify({ event, connection, direction, frame: JSON.stringify(frame) });

function fixture(name, text) {
  const p = path.join(dir, name);
  writeFileSync(p, text);
  return p;
}
const joinTraced = (lines) => `${lines.join("\n")}\n`;
const bytesOf = (p) => readFileSync(p);

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}
const delay = (ms) => new Promise((r) => setTimeout(r, ms));
async function pollUntil(predicate, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return true;
    await delay(2);
  }
  return predicate();
}

// A page-global evidence stub exposing the same accessors EVIDENCE_EXPR uses.
const evidenceStub = ({ ref = "local:t", model = () => ({ turns: [] }) } = {}) => ({
  snapshot: () => ({
    refs: new Map([[ref, { resident: true, residentBytes: 10, handoffBytes: 0, current: true, appliedSequence: 1 }]]),
    residentBytes: 10,
    pendingBytes: 0,
    workerBytes: 0,
    networkHydrations: 0,
    committedWrites: 1,
  }),
  model,
});
function runEvidenceExpr(evidence) {
  const window = { backgroundTranscriptEvidence: evidence };
  return new Function("window", `return (${EVIDENCE_EXPR});`)(window);
}

describe("review finding 8 — trace correlation must not mask invalid input", () => {
  test("valid control: pairing and counts on a well-formed trace", () => {
    const p = fixture(
      "valid.jsonl",
      joinTraced([
        rec({ direction: B2H, frame: { id: 1, method: "thread/read", params: { ref: "local:t", subscribe: true } } }),
        rec({ direction: H2B, frame: { id: 1, result: { olderCursor: "cur-1" } } }),
        rec({ direction: B2H, frame: { id: 2, method: "thread/turns/list", params: { ref: "local:t" } } }),
        rec({ direction: H2B, frame: { id: 2, result: { nextCursor: "p2" } } }),
      ]),
    );
    expect(readTraceFrames(p)).toHaveLength(4);
    // The read is a foreground subscription; the count also classifies it so a
    // regression that turned recovery into a subscription still fails.
    expect(countTargetRequestsInWindow("local:t", 0, bytesOf(p).length, p)).toEqual({
      reads: 1,
      pages: 1,
      subscribeReads: 1,
      recoveryReads: 0,
      plainReads: 0,
    });
  });

  test("classifies a held-snapshot recovery read apart from a subscription read", () => {
    const p = fixture(
      "recovery.jsonl",
      joinTraced([
        rec({ direction: B2H, frame: { id: 1, method: "thread/read", params: { ref: "local:t", heldSnapshot: "h" } } }),
        rec({ direction: H2B, frame: { id: 1, result: { olderCursor: "cur-1" } } }),
      ]),
    );
    expect(countTargetRequestsInWindow("local:t", 0, bytesOf(p).length, p)).toEqual({
      reads: 1,
      pages: 0,
      subscribeReads: 0,
      recoveryReads: 1,
      plainReads: 0,
    });
  });

  test("non-frame metadata records are ignored, not rejected", () => {
    const p = fixture(
      "metadata.jsonl",
      joinTraced([
        JSON.stringify({ event: "open", connection: "c1", at: 1 }),
        rec({ direction: B2H, frame: { id: 1, method: "thread/read", params: { ref: "local:t" } } }),
        rec({ direction: H2B, frame: { id: 1, result: { olderCursor: "c" } } }),
        JSON.stringify({ event: "close", connection: "c1", at: 2 }),
      ]),
    );
    expect(readTraceFrames(p)).toHaveLength(2);
    // No subscribe and no held snapshot: a plain non-subscribing read.
    expect(countTargetRequestsInWindow("local:t", 0, bytesOf(p).length, p)).toEqual({
      reads: 1,
      pages: 0,
      subscribeReads: 0,
      recoveryReads: 0,
      plainReads: 1,
    });
  });

  test("a newline-terminated malformed OUTER record fails, not skipped", () => {
    const p = fixture(
      "malformed-outer.jsonl",
      joinTraced([
        rec({ direction: B2H, frame: { id: 1, method: "thread/read", params: { ref: "local:t" } } }),
        "{ this is not json",
        rec({ direction: H2B, frame: { id: 1, result: { olderCursor: "c" } } }),
      ]),
    );
    expect(() => readTraceFrames(p)).toThrow();
    expect(() => countTargetRequestsInWindow("local:t", 0, bytesOf(p).length, p)).toThrow();
  });

  test("a newline-terminated malformed INNER frame fails, not skipped", () => {
    const p = fixture(
      "malformed-frame.jsonl",
      joinTraced([
        rec({ direction: B2H, frame: { id: 1, method: "thread/read", params: { ref: "local:t" } } }),
        JSON.stringify({ event: "frame", connection: "c1", direction: H2B, frame: "{ not a frame" }),
      ]),
    );
    expect(() => readTraceFrames(p)).toThrow();
    expect(() => countTargetRequestsInWindow("local:t", 0, bytesOf(p).length, p)).toThrow();
  });

  test("a missing/unreadable trace file fails, not read as empty or zero", () => {
    const missing = path.join(dir, "does-not-exist.jsonl");
    expect(() => readTraceFrames(missing)).toThrow();
    expect(() => countTargetRequestsInWindow("local:t", 0, Number.MAX_SAFE_INTEGER, missing)).toThrow();
  });

  test("count-window fails independently on a malformed complete record", () => {
    const p = fixture(
      "count-malformed.jsonl",
      joinTraced([
        rec({ direction: B2H, frame: { id: 1, method: "thread/read", params: { ref: "local:t" } } }),
        "{ broken",
      ]),
    );
    expect(() => countTargetRequestsInWindow("local:t", 0, bytesOf(p).length, p)).toThrow();
  });

  test("an incomplete unterminated trailing line is deferred, preceding frames kept", () => {
    const text =
      joinTraced([
        rec({ direction: B2H, frame: { id: 1, method: "thread/read", params: { ref: "local:t" } } }),
        rec({ direction: H2B, frame: { id: 1, result: { olderCursor: "c" } } }),
      ]) + JSON.stringify({ event: "frame", connection: "c1", direction: H2B, frame: '{"id":2,"resu' });
    const p = fixture("incomplete-trailing.jsonl", text);
    expect(readTraceFrames(p)).toHaveLength(2);
  });

  test("a byte window ending mid-record consumes the complete boundary (no dropped tail request)", () => {
    const request = rec({ direction: B2H, frame: { id: 1, method: "thread/read", params: { ref: "local:t" } } });
    const trailing = JSON.stringify({ event: "frame", connection: "c1", direction: H2B, frame: '{"id":2,"resu' });
    const p = fixture("end-partial.jsonl", `${request}\n${trailing}`);
    const after = Math.floor(request.length / 2); // inside the request record
    expect(countTargetRequestsInWindow("local:t", 0, after, p)).toEqual({
      reads: 1,
      pages: 0,
      subscribeReads: 0,
      recoveryReads: 0,
      plainReads: 1,
    });
  });

  test("a byte window starting mid-record ignores the leading fragment, keeps complete records", () => {
    const a = rec({ direction: B2H, frame: { id: 1, method: "thread/read", params: { ref: "local:t" } } });
    const b = rec({ direction: B2H, frame: { id: 2, method: "thread/read", params: { ref: "local:t" } } });
    const c = rec({ direction: B2H, frame: { id: 3, method: "thread/read", params: { ref: "local:t" } } });
    const text = joinTraced([a, b, c]);
    const p = fixture("start-partial.jsonl", text);
    const before = a.length + 1 + Math.floor(b.length / 2); // inside b
    expect(countTargetRequestsInWindow("local:t", before, bytesOf(p).length, p)).toEqual({
      reads: 1,
      pages: 0,
      subscribeReads: 0,
      recoveryReads: 0,
      plainReads: 1,
    });
  });

  test("multibyte UTF-8: byte-offset windows count the record, not string indices", () => {
    const multibyte = rec({
      direction: H2B,
      frame: { id: 1, result: { text: "日本語".repeat(100) } },
    });
    const target = rec({ direction: B2H, frame: { id: 2, method: "thread/read", params: { ref: "local:t" } } });
    const p = fixture("multibyte.jsonl", joinTraced([multibyte, target]));
    const before = Buffer.byteLength(`${multibyte}\n`, "utf8"); // byte offset of the target request
    expect(countTargetRequestsInWindow("local:t", before, bytesOf(p).length, p)).toEqual({
      reads: 1,
      pages: 0,
      subscribeReads: 0,
      recoveryReads: 0,
      plainReads: 1,
    });
  });

  test("interleaved two connections: pairing is connection+id scoped, opposing-direction, sequential", () => {
    const p = fixture(
      "two-connections.jsonl",
      joinTraced([
        rec({ connection: "c1", direction: B2H, frame: { id: 1, method: "thread/read", params: { ref: "local:t" } } }),
        rec({ connection: "c2", direction: B2H, frame: { id: 1, method: "thread/read", params: { ref: "local:o" } } }),
        rec({ connection: "c1", direction: H2B, frame: { id: 1, result: { olderCursor: "A" } } }),
        rec({ connection: "c2", direction: H2B, frame: { id: 1, result: { olderCursor: "B" } } }),
        rec({ connection: "c1", direction: H2B, frame: { id: 1, result: { nextCursor: "Q" } } }),
        rec({
          connection: "c1",
          direction: B2H,
          frame: { id: 2, method: "thread/turns/list", params: { ref: "local:t" } },
        }),
        rec({ connection: "c1", direction: H2B, frame: { id: 2, result: { nextCursor: "P" } } }),
        rec({ connection: "c1", direction: B2H, frame: { id: 3, method: "thread/read", params: { ref: "local:t" } } }),
        rec({ connection: "c1", direction: B2H, frame: { id: 3, result: { olderCursor: "Z" } } }),
      ]),
    );
    const paging = targetPagingEvidence("local:t", p);
    expect(paging.readResponses).toBe(1);
    expect(paging.initialOlderCursor).toBe("A");
    expect(paging.pages).toBe(1);
    expect(paging.lastCursor).toBe("P");
    expect(countTargetRequestsInWindow("local:t", 0, bytesOf(p).length, p)).toEqual({
      reads: 2,
      pages: 1,
      subscribeReads: 0,
      recoveryReads: 0,
      plainReads: 2,
    });
  });

  test("a fixed [before, after) window does not count a target request appended after after", () => {
    // after is captured exactly at a record boundary (the line ends with '\n'),
    // then a target thread/read is appended. The fixed window must stay zero: the
    // consumed boundary may only complete a record straddling `after`, never a
    // record that starts at or after it.
    const first = rec({ direction: H2B, frame: { id: 1, result: {} } });
    const p = fixture("boundary-then-append.jsonl", `${first}\n`);
    const after = bytesOf(p).length;
    writeFileSync(
      p,
      `${first}\n${rec({ direction: B2H, frame: { id: 2, method: "thread/read", params: { ref: "local:t" } } })}\n`,
    );
    expect(countTargetRequestsInWindow("local:t", 0, after, p)).toEqual({
      reads: 0,
      pages: 0,
      subscribeReads: 0,
      recoveryReads: 0,
      plainReads: 0,
    });
  });

  test("a fixed window whose end record is unterminated must not return a silent no-read verdict", () => {
    // SYNTHETIC partial robustness, not ordinary browser reachability: the Go
    // trace writer (internal/appserver/websocket_trace.go:96-111) appends '\n' and
    // writes the whole line under one mutex, and reports a short write. Still, a
    // fixed window that ends in an unterminated record must not conclude a
    // definitive no-read while reconciliation is pending.
    const p = fixture(
      "unterminated-end.jsonl",
      rec({ direction: B2H, frame: { id: 1, method: "thread/read", params: { ref: "local:t" } } }),
    );
    expect(() => countTargetRequestsInWindow("local:t", 0, bytesOf(p).length, p)).toThrow();
  });

  test("an empty [before, before) window counts nothing", () => {
    const a = rec({ direction: H2B, frame: { id: 1, result: {} } });
    const target = rec({ direction: B2H, frame: { id: 2, method: "thread/read", params: { ref: "local:t" } } });
    const p = fixture("empty-window.jsonl", joinTraced([a, target]));
    const boundary = Buffer.byteLength(`${a}\n`, "utf8"); // a zero-width window at a record boundary
    expect(countTargetRequestsInWindow("local:t", boundary, boundary, p)).toEqual({
      reads: 0,
      pages: 0,
      subscribeReads: 0,
      recoveryReads: 0,
      plainReads: 0,
    });
  });

  test("a completed window's verdict is unchanged by a later append (recheck the same window)", () => {
    const a = rec({ direction: B2H, frame: { id: 1, method: "thread/read", params: { ref: "local:t" } } });
    const p = fixture("recheck.jsonl", joinTraced([a]));
    const after = bytesOf(p).length;
    expect(countTargetRequestsInWindow("local:t", 0, after, p)).toEqual({
      reads: 1,
      pages: 0,
      subscribeReads: 0,
      recoveryReads: 0,
      plainReads: 1,
    });
    writeFileSync(
      p,
      joinTraced([a, rec({ direction: B2H, frame: { id: 2, method: "thread/read", params: { ref: "local:t" } } })]),
    );
    expect(countTargetRequestsInWindow("local:t", 0, after, p)).toEqual({
      reads: 1,
      pages: 0,
      subscribeReads: 0,
      recoveryReads: 0,
      plainReads: 1,
    });
  });

  test("traceBytes fails rather than reporting zero when the trace cannot be statted", () => {
    expect(() => traceBytes(path.join(dir, "no-such-trace.jsonl"))).toThrow();
  });
});

describe("review finding 7 — resource measurements and budget bounds", () => {
  test("the real EVIDENCE_EXPR turns a model-read error into {error}", async () => {
    const evidence = evidenceStub({
      model: () => {
        throw new Error("model read failed");
      },
    });
    const result = await runEvidenceExpr(evidence);
    expect(typeof result.producedBytes.error).toBe("string");
  });

  test("reduceProducedPeak rejects error/NaN/negative/infinity/missing, accepts a valid zero", () => {
    expect(() => reduceProducedPeak({ "local:t": { error: "boom" } })).toThrow();
    expect(() => reduceProducedPeak({ "local:t": "NaN" })).toThrow();
    expect(() => reduceProducedPeak({ "local:t": Number.NaN })).toThrow();
    expect(() => reduceProducedPeak({ "local:t": -1 })).toThrow();
    expect(() => reduceProducedPeak({ "local:t": Number.POSITIVE_INFINITY })).toThrow();
    expect(() => reduceProducedPeak(undefined)).toThrow();
    expect(() => reduceProducedPeak(null)).toThrow();
    expect(reduceProducedPeak({ "local:t": 0 })).toBe(0);
    expect(reduceProducedPeak({})).toBe(0);
    expect(reduceProducedPeak({ a: 5, b: 9 })).toBe(9);
  });

  test("samplePeaksOnce rejects a bad measurement and accepts a valid zero", async () => {
    const withEvidence = (producedBytes) => async () => ({
      residentBytes: 0,
      pendingBytes: 0,
      workerBytes: 0,
      networkHydrations: 0,
      committedWrites: 0,
      producedBytes,
    });
    await expect(samplePeaksOnce({ send: () => {} }, withEvidence({ "local:t": { error: "boom" } }))).rejects.toThrow();
    await expect(samplePeaksOnce({ send: () => {} }, withEvidence({ "local:t": Number.NaN }))).rejects.toThrow();
    await expect(samplePeaksOnce({ send: () => {} }, withEvidence({ "local:t": -5 }))).rejects.toThrow();
    await expect(samplePeaksOnce({ send: () => {} }, withEvidence(undefined))).rejects.toThrow();
    const zero = await samplePeaksOnce({ send: () => {} }, withEvidence({ "local:t": 0 }));
    expect(zero.producedBytes).toBe(0);
    const numeric = await samplePeaksOnce({ send: () => {} }, withEvidence({ "local:t": 123 }));
    expect(numeric.producedBytes).toBe(123);
  });

  test("samplePeaksOnce rejects invalid/missing/negative/nonfinite scalar measurements; a true zero is valid", async () => {
    const payload = (over = {}) => ({
      residentBytes: 0,
      pendingBytes: 0,
      workerBytes: 0,
      networkHydrations: 0,
      committedWrites: 0,
      producedBytes: { "local:t": 0 },
      ...over,
    });
    const sample = (over) => samplePeaksOnce({ send: () => {} }, async () => payload(over));
    // True-zero control: every measured scalar is a legitimate 0.
    expect(await sample({})).toEqual({
      residentBytes: 0,
      pendingBytes: 0,
      workerBytes: 0,
      networkHydrations: 0,
      committedWrites: 0,
      producedBytes: 0,
    });
    for (const field of ["residentBytes", "pendingBytes", "workerBytes", "networkHydrations", "committedWrites"]) {
      await expect(sample({ [field]: -1 })).rejects.toThrow();
      await expect(sample({ [field]: Number.NaN })).rejects.toThrow();
      await expect(sample({ [field]: Number.POSITIVE_INFINITY })).rejects.toThrow();
      await expect(sample({ [field]: undefined })).rejects.toThrow();
      await expect(sample({ [field]: null })).rejects.toThrow();
      await expect(sample({ [field]: "12" })).rejects.toThrow();
    }
  });

  test("BUDGET_LIMITS are true to spec: resident/pending/one-worker-32MiB/hydrations", () => {
    expect(BUDGET_LIMITS).toEqual({
      residentBytes: 65536,
      pendingBytes: 32768,
      workerBytes: 32 * 1024 * 1024,
      hydrations: 2,
    });
  });

  test("budgetFailures enforces each bound independently; worker is not compared to pending", () => {
    const within = { residentBytes: 100, pendingBytes: 100, workerBytes: 1024 * 1024, networkHydrations: 1 };
    expect(budgetFailures(within)).toEqual([]);
    // The 1 MiB worker record is above the 32 KiB pending limit but within its own 32 MiB bound.
    expect(budgetFailures({ ...within, workerBytes: 1024 * 1024 })).toEqual([]);
    const resident = budgetFailures({ ...within, residentBytes: 65537 });
    expect(resident.some((f) => /resident/.test(f))).toBe(true);
    const pending = budgetFailures({ ...within, pendingBytes: 32769 });
    expect(pending.some((f) => /pending/.test(f))).toBe(true);
    const worker = budgetFailures({ ...within, workerBytes: 32 * 1024 * 1024 + 1 });
    expect(worker.some((f) => /worker/.test(f))).toBe(true);
    const hydration = budgetFailures({ ...within, networkHydrations: 3 });
    expect(hydration.some((f) => /hydration/.test(f))).toBe(true);
    expect(budgetFailures(within, { ...BUDGET_LIMITS, residentBytes: 50 }).some((f) => /resident/.test(f))).toBe(true);
  });
});

describe("review finding 7 — gate-controlled producer sampling lifetime", () => {
  test("the sampler starts before the first producer turn is triggered", async () => {
    let started = false;
    let sampledBeforeProducer = false;
    const gate = deferred();
    const promise = collectProductionPeak({
      turns: 1,
      sentinel: "S",
      startTurn: () => {
        started = true;
      },
      waitForComplete: () => gate.promise,
      sample: async () => {
        if (!started) sampledBeforeProducer = true;
        return ZERO;
      },
      intervalMs: 2,
    });
    await pollUntil(() => sampledBeforeProducer, 200);
    gate.resolve();
    await promise;
    expect(sampledBeforeProducer).toBe(true);
  });

  test("the sampler runs throughout the producer's pending wait", async () => {
    let pending = false;
    let samplesWhilePending = 0;
    const gate = deferred();
    const promise = collectProductionPeak({
      turns: 1,
      sentinel: "S",
      startTurn: () => {},
      waitForComplete: () => {
        pending = true;
        return gate.promise.then(() => {
          pending = false;
        });
      },
      sample: async () => {
        if (pending) samplesWhilePending += 1;
        return ZERO;
      },
      intervalMs: 2,
    });
    await pollUntil(() => samplesWhilePending >= 3, 300);
    gate.resolve();
    await promise;
    expect(samplesWhilePending).toBeGreaterThanOrEqual(3);
  });

  test("the sampler's peak is folded into the returned peak", async () => {
    let n = 0;
    const peak = await collectProductionPeak({
      turns: 2,
      sentinel: "S",
      startTurn: () => {},
      waitForComplete: async () => {},
      sample: async () => {
        n += 7;
        return { ...ZERO, producedBytes: n };
      },
      intervalMs: 2,
    });
    expect(peak.producedBytes).toBeGreaterThanOrEqual(7);
  });

  test("a sample error rejects the collection and leaves no unhandled rejection", async () => {
    const seen = [];
    const onUnhandled = (error) => seen.push(error);
    process.on("unhandledRejection", onUnhandled);
    try {
      await expect(
        collectProductionPeak({
          turns: 1,
          sentinel: "S",
          startTurn: () => {},
          waitForComplete: async () => {},
          sample: async () => {
            throw new Error("sample boom");
          },
          intervalMs: 2,
        }),
      ).rejects.toThrow("sample boom");
      await delay(30);
    } finally {
      process.off("unhandledRejection", onUnhandled);
    }
    expect(seen).toEqual([]);
  });

  test("a producer error stops and joins the sampler; no sample runs after return", async () => {
    let returned = false;
    let samplesAfterReturn = 0;
    await expect(
      collectProductionPeak({
        turns: 1,
        sentinel: "S",
        startTurn: () => {},
        waitForComplete: async () => {
          throw new Error("producer boom");
        },
        sample: async () => {
          if (returned) samplesAfterReturn += 1;
          return ZERO;
        },
        intervalMs: 2,
      }),
    ).rejects.toThrow("producer boom");
    returned = true;
    await delay(30);
    expect(samplesAfterReturn).toBe(0);
  });
});

describe("narrow-viewport repeat activation must preserve the transcript payload", () => {
  // The item shape readTargetDomain records: identity plus the body fields the
  // reopen oracle compares (text, output, arguments, status).
  const textItem = (id, text, over = {}) => ({
    turnId: "t1",
    id,
    type: "assistant",
    text,
    output: "",
    arguments: null,
    status: "done",
    ...over,
  });
  const toolItem = (id, output, over = {}) => ({
    turnId: "t1",
    id,
    type: "tool",
    text: "",
    output,
    arguments: '{"command":"ls"}',
    status: "done",
    ...over,
  });

  test("a body-loss rewrite with unchanged IDs is rejected", () => {
    // The sentinel is the third item; damage the OTHER text item and the tool
    // output, so the body path fails, not the separate sentinel.
    const before = {
      items: [
        textItem("a", "BACKGROUND_OTHER_TEXT"),
        toolItem("b", "BACKGROUND_TOOL_OUTPUT"),
        textItem("c", "BACKGROUND_GAP_TURN"),
      ],
    };
    // Same IDs and order, sentinel intact, but another item's text was emptied.
    // Identity-only preservation would report this as preserved.
    const lostText = {
      items: [textItem("a", ""), toolItem("b", "BACKGROUND_TOOL_OUTPUT"), textItem("c", "BACKGROUND_GAP_TURN")],
    };
    // Same IDs and order, sentinel intact, but the tool output emptied.
    const lostOutput = {
      items: [textItem("a", "BACKGROUND_OTHER_TEXT"), toolItem("b", ""), textItem("c", "BACKGROUND_GAP_TURN")],
    };
    expect(narrowViewportContentPreserved(before, lostText)).toBe(false);
    expect(narrowViewportContentPreserved(before, lostOutput)).toBe(false);
  });

  test("identical payloads are preserved; changed arguments, status, order or a dropped item are not", () => {
    const before = { items: [textItem("a", "hello"), toolItem("b", "OUT")] };
    expect(narrowViewportContentPreserved(before, before)).toBe(true);
    const changedArgs = {
      items: [textItem("a", "hello"), toolItem("b", "OUT", { arguments: '{"command":"pwd"}' })],
    };
    expect(narrowViewportContentPreserved(before, changedArgs)).toBe(false);
    const changedStatus = {
      items: [textItem("a", "hello", { status: "error" }), toolItem("b", "OUT")],
    };
    expect(narrowViewportContentPreserved(before, changedStatus)).toBe(false);
    const reordered = { items: [toolItem("b", "OUT"), textItem("a", "hello")] };
    expect(narrowViewportContentPreserved(before, reordered)).toBe(false);
    const dropped = { items: [toolItem("b", "OUT")] };
    expect(narrowViewportContentPreserved(before, dropped)).toBe(false);
  });
});
