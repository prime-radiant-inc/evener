import assert from "node:assert/strict";
import { test } from "node:test";
import * as recovery from "./management-recovery.mjs";

test("acknowledged mutations retain their outcome when readback fails", async () => {
  for (const cause of [new Error("private-read-7d5f"), undefined, null, "private-read-7d5f"]) {
    let writes = 0,
      reads = 0,
      decodes = 0;
    const hub = {
      request: async () => {
        writes++;
        return { started: false };
      },
    };
    await assert.rejects(
      recovery.mutateAndReadback(
        hub,
        "goal/set",
        { ref: "private-ref-4ab2", objective: "private-objective-9c8a" },
        (value) => {
          assert.deepEqual(value, { started: false });
          decodes++;
        },
        async () => {
          reads++;
          throw cause;
        },
      ),
      (error) => {
        assert.equal(error.outcome, "acknowledged");
        assert.equal(error.execution, "unverified");
        assert.equal(error.method, "goal/set");
        assert.equal(error.cause, cause);
        assert.ok(error instanceof recovery.AcknowledgedReadbackError);
        assert.ok(Object.hasOwn(error, "cause"));
        const visible = recovery.managementErrorMessage(error, "Operation failed.");
        assert.deepEqual(JSON.parse(visible), {
          outcome: "acknowledged",
          execution: "unverified",
          readback: "unavailable",
        });
        for (const secret of ["private-read-7d5f", "private-ref-4ab2", "private-objective-9c8a"])
          assert.ok(!error.message.includes(secret) && !visible.includes(secret));
        return true;
      },
    );
    assert.deepEqual({ writes, reads, decodes }, { writes: 1, reads: 1, decodes: 1 });
  }
});

test("failed mutations preserve both errors and never claim acknowledgment", async () => {
  for (const mutationError of [undefined, null, new Error("mutation")]) {
    const readError = new Error("read");
    let writes = 0,
      reads = 0;
    await assert.rejects(
      recovery.mutateAndReadback(
        {
          request: async () => {
            writes++;
            throw mutationError;
          },
        },
        "goal/set",
        {},
        () => assert.fail("decode must not run"),
        async () => {
          reads++;
          throw readError;
        },
      ),
      (error) => {
        assert.ok(error instanceof AggregateError);
        assert.deepEqual(error.errors, [mutationError, readError]);
        assert.equal(error.outcome, undefined);
        assert.equal(recovery.managementErrorMessage(error, "Operation incomplete."), "Operation incomplete.");
        return true;
      },
    );
    assert.deepEqual({ writes, reads }, { writes: 1, reads: 1 });
  }
});

test("decoder failures remain uncertain even when readback succeeds", async () => {
  let writes = 0,
    reads = 0;
  const snapshot = { state: "current" };
  const result = await recovery.mutateAndReadback(
    {
      request: async () => {
        writes++;
        return {};
      },
    },
    "goal/set",
    {},
    () => {
      throw undefined;
    },
    async () => {
      reads++;
      return snapshot;
    },
  );
  assert.deepEqual(result, { outcome: "uncertain", readback: snapshot });
  assert.deepEqual({ writes, reads }, { writes: 1, reads: 1 });
});

test("unclassified errors cannot inject private content into CLI diagnostics", () => {
  for (const error of [undefined, null, new Error("private"), { outcome: "acknowledged", message: "private" }])
    assert.equal(recovery.managementErrorMessage(error, "Operation incomplete."), "Operation incomplete.");
});
