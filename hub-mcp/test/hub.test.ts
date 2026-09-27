import assert from "node:assert/strict";
import { test } from "node:test";

import type { AppwireClient, ConnectionState, MethodName, MethodTypes } from "@evener/appwire-client";

import { AppwireHub } from "../src/hub.js";

/**
 * A stub AppwireClient for driving AppwireHub's connection machinery without
 * a socket: it never connects, records handlers, and lets the test push
 * connection states and ready events through the same seams the real client
 * uses.
 */
function stubClient() {
  const stateCbs = new Set<(s: ConnectionState) => void>();
  const readyCbs = new Set<() => void>();
  let closes = 0;
  return {
    connect: () => Promise.reject(new Error("stub never connects")),
    request: <M extends MethodName>(_method: M, _params: MethodTypes[M]["params"]) =>
      Promise.reject(new Error("stub never serves requests")),
    onNotification: () => () => {},
    onReady: (cb: () => void) => {
      readyCbs.add(cb);
      return () => readyCbs.delete(cb);
    },
    onStateChange: (cb: (s: ConnectionState) => void) => {
      stateCbs.add(cb);
      return () => stateCbs.delete(cb);
    },
    close: () => {
      closes += 1;
    },
    fireState(s: ConnectionState) {
      for (const cb of stateCbs) cb(s);
    },
    fireReady() {
      for (const cb of readyCbs) cb();
    },
    closeCount() {
      return closes;
    },
  };
}

function stubHub() {
  const stub = stubClient();
  const hub = new AppwireHub(
    { url: "ws://stub/rpc", token: "t", tokenSource: "test" },
    stub as unknown as AppwireClient,
  );
  return { stub, hub };
}

test("close clears a pending waitForReady timer so shutdown cannot linger", async () => {
  const { stub, hub } = stubHub();
  stub.fireState("reconnecting");

  const pending = hub.request("ping", {});
  hub.close();
  const outcome = await Promise.race([
    pending.then(
      () => "resolved",
      (err: unknown) => `rejected: ${err instanceof Error ? err.message : String(err)}`,
    ),
    new Promise<string>((resolve) => setTimeout(() => resolve("still pending a second after close"), 1000)),
  ]);
  assert.match(
    outcome,
    /^rejected:/,
    "close must settle the pending wait promptly, not leave it hanging on its 10s timer",
  );
  assert.match(outcome, /connection state: closed/);
  assert.equal(stub.closeCount(), 1);
});

test("a ready-wait that reconnects resolves, and close with nothing pending is quiet", async () => {
  const { stub, hub } = stubHub();
  stub.fireState("reconnecting");
  const pending = hub.request("ping", {});
  stub.fireReady();
  const outcome = await pending.then(
    () => "resolved",
    (err: unknown) => `rejected: ${err instanceof Error ? err.message : String(err)}`,
  );
  assert.match(
    outcome,
    /stub never serves requests/,
    "the ready-wait resolved and the call reached the client — not the 10s timeout",
  );
  hub.close();
  assert.equal(stub.closeCount(), 1);
});
