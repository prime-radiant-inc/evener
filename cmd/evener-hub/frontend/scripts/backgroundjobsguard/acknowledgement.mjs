// The producer.fifo handshake's acknowledgement wait.
//
// The producer (the Go fixture behind TestJobOutputPagingProducerProcess in
// job_output_paging_browser_test.go) writes each phase's payload to the job's
// stdout and only then publishes its phase acknowledgement file. That stdout is
// the real background job output, which the hub drains into the job's durable
// output store before the bytes are observable on the wire, so the
// acknowledgement cannot arrive until the hub has ingested the payload.
//
// The wait therefore has to tolerate the ingest time of the phase it is waiting
// on, and that time scales with the payload: the rollover phase writes roughly
// 10 MiB in a single write, and the store's per-chunk durability makes that
// drain cost roughly a second per half-megabyte on this host (measured: the
// 10 MiB rollover flush took 20.3s on a loaded box, while the 50 KiB grows
// drained in under 100ms). A fixed floor, or a budget scaled only from the
// page's render reactions, read that correct-but-slow producer as a missing
// acknowledgement on a loaded host (issue #4632).
//
// So the budget is the larger of two bounds, both of them tripwires that only
// the acknowledgement releases:
//   - the driver's demonstrated render slowness, the same rule every page wait
//     uses (skillguard/budgets.mjs), and
//   - the payload still to drain divided by ACK_INGEST_BYTES_PER_MS, a
//     conservative floor of the store's measured ingest rate.
// Polling re-reads the current ack file and the current payload every tick, so
// a producer that is emitting is never mistaken for a wedged one.
import { readFileSync } from "node:fs";

export const ACK_FLOOR_MS = 15_000;
export const ACK_POLL_MS = 100;
// A conservative floor of the hub's durable-output ingest rate. The measured
// rate on this host was ~0.5 KiB/ms (10 MiB in 20.3s); this floor is ~5x
// slower, so a large phase is given generous headroom while a small one still
// keeps the 15s floor.
export const ACK_INGEST_BYTES_PER_MS = 100;

const readAckFile = (ackPath) => JSON.parse(readFileSync(ackPath, "utf8"));

export async function awaitProducerAcknowledgement({
  driver,
  ackPath,
  command,
  readAck = readAckFile,
  pendingBytes = () => 0,
  pollMs = ACK_POLL_MS,
}) {
  const startedAt = Date.now();
  for (;;) {
    let value;
    try {
      value = readAck(ackPath);
    } catch {
      value = undefined;
    }
    if (value !== undefined) {
      if (value.command !== command) throw new Error(`wrong producer acknowledgement ${JSON.stringify(value)}`);
      driver.reactions.observe(Date.now() - startedAt);
      return value;
    }
    const drainBudgetMs = Math.ceil(pendingBytes() / ACK_INGEST_BYTES_PER_MS);
    const deadlineMs = Math.max(driver.reactions.deadline(ACK_FLOOR_MS), drainBudgetMs);
    if (Date.now() - startedAt > deadlineMs) throw new Error(`producer did not acknowledge ${command}`);
    await new Promise((resolve) => setTimeout(resolve, pollMs));
  }
}
