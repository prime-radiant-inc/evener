#!/usr/bin/env node
// Real tabs, real BroadcastChannel, real IndexedDB. Only AppWire is scripted.
import path from "node:path";
import { fileURLToPath } from "node:url";
import { isDeepStrictEqual } from "node:util";
import {
  assertGuardOrigin,
  closePage,
  connectPage,
  evaluate,
  navigateTo,
  openPage,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";
import { pollInPage, waitDeadline } from "./inPageWait.mjs";
import { SOURCE_DISCOVERED, SUPPRESSED } from "./predicates.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const CHANNEL = "evener.session-cache.v1";

function check(condition, message, observed) {
  if (!condition) throw new Error(`${message}; observed ${JSON.stringify(observed)}`);
}

// The snapshot read every wait issues in the page.
const snapshotExpr = (ref) => `window.sessionCacheGuard.snapshot(${JSON.stringify(ref)})`;

function tabAPI(page, label) {
  const call = (method, ...args) =>
    evaluate(page.send, `window.sessionCacheGuard.${method}(${args.map((arg) => JSON.stringify(arg)).join(",")})`);
  const checkSnapshot = (state) => {
    check(state.errors.length === 0, `tab ${label} page errors`, state);
    return state;
  };
  return {
    send: page.send,
    call,
    checkSnapshot,
    async snapshot(ref) {
      return checkSnapshot(await call("snapshot", ref));
    },
  };
}

async function resetTabs(a, b) {
  await a.call("reset");
  await b.call("reset");
  const cleared = await a.call("clearShared");
  check(cleared.committed, "sessioncacheguard reset did not commit", cleared);
}

async function siblingDeletion(a, b) {
  const ref = "local:sessioncacheguard-delete";
  const seeded = (await b.call("seed", ref)).outcome === "written";
  check(seeded, "B seed must commit", seeded ? null : await b.call("durable", ref));
  check((await a.call("durable", ref)).record?.ref === ref, "A must see B's shared durable seed", ref);
  // Baseline the SAME flush used by the refusal check, not just the seed adapter.
  await b.call("armLease", ref);
  const written = await b.call("fireWrite", ref);
  check(
    written.record?.name === "sessioncacheguard live",
    "B control write must replace the seed before deletion",
    written,
  );
  const idle = await b.snapshot(ref);
  check(idle.lifetime === null, "B must start without an open lease", idle);
  await a.call("delete", ref); // action-owned fence, never markCacheSessionsDeleted directly
  const healPoll = await pollInPage(b.send, snapshotExpr(ref), SUPPRESSED);
  const healed = b.checkSnapshot(healPoll.observed);
  if (!healPoll.ok) {
    throw waitDeadline("sibling deletion heal: B never gained suppression-only state", healed);
  }
  check(
    isDeepStrictEqual(healed.lifetime, { suppressed: true }) && !healed.deletedRefs.includes(ref),
    "B must suppress without joining the deleting tab's deletedRefs",
    healed,
  );
  const sender = await a.snapshot(ref);
  check(sender.deletedRefs.includes(ref), "A deletion action must fence deletedRefs", sender);
  const removed = await a.call("durable", ref);
  check(removed.record === null, "A deletion must remove the durable record", removed);
  // Row absence alone is NOT B healing evidence: A deleted those same rows.
  await b.call("armLease", ref);
  const refused = await b.call("fireWrite", ref);
  check(refused.record === null, "B resurrected a sibling-deleted record", refused);
}

async function clearEpoch(a, b) {
  const control = "local:sessioncacheguard-clear-control";
  const open = await b.call("armLease", control);
  check(
    Number.isInteger(open.lifetime?.leaseEpoch) && !open.lifetime.suppressed,
    "control needs an unsuppressed lease",
    open,
  );
  const written = await b.call("fireWrite", control);
  check(written.record?.ref === control, "without A's clear the B write must land", written);

  const ref = "local:sessioncacheguard-clear";
  const before = await b.call("armLease", ref);
  check(
    before.lifetime?.leaseEpoch === written.epoch && !before.lifetime.suppressed,
    "B must hold the old epoch",
    before,
  );
  const cleared = await a.call("clear");
  check(cleared.committed, "A clear must commit", cleared);
  // NO B cache read/write here. The held read has not published a model, so
  // no debounce can accidentally observe the new durable epoch and heal B.
  const suppressedPoll = await pollInPage(b.send, snapshotExpr(ref), SUPPRESSED);
  const suppressed = b.checkSnapshot(suppressedPoll.observed);
  if (!suppressedPoll.ok) {
    throw waitDeadline("clear-epoch suppression: B never suppressed before storage I/O", suppressed);
  }
  check(
    suppressed.lifetime.leaseEpoch === before.lifetime.leaseEpoch,
    "clear must suppress, not rebind B's lease",
    suppressed,
  );
  const refused = await b.call("fireWrite", ref);
  check(
    refused.record === null && refused.epoch > before.lifetime.leaseEpoch,
    "old-lease B write must be refused after clear",
    refused,
  );
}

// A CDP isolated world is the driver realm, not a third tab or a replacement
// channel factory. Its receiver is created AFTER the production receiver.
// Native BroadcastChannel queues same-agent destinations in creation order:
// receiving a tagged probe here is a delivery barrier, not a guessed sleep.
async function driverRealm(page) {
  const frame = await page.send("Page.getFrameTree");
  const world = await page.send("Page.createIsolatedWorld", {
    frameId: frame.result.frameTree.frame.id,
    worldName: "sessioncacheguard-driver",
  });
  const realmSend = (method, params) => page.send(method, { ...params, contextId: world.result.executionContextId });
  const run = (expression) => evaluate(realmSend, expression);
  await run(`globalThis.received = []; globalThis.observer = new BroadcastChannel(${JSON.stringify(CHANNEL)});
    observer.onmessage = event => { if (event.data.guardProbe) received.push(event.data.guardProbe); };`);
  return {
    // The delivery barrier as one bounded in-page evaluation: the same
    // received.includes predicate at the same 20ms cadence on the same 5s
    // deadline, rather than one CDP round trip per Node tick.
    waitSeen: (tag) => pollInPage(realmSend, `received.includes(${JSON.stringify(tag)})`, `(seen) => seen === true`),
    post: (envelope) =>
      run(`(() => { const sender = new BroadcastChannel(${JSON.stringify(CHANNEL)});
      sender.postMessage(${JSON.stringify(envelope)}); sender.close(); })()`),
    close: () => run("observer.close()"),
  };
}

async function channelFiltering(a, b, tabs) {
  const snapshotBoth = (ref) => Promise.all([a.snapshot(ref), b.snapshot(ref)]);
  // Learn each real sourceId from an actual action's native outgoing envelope.
  await a.call("delete", "local:sessioncacheguard-source-a");
  await b.call("delete", "local:sessioncacheguard-source-b");
  const [aSourcePoll, bSourcePoll] = await Promise.all([
    pollInPage(a.send, snapshotExpr("local:sessioncacheguard-source-b"), SOURCE_DISCOVERED),
    pollInPage(b.send, snapshotExpr("local:sessioncacheguard-source-a"), SOURCE_DISCOVERED),
  ]);
  const sources = [a.checkSnapshot(aSourcePoll.observed), b.checkSnapshot(bSourcePoll.observed)];
  if (!(aSourcePoll.ok && bSourcePoll.ok)) {
    throw waitDeadline("channel filtering: source discovery never arrived", sources);
  }
  check(sources[0].sourceId !== sources[1].sourceId, "real tabs must own distinct source IDs", sources);
  await resetTabs(a, b);
  const drivers = [];
  try {
    for (const { page } of tabs) drivers.push(await driverRealm(page));
    const stateOnly = (state) => ({
      deletedRefs: state.deletedRefs,
      lifetimes: state.lifetimes,
      clearInFlight: state.clearInFlight,
    });
    const ref = "local:sessioncacheguard-filter";
    const baseline = (await snapshotBoth(ref)).map(stateOnly);
    const wrong = {
      version: 2,
      sourceId: "sessioncacheguard-driver",
      kind: "deletion",
      refs: [ref],
      guardProbe: "wrong-version",
    };
    await drivers[0].post(wrong);
    const wrongPolls = await Promise.all(drivers.map((driver) => driver.waitSeen(wrong.guardProbe)));
    // The old accept tick read BOTH tabs after the delivery it accepted on, so
    // the invariant is sampled here, at that same point: the delivery barrier
    // is the accept, and this read is what proves the probe changed neither tab.
    const wrongStates = (await snapshotBoth(ref)).map(stateOnly);
    check(
      isDeepStrictEqual(wrongStates, baseline),
      "channel filtering: wrong-version envelope changed a tab",
      wrongStates,
    );
    if (!wrongPolls.every((poll) => poll.ok)) {
      throw waitDeadline(
        "channel filtering: wrong-version probe never reached both tabs",
        wrongPolls.map((poll) => poll.observed),
      );
    }
    // One post: A must ignore its OWN ID, B must accept that very same ID.
    await drivers[0].post({ ...wrong, version: 1, sourceId: sources[0].sourceId, guardProbe: "own-source" });
    const [aSeenPoll, bSeenPoll, bStatePoll] = await Promise.all([
      drivers[0].waitSeen("own-source"),
      drivers[1].waitSeen("own-source"),
      pollInPage(b.send, snapshotExpr(ref), SUPPRESSED),
    ]);
    const delivered = [aSeenPoll.observed, bSeenPoll.observed];
    const acceptedStateA = await a.snapshot(ref);
    check(
      isDeepStrictEqual(stateOnly(acceptedStateA), baseline[0]),
      "channel filtering: A accepted its own sourceId",
      acceptedStateA,
    );
    const accepted = { states: [acceptedStateA, b.checkSnapshot(bStatePoll.observed)], delivered };
    if (!(aSeenPoll.ok && bSeenPoll.ok && bStatePoll.ok)) {
      throw waitDeadline("channel filtering: B did not accept A's sourceId", accepted);
    }
    check(!accepted.states[1].deletedRefs.includes(ref), "B sibling acceptance must not set deletedRefs", accepted);
  } finally {
    await Promise.all(drivers.map((driver) => driver.close()));
  }
}

async function main() {
  const started = performance.now();
  const guard = await startBrowserGuard({ frontend: FRONTEND, profilePrefix: "sessioncacheguard-chrome-" });
  const tabs = [];
  const failures = [];
  let endpoint;
  try {
    const origin = `http://127.0.0.1:${guard.vitePort}`;
    await waitForHttp(`${origin}/sessioncacheguard.html`, "sessioncacheguard Vite", guard.getViteLaunchError);
    endpoint = await waitForBrowserReady(guard);
    for (const label of ["A", "B"]) {
      const target = await openPage(endpoint, "about:blank");
      const tab = { label, page: null, targetId: target.id };
      tabs.push(tab);
      tab.page = await connectPage(endpoint, tab.targetId);
      await navigateTo(tab.page, `${origin}/sessioncacheguard.html`, {
        bootExpression: "typeof window.sessionCacheGuard !== 'undefined'",
        bootLabel: `sessioncacheguard tab ${label}`,
      });
      await assertGuardOrigin(tab.page.send, `127.0.0.1:${guard.vitePort}`);
    }
    check(
      tabs[0].targetId !== tabs[1].targetId,
      "guard must drive two distinct real tabs",
      tabs.map((tab) => tab.targetId),
    );
    const [a, b] = tabs.map(({ page, label }) => tabAPI(page, label));
    for (const [name, scenario] of [
      ["sibling deletion heal", siblingDeletion],
      ["clear-epoch suppression", clearEpoch],
      ["channel filtering", channelFiltering],
    ]) {
      await resetTabs(a, b);
      try {
        await scenario(a, b, tabs);
      } catch (error) {
        throw new Error(`${name}: ${error.message}`, { cause: error });
      }
      console.log(`sessioncacheguard ok: ${name}`);
    }
  } catch (error) {
    failures.push(error);
  } finally {
    for (const { page } of tabs) page?.close();
    const closed = await Promise.allSettled(tabs.map(({ targetId }) => closePage(endpoint, targetId)));
    const cleaned = await Promise.allSettled([guard.cleanup()]);
    for (const result of [...closed, ...cleaned]) if (result.status === "rejected") failures.push(result.reason);
  }
  if (failures.length > 0) throw new AggregateError(failures, failures.map((error) => error.message).join("\n"));
  console.log(
    `sessioncacheguard ok: two real tabs, native BroadcastChannel and IndexedDB (${((performance.now() - started) / 1000).toFixed(2)}s)`,
  );
}

main().catch((error) => {
  console.error(`sessioncacheguard FAIL: ${error.message}`);
  process.exitCode = 1;
});
