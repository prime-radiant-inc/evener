import { describe, expect, expectTypeOf, test } from "vitest";
import { HostMutationOutcomeError, WireError } from "./errors";
import {
  committedMutationRow,
  createHostMutations,
  ErrorStaleEntry,
  HOST_CHANGED_MESSAGE,
  HOST_ENTRY_FIELD_ORDER,
  HOST_ENTRY_FIELD_TEXT,
  HOST_ENTRY_FIELD_WHEN_EMPTY,
  HOST_GATE_TIMEOUT_MS,
  type HostMutationPair,
  hostChangedSinceOpened,
  rootsFromText,
  rootsToText,
} from "./hostMutations";
import type { HostRow, MethodTypes, RemovedRow } from "./types.gen";

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
  test("an update echoes the pair of the row its form opened on, with a fresh mutation id", async () => {
    // The rows have moved on since the form opened; the edit still speaks for
    // the row the person saw.
    const h = harness([row("alpha", { generation: 5 })]);
    h.answers.push(() => ({ outcome: "committed", host: row("alpha", { address: "a2", generation: 2 }) }));
    const updated = await h.mutations.update({
      name: "alpha",
      entry: { address: "a2" },
      expected: { generation: 1, incarnationId: "alpha-1" },
    });
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

  test("an update refused as stale surfaces to its form, never retried over the newer edit", async () => {
    // Only add and update advance a generation (spec 08), so a stale update
    // means someone else edited the host: retrying would overwrite that edit.
    const h = harness([row("alpha", { generation: 2 })]);
    h.answers.push(() => {
      throw stale();
    });
    await expect(
      h.mutations.update({
        name: "alpha",
        entry: { address: "a1" },
        expected: { generation: 1, incarnationId: "alpha-1" },
      }),
    ).rejects.toThrow("the entry moved");
    expect(h.sent).toHaveLength(1);
    expect(h.registry.reads).toBe(0);
  });

  test("a remove refused as stale re-reads and retries once with the new pair and a new mutation id", async () => {
    const h = harness([row("alpha")]);
    h.answers.push(() => {
      h.registry.rows = [row("alpha", { generation: 2 })];
      throw stale();
    });
    h.answers.push(() => ({ outcome: "committed", host: { name: "alpha", generation: 2, incarnationId: "alpha-1" } }));
    await h.mutations.remove("alpha");
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

  test("a stale-entry refusal whose re-read no longer lists the host refuses locally, reading only once", async () => {
    const h = harness([row("alpha")]);
    h.answers.push(() => {
      h.registry.rows = [];
      throw stale();
    });
    await expect(h.mutations.remove("alpha")).rejects.toThrow(/not listed/);
    expect(h.registry.reads).toBe(1);
    expect(h.sent).toHaveLength(1);
  });

  test("any other refusal is not retried", async () => {
    const h = harness([row("alpha")]);
    h.answers.push(() => {
      throw new WireError("missing ssh destination", -32602, { evenerErrorInfo: "invalidHostField", field: "address" });
    });
    await expect(
      h.mutations.update({
        name: "alpha",
        entry: { address: "" },
        expected: { generation: 1, incarnationId: "alpha-1" },
      }),
    ).rejects.toThrow("missing ssh destination");
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

test("generated mutation results narrow by outcome to their method's row type", () => {
  type Add = MethodTypes["evener/host/add"]["result"];
  type Update = MethodTypes["evener/host/update"]["result"];
  type Remove = MethodTypes["evener/host/remove"]["result"];
  expectTypeOf<Extract<Add, { outcome: "committed" }>["host"]>().toEqualTypeOf<HostRow>();
  expectTypeOf<Extract<Update, { outcome: "committed" }>["host"]>().toEqualTypeOf<HostRow>();
  expectTypeOf<Extract<Remove, { outcome: "committed" }>["host"]>().toEqualTypeOf<RemovedRow>();
  expectTypeOf<Extract<Update | Remove, { outcome: "ambiguous" }>>().toEqualTypeOf<never>();
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
    ["a committed arm without its row", { outcome: "committed" }, /no arm this client knows/],
  ])("refuses %s as a success", (_name, result, message) => {
    expect(() => committedMutationRow(result as never, "evener/host/update")).toThrow(message);
  });

  test.each([
    ["an ambiguous keyless add", { outcome: "ambiguous", observedRow: row("alpha") }, "ambiguous"],
    ["a dropped collision", { outcome: "collision-dropped", droppedEntry: {} }, "collision-dropped"],
    [
      "a teardown failure",
      { outcome: "committed-with-teardown-failure", seam: "rebind", remnantId: "r1" },
      "committed-with-teardown-failure",
    ],
    ["an unknown arm", { outcome: "later" }, "unknown"],
  ])("refuses %s as a typed error carrying its arm", (_name, result, outcome) => {
    let thrown: unknown;
    try {
      committedMutationRow(result as never, "evener/host/update");
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toBeInstanceOf(HostMutationOutcomeError);
    expect((thrown as HostMutationOutcomeError).outcome).toBe(outcome);
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

  test("say what each optional field means while it's empty, as the hub's ssh dial treats it", () => {
    // sshconn/runner.go: no user is added to the address, no -i without a key
    // path, `evener` when no path is set, and no --config or --addr flags.
    expect(HOST_ENTRY_FIELD_WHEN_EMPTY).toEqual({
      user: "From the address or SSH config",
      keyPath: "From your SSH config",
      evenerPath: "evener on PATH",
      configPath: "The default hub.toml",
      addr: "The default address",
      roots: "No project roots. One per line.",
    });
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

describe("an edit refused because the host changed", () => {
  test("is told apart from any other refusal", () => {
    expect(hostChangedSinceOpened(stale())).toBe(true);
    expect(hostChangedSinceOpened(new WireError("missing ssh destination", -32602))).toBe(false);
    expect(hostChangedSinceOpened(new Error("the entry moved"))).toBe(false);
  });

  test("says what happened and what to do", () => {
    expect(HOST_CHANGED_MESSAGE).toBe(
      "This host changed since you opened it. Cancel, then open it again to see the change.",
    );
  });
});
