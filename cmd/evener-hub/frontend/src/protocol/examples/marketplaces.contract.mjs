import assert from "node:assert/strict";
import test from "node:test";
import { ownedHub, scriptedHub, withMutationEnv } from "./management-contract-fixtures.mjs";
import { AcknowledgedReadbackError } from "./management-recovery.mjs";
import { runMarketplaces } from "./marketplaces-logic.mjs";

const list = "evener/marketplace/list";
const entry = { name: "fixture", source: { kind: "directory", path: "/owned" }, lastUpdated: 1 };
const catalog = { marketplaces: [entry] };
const empty = { marketplaces: [] };
const options = { action: "refresh", params: { name: "fixture" }, ownedHub };
const optedIn = (run) => withMutationEnv("EVENER_MARKETPLACE_MUTATION", run);

test("marketplace defaults to a connected read without mutation opt-in", async () => {
  const f = scriptedHub([() => catalog]);
  assert.deepEqual(await runMarketplaces(f.hub), { outcome: "read", readback: catalog });
  assert.deepEqual(f.calls, [{ method: "connect" }, { method: list, params: {} }]);
});
test("browse requests the registry alias and preserves the manifest catalog name", async () => {
  const response = { name: "manifest-name", plugins: [{ name: "tool" }] };
  const f = scriptedHub([() => response]);
  assert.deepEqual(await runMarketplaces(f.hub, { action: "browse", params: options.params }), {
    outcome: "read",
    readback: response,
  });
  assert.deepEqual(f.calls, [{ method: "connect" }, { method: "evener/marketplace/browse", params: options.params }]);
  for (const invalid of [
    { name: "", plugins: [] },
    { name: "fixture", plugins: [null] },
    { name: "fixture", plugins: [{ name: 1 }] },
  ]) {
    const bad = scriptedHub([() => invalid]);
    await assert.rejects(runMarketplaces(bad.hub, { action: "browse", params: options.params }));
  }
});
test("marketplace guards reject before connection", async () =>
  optedIn(async () => {
    const f = scriptedHub([]);
    delete process.env.EVENER_MARKETPLACE_MUTATION;
    await assert.rejects(runMarketplaces(f.hub, options));
    process.env.EVENER_MARKETPLACE_MUTATION = "1";
    for (const change of [
      { ownedHub: undefined },
      { ownedHub: "ws://other/rpc" },
      { action: "toString" },
      { action: "" },
      { params: null },
      { params: [] },
      { params: { name: "" } },
      { action: "list", params: { action: "add" } },
      { action: "add", params: { source: { kind: "github" } } },
      { action: "add", params: { source: { kind: "url", url: false } } },
      { action: "add", params: { source: { kind: "directory", path: "/owned", extra: "x" } } },
      { action: "add", params: { source: { kind: "git-subdir", url: "/owned" } } },
      { action: "add", params: { source: { kind: "url", url: "/owned", ref: null } } },
      { params: { name: "fixture", action: "remove" } },
    ])
      await assert.rejects(runMarketplaces(f.hub, { ...options, ...change }));
    assert.deepEqual(f.calls, []);
  }));
for (const source of [
  { kind: "github", repo: "owner/fixture" },
  { kind: "url", url: "/owned/git", ref: "", sha: "" },
  { kind: "directory", path: "/owned" },
  { kind: "git-subdir", url: "/owned/git", path: "plugins", ref: "main" },
])
  test(`marketplace add preserves ${source.kind} fields and omitted name`, async () =>
    optedIn(async () => {
      const f = scriptedHub([() => empty, () => catalog, () => catalog]);
      assert.equal(
        (await runMarketplaces(f.hub, { action: "add", params: { source }, ownedHub })).outcome,
        "acknowledged",
      );
      assert.deepEqual(f.calls, [
        { method: "connect" },
        { method: list, params: {} },
        { method: "evener/marketplace/add", params: { source } },
        { method: list, params: {} },
      ]);
    }));
for (const action of ["add", "remove", "refresh"])
  test(`marketplace ${action} uses exact authored target and fresh readback`, async () =>
    optedIn(async () => {
      const params = action === "add" ? { name: "fixture", source: entry.source } : { name: "fixture" };
      const after = action === "remove" ? empty : catalog;
      const f = scriptedHub([() => (action === "add" ? empty : catalog), () => after, () => after]);
      assert.deepEqual(await runMarketplaces(f.hub, { action, params, ownedHub }), {
        outcome: "acknowledged",
        readback: after,
      });
      assert.deepEqual(f.calls, [
        { method: "connect" },
        { method: list, params: {} },
        { method: `evener/marketplace/${action}`, params },
        { method: list, params: {} },
      ]);
    }));
test("marketplace preflight rejects duplicate explicit names and absent targets", async () =>
  optedIn(async () => {
    for (const change of [
      { action: "add", params: { name: "fixture", source: entry.source } },
      { action: "remove", params: { name: "different" } },
      { action: "refresh", params: { name: "different" } },
    ]) {
      const f = scriptedHub([() => catalog]);
      await assert.rejects(runMarketplaces(f.hub, { ...options, ...change }));
      assert.equal(f.calls.length, 2);
    }
    const f = scriptedHub([() => catalog, () => catalog, () => catalog]);
    await runMarketplaces(f.hub, { action: "add", params: { source: entry.source }, ownedHub });
    assert.equal(f.calls.filter((c) => c.method === "evener/marketplace/add").length, 1);
  }));
for (const cause of [new Error("private"), undefined, null])
  test(`marketplace thrown mutation stays uncertain: ${String(cause)}`, async () =>
    optedIn(async () => {
      const f = scriptedHub([
        () => catalog,
        () => {
          throw cause;
        },
        () => catalog,
      ]);
      assert.deepEqual(await runMarketplaces(f.hub, options), { outcome: "uncertain", readback: catalog });
      assert.deepEqual(
        f.calls.map((c) => c.method),
        ["connect", list, "evener/marketplace/refresh", list],
      );
    }));
test("marketplace malformed lists are rejected and malformed ACK remains uncertain", async () =>
  optedIn(async () => {
    for (const value of [{}, { marketplaces: null }, { marketplaces: [null] }, { marketplaces: [{ name: 1 }] }]) {
      const bad = scriptedHub([() => value]);
      await assert.rejects(runMarketplaces(bad.hub));
    }
    const f = scriptedHub([() => catalog, () => ({}), () => catalog]);
    assert.equal((await runMarketplaces(f.hub, options)).outcome, "uncertain");
  }));
test("marketplace read failures preserve the ACK error or both causes without replay", async () =>
  optedIn(async () => {
    const readError = new Error("private read");
    const ack = scriptedHub([
      () => catalog,
      () => catalog,
      () => {
        throw readError;
      },
    ]);
    await assert.rejects(
      runMarketplaces(ack.hub, options),
      (e) => e instanceof AcknowledgedReadbackError && e.cause === readError,
    );
    for (const cause of [new Error("private mutation"), undefined, null]) {
      const f = scriptedHub([
        () => catalog,
        () => {
          throw cause;
        },
        () => {
          throw readError;
        },
      ]);
      await assert.rejects(
        runMarketplaces(f.hub, options),
        (e) => e instanceof AggregateError && e.errors[0] === cause && e.errors[1] === readError,
      );
      assert.equal(f.calls.filter((c) => c.method === "evener/marketplace/refresh").length, 1);
    }
  }));
