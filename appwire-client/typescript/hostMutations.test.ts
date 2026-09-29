import { describe, expect, test } from "vitest";
import { WireError } from "./errors";
import {
  committedMutationRow,
  createHostMutations,
  ErrorStaleEntry,
  HOST_ENTRY_FIELD_ORDER,
  HOST_ENTRY_FIELD_TEXT,
  HOST_GATE_TIMEOUT_MS,
  type HostMutationPair,
  rootsFromText,
  rootsToText,
} from "./hostMutations";
import type { HostRow } from "./types.gen";

const row = (name: string, over: Partial<HostRow> = {}): HostRow => ({
  name,
  origin: "sidecar",
  attached: true,
  midAttach: false,
  removed: false,
  generation: 1,
  incarnationId: `${name}-1`,
  ...over,
});

const stale = () => new WireError("the entry moved", -32013, { evenerErrorInfo: ErrorStaleEntry });

/** A registry the ports read pairs from, a request log, and scripted answers. */
function harness(rows: HostRow[]) {
  const registry = { rows, reads: 0 };
  const sent: { method: string; params: Record<string, unknown>; timeoutMs?: number }[] = [];
  const answers: ((params: Record<string, unknown>) => unknown)[] = [];
  let ids = 0;
  const mutations = createHostMutations({
    client: () => ({
      request: (async (method: string, params: Record<string, unknown>, opts?: { timeoutMs?: number }) => {
        sent.push({ method, params, timeoutMs: opts?.timeoutMs });
        const answer = answers.shift();
        if (!answer) throw new Error(`no answer scripted for ${method}`);
        return answer(params);
      }) as never,
    }),
    heldPair: (name): HostMutationPair | undefined => {
      const held = registry.rows.find((candidate) => candidate.name === name);
      return held && { generation: held.generation, incarnationId: held.incarnationId };
    },
    reRead: async () => {
      registry.reads += 1;
    },
    newMutationId: () => `m${++ids}`,
  });
  return { registry, sent, answers, mutations };
}

describe("guarded host mutations (registry spec 08 §12)", () => {
  test("an update echoes the held pair with a fresh mutation id and returns the committed row", async () => {
    const h = harness([row("alpha")]);
    h.answers.push(() => ({ outcome: "committed", host: row("alpha", { address: "a2", generation: 2 }) }));
    const updated = await h.mutations.update({ name: "alpha", entry: { address: "a2" } });
    expect(updated.generation).toBe(2);
    expect(h.sent).toEqual([
      {
        method: "evener/host/update",
        params: {
          name: "alpha",
          entry: { address: "a2" },
          mutationId: "m1",
          expectedGeneration: 1,
          expectedIncarnationId: "alpha-1",
        },
        timeoutMs: HOST_GATE_TIMEOUT_MS,
      },
    ]);
  });

  test("a stale-entry refusal re-reads and retries once with the new pair and a new mutation id", async () => {
    const h = harness([row("alpha")]);
    h.answers.push(() => {
      h.registry.rows = [row("alpha", { generation: 2 })];
      throw stale();
    });
    h.answers.push(() => ({ outcome: "committed", host: row("alpha", { generation: 3 }) }));
    await h.mutations.update({ name: "alpha", entry: { address: "a1" } });
    expect(h.registry.reads).toBe(1);
    expect(h.sent.map((request) => [request.params.expectedGeneration, request.params.mutationId])).toEqual([
      [1, "m1"],
      [2, "m2"],
    ]);
  });

  test("a second stale-entry refusal surfaces to the caller", async () => {
    const h = harness([row("alpha")]);
    h.answers.push(() => {
      throw stale();
    });
    h.answers.push(() => {
      throw stale();
    });
    await expect(h.mutations.remove("alpha")).rejects.toThrow("the entry moved");
    expect(h.sent).toHaveLength(2);
  });

  test("any other refusal is not retried", async () => {
    const h = harness([row("alpha")]);
    h.answers.push(() => {
      throw new WireError("missing ssh destination", -32602, { evenerErrorInfo: "invalidHostField", field: "address" });
    });
    await expect(h.mutations.update({ name: "alpha", entry: { address: "" } })).rejects.toThrow(
      "missing ssh destination",
    );
    expect(h.sent).toHaveLength(1);
    expect(h.registry.reads).toBe(0);
  });

  test("a name no row holds is read for once, then refused locally without a request", async () => {
    const h = harness([]);
    await expect(h.mutations.remove("alpha")).rejects.toThrow(/not listed/);
    expect(h.registry.reads).toBe(1);
    expect(h.sent).toHaveLength(0);
  });

  test("a remove sends the held pair and accepts the committed removed row", async () => {
    const h = harness([row("alpha")]);
    h.answers.push(() => ({ outcome: "committed", host: { name: "alpha", generation: 1, incarnationId: "alpha-1" } }));
    await h.mutations.remove("alpha");
    expect(h.sent[0]).toMatchObject({
      method: "evener/host/remove",
      params: { name: "alpha", mutationId: "m1", expectedGeneration: 1, expectedIncarnationId: "alpha-1" },
      timeoutMs: HOST_GATE_TIMEOUT_MS,
    });
  });
});

describe("committed mutation rows (registry spec 08 §11)", () => {
  test.each([
    [
      "an ambiguous keyless add",
      { outcome: "ambiguous", observedRow: row("alpha") },
      /cannot tell whether it committed/,
    ],
    ["a dropped collision", { outcome: "collision-dropped", droppedEntry: {} }, /hub.toml edit won the race/],
    [
      "a teardown failure",
      { outcome: "committed-with-teardown-failure", seam: "rebind", remnantId: "r1" },
      /rebind teardown failed.*remnantId r1/,
    ],
    ["an unknown arm", { outcome: "later" }, /no arm this client knows/],
  ])("refuses %s as a success", (_name, result, message) => {
    expect(() => committedMutationRow(result as never, "evener/host/update")).toThrow(message);
  });
});

describe("the roots field", () => {
  test("reads one root per line, trimmed, with blank lines dropped and order kept", () => {
    expect(rootsFromText("  /srv/b \n\n/srv/a\n   \n")).toEqual(["/srv/b", "/srv/a"]);
  });

  test("writes one root per line, and nothing for none", () => {
    expect(rootsToText(["/srv/b", "/srv/a"])).toBe("/srv/b\n/srv/a");
    expect(rootsToText(undefined)).toBe("");
  });
});

describe("the editable host fields", () => {
  test("list every mutable HostEntry field under its wire spelling, in the dialog's order", () => {
    expect(HOST_ENTRY_FIELD_ORDER).toEqual(["address", "user", "keyPath", "evenerPath", "configPath", "addr", "roots"]);
  });

  test("name each field and say what it takes, as the web's Edit dialog does", () => {
    expect(HOST_ENTRY_FIELD_TEXT.address).toEqual({
      label: "SSH address",
      help: "SSH destination, e.g. host.example or user@host.example.",
    });
    expect(HOST_ENTRY_FIELD_TEXT.roots).toEqual({
      label: "Roots",
      help: "Optional directories on the host to serve. One per line.",
    });
  });
});
