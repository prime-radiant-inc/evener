// Pins the producer.fifo acknowledgement wait against the load-sensitive race
// that made TestBackgroundJobsBrowser intermittently fail with "producer did not
// acknowledge rollover" (issue #4632).
//
// The producer cannot publish its acknowledgement until the hub has drained the
// phase's payload, and the rollover phase drains roughly 10 MiB through the job
// output store's per-chunk durability -- measured at 20.3s on a loaded host
// while the 50 KiB grows took under 100ms. A wait with a fixed floor, or one
// sized only from the page's render reactions, therefore rejected a
// correct-but-slow producer. The tests below drive the REAL wait loop under
// vitest's fake clock with a scripted acknowledgement source, so both a
// slow-but-correct producer and a truly wedged one are deterministic and
// neither needs a browser or a hub.

import { afterEach, describe, expect, test, vi } from "vitest";

import { Driver } from "../skillguard/run.mjs";
import { ACK_FLOOR_MS, ACK_INGEST_BYTES_PER_MS, awaitProducerAcknowledgement } from "./acknowledgement.mjs";

afterEach(() => {
  vi.useRealTimers();
});

// A driver whose reaction budget has already observed `slowestMs`, standing in
// for the slow steps the loaded run completed before the rollover handshake.
function driverWithSlowest(slowestMs = 0) {
  const driver = new Driver({ url: "http://127.0.0.1/", artifactDir: "", controlPath: "", milestonePath: "" });
  if (slowestMs > 0) driver.reactions.observe(slowestMs);
  return driver;
}

// A readAck double that throws until the test publishes `value`, matching the
// helper's parsed-value contract (its default reader JSON-parses the file).
function lateAck() {
  const state = { value: undefined };
  return {
    readAck: () => {
      if (state.value === undefined) throw new Error("acknowledgement not written yet");
      return state.value;
    },
    publish: (value) => {
      state.value = value;
    },
  };
}

describe("the producer acknowledgement wait", () => {
  test("absorbs a correct-but-slow acknowledgement a slower step has already justified", async () => {
    vi.useFakeTimers();
    const driver = driverWithSlowest(9_000);
    const ack = lateAck();
    const pending = awaitProducerAcknowledgement({ driver, ackPath: "phase-21.json", command: "rollover", readAck: ack.readAck });
    const settled = expect(pending).resolves.toMatchObject({ command: "rollover", totalBytes: 12_583_521 });
    // The producer needs longer than the fixed floor; the run has demonstrated
    // 9s reactions, so the sized budget must still be waiting.
    await vi.advanceTimersByTimeAsync(ACK_FLOOR_MS + 5_000);
    ack.publish({ command: "rollover", totalBytes: 12_583_521 });
    await vi.advanceTimersByTimeAsync(500);
    await settled;
  });

  test("sizes the budget from the payload the hub still has to drain", async () => {
    vi.useFakeTimers();
    // No slow page reaction yet: only the payload can explain the rollover cost.
    const driver = driverWithSlowest();
    const payloadBytes = 10_080_016;
    const ack = lateAck();
    const pending = awaitProducerAcknowledgement({
      driver,
      ackPath: "phase-21.json",
      command: "rollover",
      readAck: ack.readAck,
      pendingBytes: () => payloadBytes,
    });
    const settled = expect(pending).resolves.toMatchObject({ command: "rollover", totalBytes: 14_832_650 });
    // The measured rollover drain was 20.3s, well past the fixed floor; the
    // payload-sized budget must still be waiting.
    await vi.advanceTimersByTimeAsync(ACK_FLOOR_MS + 6_000);
    ack.publish({ command: "rollover", totalBytes: 14_832_650 });
    await vi.advanceTimersByTimeAsync(500);
    await settled;
  });

  test("records its own latency as a reaction so a later acknowledgement is sized from it", async () => {
    vi.useFakeTimers();
    const driver = driverWithSlowest();
    const ack = lateAck();
    const pending = awaitProducerAcknowledgement({ driver, ackPath: "phase-02.json", command: "grow", readAck: ack.readAck });
    await vi.advanceTimersByTimeAsync(3_000);
    ack.publish({ command: "grow", totalBytes: 10 });
    await vi.advanceTimersByTimeAsync(500);
    await pending;
    expect(driver.reactions.slowestMs).toBeGreaterThanOrEqual(3_000);
  });

  test("still trips on a wedged producer, bounded by the sized ceiling", async () => {
    vi.useFakeTimers();
    const driver = driverWithSlowest(9_000);
    const pending = awaitProducerAcknowledgement({
      driver,
      ackPath: "phase-21.json",
      command: "rollover",
      readAck: () => {
        throw new Error("acknowledgement never written");
      },
    });
    const rejected = expect(pending).rejects.toBeInstanceOf(Error);
    await vi.advanceTimersByTimeAsync(60_000 + 1_000);
    await rejected;
  });

  test("trips a wedged producer at the payload-sized ceiling", async () => {
    vi.useFakeTimers();
    const driver = driverWithSlowest();
    const payloadBytes = 10_080_016;
    const ceilingMs = Math.ceil(payloadBytes / ACK_INGEST_BYTES_PER_MS);
    const pending = awaitProducerAcknowledgement({
      driver,
      ackPath: "phase-21.json",
      command: "rollover",
      readAck: () => {
        throw new Error("acknowledgement never written");
      },
      pendingBytes: () => payloadBytes,
    });
    const rejected = expect(pending).rejects.toBeInstanceOf(Error);
    await vi.advanceTimersByTimeAsync(ceilingMs + 1_000);
    await rejected;
  });

  test("rejects an acknowledgement naming a different command", async () => {
    vi.useFakeTimers();
    const driver = driverWithSlowest();
    const ack = lateAck();
    const pending = awaitProducerAcknowledgement({ driver, ackPath: "phase-21.json", command: "rollover", readAck: ack.readAck });
    const rejected = expect(pending).rejects.toBeInstanceOf(Error);
    ack.publish({ command: "grow", totalBytes: 1 });
    await vi.advanceTimersByTimeAsync(500);
    await rejected;
  });
});
