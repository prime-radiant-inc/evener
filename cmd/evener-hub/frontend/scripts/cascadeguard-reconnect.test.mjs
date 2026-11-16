// The reconnect leg's branch decision, driven without a browser. The browser
// journey cannot make the navigation store take its reload branch on demand:
// a real hub advances its sequence only when a mutation commits, and the
// fixture owns no such command (cascade_browser_test.go is deliberately
// untouched by this fix). What the journey CAN observe is the two handshakes,
// so the decision reads them and these cases pin what it answers.
import assert from "node:assert/strict";
import test from "node:test";
import { reconnectReadyDecision } from "./cascadeguard/attention.mjs";

// The handshake the loaded tier belongs to. A reconnect reports a handshake of
// its own, so every case below gives the reconnect a distinct handshake id.
const consumed = { handshakeId: "handshake-a", generationId: "generation-a", sequence: 7 };
const reconnectedAt = (over) => ({ ...consumed, handshakeId: "handshake-b", ...over });
const tierRead = (offset, id = "request-1") => ({ offset, label: `offset ${offset} (${id})` });

test("an equal-sequence reconnect retains the loaded tier and starts no read", () => {
  assert.equal(
    reconnectReadyDecision({ consumed, reconnected: reconnectedAt({}), deliveredTierReads: [] }),
    "retained",
  );
});

// This is the failure the old leg could not see: a reconnect that reloads is
// correct behavior, and a guard that demanded a re-read on every reconnect
// failed whenever no change had committed. Both directions are pinned here so
// neither can be asserted away.
test("a reconnect past the consumed sequence reloads and is not called retained", () => {
  assert.equal(
    reconnectReadyDecision({
      consumed,
      reconnected: reconnectedAt({ sequence: 8 }),
      deliveredTierReads: [tierRead(0)],
    }),
    "reloaded",
  );
});

test("a retained reconnect that read the tier anyway is a failure", () => {
  assert.throws(
    () =>
      reconnectReadyDecision({
        consumed,
        reconnected: reconnectedAt({}),
        deliveredTierReads: [tierRead(0)],
      }),
    /re-reads no needs-you page/,
    "an equal-sequence reconnect must not issue a tier read",
  );
});

// The one read a retained reconnect may issue: the store re-forces an entry
// only when it is stale or errored, which is what a read the closing socket
// took down leaves behind.
test("a retained reconnect may retry an offset whose read the close lost", () => {
  assert.equal(
    reconnectReadyDecision({
      consumed,
      reconnected: reconnectedAt({}),
      deliveredTierReads: [tierRead(50)],
      retryableOffsets: [50],
    }),
    "retained",
  );
});

test("the retry allowance does not extend to an offset the close did not lose", () => {
  assert.throws(
    () =>
      reconnectReadyDecision({
        consumed,
        reconnected: reconnectedAt({}),
        deliveredTierReads: [tierRead(0)],
        retryableOffsets: [50],
      }),
    /re-reads no needs-you page/,
  );
});

test("a reconnect into a different navigation generation is a protocol error", () => {
  assert.throws(
    () =>
      reconnectReadyDecision({
        consumed,
        reconnected: reconnectedAt({ generationId: "generation-b" }),
        deliveredTierReads: [],
      }),
    /stays in the navigation generation/,
  );
});

test("a reconnect whose sequence moved backward is refused", () => {
  assert.throws(
    () =>
      reconnectReadyDecision({
        consumed,
        reconnected: reconnectedAt({ sequence: 6 }),
        deliveredTierReads: [],
      }),
    /never moves the navigation sequence backward/,
  );
});

// Reading the same handshake twice would make every other case pass without a
// reconnect ever having happened.
test("a reconnect still reporting the tier's own handshake is refused", () => {
  assert.throws(
    () =>
      reconnectReadyDecision({
        consumed,
        reconnected: { ...consumed },
        deliveredTierReads: [],
      }),
    /handshake of its own/,
  );
});

test("a reconnect with no observed handshake cannot claim retained", () => {
  assert.throws(
    () => reconnectReadyDecision({ consumed, reconnected: null, deliveredTierReads: [] }),
    /the reconnect reported a navigation handshake/,
  );
});
