#!/usr/bin/env node
// Real tabs, real BroadcastChannel, real IndexedDB. Only AppWire is scripted.
import path from "node:path";
import { setTimeout as poll } from "node:timers/promises";
import { fileURLToPath } from "node:url";
import {
  assertGuardOrigin,
  closePage,
  connectPageMatching,
  evaluate,
  navigateTo,
  openPage,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const CHANNEL = "evener.session-cache.v1";
const WAIT_MS = 5000;

function check(condition, message, observed) {
  if (!condition) throw new Error(`${message}; observed ${JSON.stringify(observed)}`);
}

async function waitFor(message, read, accepts) {
  const deadline = performance.now() + WAIT_MS;
  let observed;
  do {
    observed = await read();
    if (accepts(observed)) return observed;
    await poll(20); // condition polling, never a sleep used as delivery evidence
  } while (performance.now() < deadline);
  throw new Error(`${message} (${WAIT_MS}ms deadline); observed ${JSON.stringify(observed)}`);
}

function tabAPI(page, instance) {
  const call = (method, ...args) =>
    evaluate(page.send, `window.${instance}Guard.${method}(${args.map((arg) => JSON.stringify(arg)).join(",")})`);
  return {
    call,
    async snapshot(ref) {
      const state = await call("snapshot", ref);
      check(state.errors.length === 0, `tab ${instance} page errors`, state);
      return state;
    },
  };
}

async function siblingDeletion(a, b) {
  const ref = "local:sessioncacheguard-delete";
  check((await b.call("seed", ref)).outcome === "written", "B seed must commit", await b.call("durable", ref));
  check((await a.call("durable", ref)).record?.ref === ref, "A must see B's shared durable seed", ref);
  // Baseline the SAME flush used by the refusal check, not just the seed adapter.
  await b.call("armLease", ref);
  const written = await b.call("fireWrite", ref);
  check(
    written.record?.name === "sessioncacheguard live",
    "B control write must replace the seed before deletion",
    written,
  );
  check((await b.snapshot(ref)).lifetime === null, "B must start without an open lease", await b.snapshot(ref));
  await a.call("delete", ref); // action-owned fence, never markCacheSessionsDeleted directly
  const healed = await waitFor(
    "sibling deletion heal: B never gained suppression-only state",
    () => b.snapshot(ref),
    (state) => state.lifetime?.suppressed === true,
  );
  check(
    JSON.stringify(healed.lifetime) === '{"suppressed":true}' && !healed.deletedRefs.includes(ref),
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
  const suppressed = await waitFor(
    "clear-epoch suppression: B never suppressed before storage I/O",
    () => b.snapshot(ref),
    (state) => state.lifetime?.suppressed === true,
  );
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
  const run = (expression) =>
    evaluate(
      (method, params) => page.send(method, { ...params, contextId: world.result.executionContextId }),
      expression,
    );
  await run(`globalThis.received = []; globalThis.observer = new BroadcastChannel(${JSON.stringify(CHANNEL)});
    observer.onmessage = event => { if (event.data.guardProbe) received.push(event.data.guardProbe); };`);
  return {
    seen: (tag) => run(`received.includes(${JSON.stringify(tag)})`),
    post: (envelope) =>
      run(`(() => { const sender = new BroadcastChannel(${JSON.stringify(CHANNEL)});
      sender.postMessage(${JSON.stringify(envelope)}); sender.close(); })()`),
    close: () => run("observer.close()"),
  };
}

async function channelFiltering(a, b, pages) {
  // Learn each real sourceId from an actual action's native outgoing envelope.
  await a.call("delete", "local:sessioncacheguard-source-a");
  await b.call("delete", "local:sessioncacheguard-source-b");
  const sources = await waitFor(
    "channel filtering: source discovery never arrived",
    async () => [
      await a.snapshot("local:sessioncacheguard-source-b"),
      await b.snapshot("local:sessioncacheguard-source-a"),
    ],
    (states) =>
      states.every(
        (state) =>
          typeof state.sourceId === "string" && state.sourceId.length > 0 && state.lifetime?.suppressed === true,
      ),
  );
  check(sources[0].sourceId !== sources[1].sourceId, "real tabs must own distinct source IDs", sources);
  await a.call("reset");
  await b.call("reset");
  const drivers = [];
  try {
    for (const page of pages) drivers.push(await driverRealm(page));
    const stateOnly = (state) => ({
      deletedRefs: state.deletedRefs,
      lifetimes: state.lifetimes,
      clearInFlight: state.clearInFlight,
    });
    const ref = "local:sessioncacheguard-filter";
    const baseline = [stateOnly(await a.snapshot(ref)), stateOnly(await b.snapshot(ref))];
    const wrong = {
      version: 2,
      sourceId: "sessioncacheguard-driver",
      kind: "deletion",
      refs: [ref],
      guardProbe: "wrong-version",
    };
    await drivers[0].post(wrong);
    await waitFor(
      "channel filtering: wrong-version probe never reached both tabs",
      async () => {
        const delivered = await Promise.all(drivers.map((driver) => driver.seen(wrong.guardProbe)));
        const states = [stateOnly(await a.snapshot(ref)), stateOnly(await b.snapshot(ref))];
        check(
          JSON.stringify(states) === JSON.stringify(baseline),
          "channel filtering: wrong-version envelope changed a tab",
          states,
        );
        return delivered;
      },
      (seen) => seen.every(Boolean),
    );
    // One post: A must ignore its OWN ID, B must accept that very same ID.
    await drivers[0].post({ ...wrong, version: 1, sourceId: sources[0].sourceId, guardProbe: "own-source" });
    const accepted = await waitFor(
      "channel filtering: B did not accept A's sourceId",
      async () => {
        const delivered = await Promise.all(drivers.map((driver) => driver.seen("own-source")));
        const states = [await a.snapshot(ref), await b.snapshot(ref)];
        check(
          JSON.stringify(stateOnly(states[0])) === JSON.stringify(baseline[0]),
          "channel filtering: A accepted its own sourceId",
          states[0],
        );
        return { states, delivered };
      },
      (observed) => observed.delivered.every(Boolean) && observed.states[1].lifetime?.suppressed === true,
    );
    check(!accepted.states[1].deletedRefs.includes(ref), "B sibling acceptance must not set deletedRefs", accepted);
  } finally {
    await Promise.all(drivers.map((driver) => driver.close()));
  }
}

async function main() {
  const started = performance.now();
  const guard = await startBrowserGuard({ frontend: FRONTEND, profilePrefix: "sessioncacheguard-chrome-" });
  const pages = [];
  const targets = [];
  const failures = [];
  let endpoint;
  try {
    const origin = `http://127.0.0.1:${guard.vitePort}`;
    await waitForHttp(`${origin}/sessioncacheguard.html`, "sessioncacheguard Vite", guard.getViteLaunchError);
    endpoint = await waitForBrowserReady(guard);
    for (const instance of ["a", "b"]) {
      const target = await openPage(endpoint, "about:blank");
      targets.push(target.id);
      const page = await connectPageMatching(endpoint, (entry) => entry.id === target.id);
      pages.push(page);
      await navigateTo(page, `${origin}/sessioncacheguard.html?instance=${instance}`, {
        bootExpression: `typeof window.${instance}Guard !== 'undefined'`,
        bootLabel: `sessioncacheguard tab ${instance}`,
      });
      await assertGuardOrigin(page.send, `127.0.0.1:${guard.vitePort}`);
    }
    check(targets[0] !== targets[1], "guard must drive two distinct real tabs", targets);
    const [a, b] = pages.map((page, index) => tabAPI(page, index === 0 ? "a" : "b"));
    for (const [name, scenario] of [
      ["sibling deletion heal", siblingDeletion],
      ["clear-epoch suppression", clearEpoch],
      ["channel filtering", channelFiltering],
    ]) {
      await a.call("reset");
      await b.call("reset");
      try {
        await scenario(a, b, pages);
      } catch (error) {
        throw new Error(`${name}: ${error.message}`, { cause: error });
      }
      console.log(`sessioncacheguard ok: ${name}`);
    }
  } catch (error) {
    failures.push(error);
  } finally {
    for (const page of pages) page.close();
    const closed = await Promise.allSettled(targets.map((id) => closePage(endpoint, id)));
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
