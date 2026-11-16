// The reconnect leg's branch decision, driven without a browser. The browser
// journey cannot make the navigation store take its reload branch on demand:
// a real hub advances its sequence only when a mutation commits, and the
// fixture owns no such command (cascade_browser_test.go is deliberately
// untouched by this fix). What the journey CAN observe is the two handshakes,
// so the decision reads them and these cases pin what it answers.
//
// Each rejection is identified by the assertion's own structured fields
// (`actual`, `expected`, `operator`) rather than by its message, so rewording a
// failure keeps every case meaningful (docs/developing-evener/testing.md, "Copy
// Is Not a Test Oracle").
import assert from "node:assert/strict";
import test from "node:test";
import {
  lostTierRead,
  readyDispatchTierReads,
  reconnectReadyDecision,
  reconnectSequences,
  rowUsable,
  socketInvalidations,
} from "./cascadeguard/attention.mjs";

// The handshake the loaded tier belongs to. A reconnect reports a handshake of
// its own, so every case below gives the reconnect a distinct handshake id.
const consumed = { handshakeId: "handshake-a", generationId: "generation-a", sequence: 7 };
const reconnectedAt = (over) => ({ ...consumed, handshakeId: "handshake-b", ...over });
const tierRead = (offset, id = "request-1") => ({ offset, label: `offset ${offset} (${id})` });

// The fields the assertion that rejected the call carried. `assert.throws`
// needs a predicate, so this is the predicate every rejection case shares.
const rejectedWith = (actual, expected, operator) => (error) => {
  assert.equal(error.code, "ERR_ASSERTION");
  assert.equal(error.operator, operator);
  assert.deepEqual(error.actual, actual);
  assert.deepEqual(error.expected, expected);
  return true;
};

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
    rejectedWith(["offset 0 (request-1)"], [], "deepStrictEqual"),
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
    rejectedWith(["offset 0 (request-1)"], [], "deepStrictEqual"),
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
    rejectedWith("generation-b", "generation-a", "strictEqual"),
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
    (error) => {
      assert.equal(error.code, "ERR_ASSERTION");
      assert.deepEqual([error.actual, error.expected], [false, true]);
      return true;
    },
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
    rejectedWith("handshake-a", "handshake-a", "notStrictEqual"),
  );
});

test("a reconnect with no observed handshake cannot claim retained", () => {
  assert.throws(
    () => reconnectReadyDecision({ consumed, reconnected: null, deliveredTierReads: [] }),
    rejectedWith(null, true, "=="),
  );
});

// A reconnect advertises the sequence its initialize response carries, and that
// is the number the store compares (store.ts:899). An invalidation that targets
// another resource and lands while subscriptions recover advances the page
// afterwards; folding it into the advertised value would demand a needs-you
// re-read the store never performs.
test("the advertised sequence is the handshake's own, without later invalidations", () => {
  const atReady = reconnectSequences({ handshakeSequence: 7, invalidations: [8] });
  assert.equal(atReady.advertised, 7);
  assert.equal(
    reconnectReadyDecision({
      consumed: { ...consumed, sequence: 7 },
      reconnected: reconnectedAt({ sequence: atReady.advertised }),
      deliveredTierReads: [],
    }),
    "retained",
  );
});

// The page's own side is the opposite: everything it received before the cut
// advanced what it had consumed.
test("the consumed sequence takes every invalidation the page received", () => {
  assert.equal(reconnectSequences({ handshakeSequence: 7, invalidations: [8, 9] }).consumed, 9);
  assert.equal(reconnectSequences({ handshakeSequence: 7, invalidations: [] }).consumed, 7);
});

// A row whose refresh failed keeps its place but refuses activation, so a hit
// test on its own would call a tier usable that offers no usable choice.
test("a disabled row is not a usable choice even when it is hit-tested", () => {
  assert.equal(rowUsable({ exists: true, ariaDisabled: "true", hit: true }), false);
  assert.equal(rowUsable({ exists: true, ariaDisabled: null, hit: true }), true);
  assert.equal(rowUsable({ exists: true, ariaDisabled: null, hit: false }), false);
  assert.equal(rowUsable({ exists: false, ariaDisabled: null, hit: false }), false);
});

// The reviewer's first timing: both handshakes advertise 7, and a sequence-8
// invalidation naming needs_you arrives during subscription recovery - the seen
// marker an opened target's own mutation publishes, which is asynchronous. The
// page reads the tier because of that invalidation, which is correct invalidation
// recovery, and the store retains rather than reloading. Charging that read to
// the ready dispatch would reject a healthy reconnect.
test("a tier read caused by a later invalidation is not a ready-dispatch reload", () => {
  const invalidationRead = { offset: 0, label: "offset 0 (request-invalidation)", at: 12 };
  const readyOnly = readyDispatchTierReads({ reads: [invalidationRead], firstInvalidationAt: 11 });
  assert.deepEqual(readyOnly, []);
  // Attributed correctly, the equal-sequence reconnect takes the retained branch
  // even though the tier was read after the handshake.
  assert.equal(
    reconnectReadyDecision({
      consumed: { ...consumed, sequence: 7 },
      reconnected: reconnectedAt({ sequence: 7 }),
      deliveredTierReads: readyOnly,
    }),
    "retained",
  );
});

// A ready-dispatch read that precedes a later invalidation is still the ready
// dispatch's, so the partition keeps it and the assertion still fires.
test("a ready-dispatch read before the invalidation is still caught", () => {
  const readyRead = { offset: 50, label: "offset 50 (request-ready)", at: 10 };
  const laterRead = { offset: 0, label: "offset 0 (request-invalidation)", at: 12 };
  const readyOnly = readyDispatchTierReads({ reads: [readyRead, laterRead], firstInvalidationAt: 11 });
  assert.deepEqual(readyOnly.map((read) => read.label), [readyRead.label]);
  assert.throws(
    () =>
      reconnectReadyDecision({
        consumed: { ...consumed, sequence: 7 },
        reconnected: reconnectedAt({ sequence: 7 }),
        deliveredTierReads: readyOnly,
      }),
    rejectedWith(["offset 50 (request-ready)"], [], "deepStrictEqual"),
  );
});

// A ready-dispatch read is still caught: with no invalidation to blame, every
// read after the handshake belongs to the ready dispatch.
test("with no later invalidation every tier read is the ready dispatch's", () => {
  const reads = [{ offset: 0, label: "offset 0 (request-1)", at: 9 }];
  assert.deepEqual(readyDispatchTierReads({ reads, firstInvalidationAt: null }), reads);
  assert.throws(
    () =>
      reconnectReadyDecision({
        consumed: { ...consumed, sequence: 7 },
        reconnected: reconnectedAt({ sequence: 7 }),
        deliveredTierReads: reads,
      }),
    rejectedWith(["offset 0 (request-1)"], [], "deepStrictEqual"),
  );
});

// The reviewer's timing: the page takes a live-only invalidation while the old
// socket is still closing, so it has consumed 8 by the time the reconnect
// advertises 8. The store retains - equal sequences - and so must the decision.
// Reading the page's side from the closed connection's own frames is what makes
// that number 8; a reader that snapshotted 7 before starting the close reports
// a reload for a healthy reconnect.
test("an invalidation the page took while the close was in flight is consumed", () => {
  // Frames as the connection saw them: its handshake, then a live-only
  // invalidation that lands while the reader is still asking the page what it
  // consumed. `beforeAt` is the moment the reader noticed the cut, before that
  // invalidation arrived.
  const socketFrames = [
    { direction: "Network.webSocketFrameReceived", requestId: "socket-old", method: "initialize", id: 1, result: { navigation: { generationId: "generation-a", sequence: 7 } } },
    { direction: "Network.webSocketFrameReceived", requestId: "socket-old", method: "evener/navigation/invalidated", params: { generationId: "generation-a", sequence: 8 } },
  ];
  const snapshottedEarly = socketInvalidations({
    frames: socketFrames,
    socketId: "socket-old",
    afterAt: 0,
    generationId: "generation-a",
  });
  const consumedAfterTheClose = reconnectSequences({ handshakeSequence: 7, invalidations: snapshottedEarly });
  assert.equal(consumedAfterTheClose.consumed, 8);
  // With the page's side read from its own connection, the reconnect that
  // advertises 8 is the equal-sequence reconnect the store retains, and it is
  // taken as retained rather than as a reload.
  assert.equal(
    reconnectReadyDecision({
      consumed: { ...consumed, sequence: consumedAfterTheClose.consumed },
      reconnected: reconnectedAt({ sequence: 8 }),
      deliveredTierReads: [],
    }),
    "retained",
  );
  // The stale snapshot is what the fix removes: it stays at 7, so a decision
  // fed that number would have called the same reconnect a reload.
  assert.equal(reconnectSequences({ handshakeSequence: 7, invalidations: [] }).consumed, 7);
});

// Attribution is by connection, so another socket's invalidations never advance
// this page's consumed sequence - and a reconnect that really did move past it
// is still a reload.
test("another connection's invalidation is not this page's consumed sequence", () => {
  const socketFrames = [
    { direction: "Network.webSocketFrameReceived", requestId: "socket-old", method: "initialize", id: 1, result: { navigation: { generationId: "generation-a", sequence: 7 } } },
    { direction: "Network.webSocketFrameReceived", requestId: "socket-new", method: "evener/navigation/invalidated", params: { generationId: "generation-a", sequence: 9 } },
  ];
  assert.deepEqual(
    socketInvalidations({ frames: socketFrames, socketId: "socket-old", afterAt: 0, generationId: "generation-a" }),
    [],
  );
  // A reconnect advertising a sequence genuinely past the consumed one is a
  // reload, so the connection scoping does not blunt the reload branch.
  assert.equal(
    reconnectReadyDecision({
      consumed: { ...consumed, sequence: 7 },
      reconnected: reconnectedAt({ sequence: 9 }),
      deliveredTierReads: [],
    }),
    "reloaded",
  );
});

// The failing run's own shape: the closing socket sent needs-you reads at
// offsets 0 and 50 and the hub's deltas were never applied, because the close
// landed first and the client had already detached that socket's handlers. The
// page retries both offsets on the reconnect with the bases it still holds.
// Those retries are the store's own re-force of two stale entries, so the
// retained branch must bless exactly those offsets and no others.
const tierReadSent = ({ id, socket, offset, base }) => ({
  direction: "Network.webSocketFrameSent",
  requestId: socket,
  id,
  method: "evener/navigation/read",
  params: base
    ? { resource: "section", section: "needs_you", offset, limit: 50, base }
    : { resource: "section", section: "needs_you", offset, limit: 50 },
});
const baseAt = (revision) => ({ generationId: "generation-a", revision, etag: `"etag-${revision}"` });
// The hub's answer to a read, as CDP logs it on the connection that asked.
// Present for the old socket's reads below on purpose: the wire carries the
// reply whether or not the page could still accept it.
const tierReadReply = ({ id, socket }) => ({
  direction: "Network.webSocketFrameReceived",
  requestId: socket,
  id,
  result: { data: { entities: [], containers: [] }, status: "ok", representation: "delta" },
});
// The rule a9113483bf shipped: a tier read the closing socket sent and the
// wire never answered is the one considered lost.
const answered = (frames, request) =>
  frames.some(
    (frame) =>
      frame.direction === "Network.webSocketFrameReceived" &&
      frame.id === request.id &&
      Object.hasOwn(frame, "result") &&
      !frame.error,
  );
const answeredRuleOffsets = (frames, socketId) => [
  ...new Set(
    frames
      .filter(
        (frame) =>
          frame.direction === "Network.webSocketFrameSent" &&
          frame.method === "evener/navigation/read" &&
          frame.params?.resource === "section" &&
          frame.params?.section === "needs_you" &&
          frame.requestId === socketId &&
          !answered(frames, frame),
      )
      .map((frame) => frame.params?.offset ?? 0),
  ),
];

test("a read whose answer the close dropped is the offset the reconnect may retry", () => {
  const frames = [
    tierReadSent({ id: 1, socket: "socket-old", offset: 0, base: baseAt(46) }),
    tierReadSent({ id: 2, socket: "socket-old", offset: 50, base: baseAt(46) }),
    // The hub answered both on the wire, before the close landed. The page
    // still holds revision 46 for each entry, which is what says the answers
    // never reached it.
    tierReadReply({ id: 1, socket: "socket-old" }),
    tierReadReply({ id: 2, socket: "socket-old" }),
    // The reconnect retries both with the base the lost reads used, because
    // that is the revision the entries still hold.
    tierReadSent({ id: 3, socket: "socket-new", offset: 0, base: baseAt(46) }),
    tierReadSent({ id: 4, socket: "socket-new", offset: 50, base: baseAt(46) }),
  ];
  assert.deepEqual(
    lostTierRead({ frames, lostSocketId: "socket-old", reconnectSocketId: "socket-new" }),
    [0, 50],
  );
  // The wire cannot tell a delivered answer from a dropped one, so the rule
  // a9113483bf shipped saw nothing lost here and rejected both retries. That
  // is the failing run: two stale entries re-forced by the store, called a
  // reload.
  assert.deepEqual(answeredRuleOffsets(frames, "socket-old"), []);
  const reads = [
    { offset: 0, label: "offset 0 (3)" },
    { offset: 50, label: "offset 50 (4)" },
  ];
  assert.equal(
    reconnectReadyDecision({
      consumed: { ...consumed, sequence: 112 },
      reconnected: reconnectedAt({ sequence: 112 }),
      deliveredTierReads: reads,
      retryableOffsets: lostTierRead({ frames, lostSocketId: "socket-old", reconnectSocketId: "socket-new" }),
    }),
    "retained",
  );
});

test("a retry that moved its base was an answered read, not a lost one", () => {
  // Both reads were answered and applied before the cut, so the reconnect's
  // retry of offset 0 carries the revision that answer moved the entry to. The
  // entry is not stale, the store starts no such read, and a read that did
  // arrive anyway is not a retry any lost read licenses.
  const frames = [
    tierReadSent({ id: 1, socket: "socket-old", offset: 0, base: baseAt(46) }),
    tierReadSent({ id: 2, socket: "socket-old", offset: 50, base: baseAt(46) }),
    tierReadSent({ id: 3, socket: "socket-new", offset: 0, base: baseAt(47) }),
    tierReadSent({ id: 4, socket: "socket-new", offset: 50, base: baseAt(48) }),
  ];
  assert.deepEqual(
    lostTierRead({ frames, lostSocketId: "socket-old", reconnectSocketId: "socket-new" }),
    [],
  );
  assert.throws(
    () =>
      reconnectReadyDecision({
        consumed: { ...consumed, sequence: 112 },
        reconnected: reconnectedAt({ sequence: 112 }),
        deliveredTierReads: [{ offset: 0, label: "offset 0 (3)" }],
        retryableOffsets: lostTierRead({ frames, lostSocketId: "socket-old", reconnectSocketId: "socket-new" }),
      }),
    rejectedWith(["offset 0 (3)"], [], "deepStrictEqual"),
  );
});

test("only the last read a closing connection sent at an offset can be lost", () => {
  // A tier entry read twice on the same connection: the first was superseded,
  // so its base is not a base the page still holds. Matching on any earlier
  // send would license a retry the entry never earned.
  const frames = [
    tierReadSent({ id: 1, socket: "socket-old", offset: 0, base: baseAt(20) }),
    tierReadSent({ id: 2, socket: "socket-old", offset: 0, base: baseAt(46) }),
    tierReadSent({ id: 3, socket: "socket-new", offset: 0, base: baseAt(20) }),
  ];
  assert.deepEqual(
    lostTierRead({ frames, lostSocketId: "socket-old", reconnectSocketId: "socket-new" }),
    [],
  );
});
