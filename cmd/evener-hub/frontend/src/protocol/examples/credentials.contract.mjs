import assert from "node:assert/strict";
import test from "node:test";
import { runCredentials, safeCredentialSummary } from "./credentials-logic.mjs";
import { AcknowledgedReadbackError } from "./management-recovery.mjs";

const url = "ws://127.0.0.1:1/rpc";
const status = (overrides = {}) => ({
  provider: "anthropic",
  supported: true,
  signedIn: true,
  activeSource: "store",
  authModes: ["apiKey"],
  hasStoredOAuth: false,
  hasStoredFile: true,
  ...overrides,
});
function fixture({ before = status(), mutation = status(), readback = status(), onConnect } = {}) {
  const calls = [];
  let reads = 0;
  return {
    calls,
    hub: {
      async connect() {
        calls.push({ method: "connect" });
        onConnect?.();
      },
      async request(method, params) {
        calls.push({ method, params });
        if (method === "evener/auth/status") {
          const value = ++reads === 1 ? before : readback;
          if (value instanceof Error) throw value;
          return value;
        }
        if (method === "evener/auth/list") return { providers: [before] };
        if (mutation instanceof Error) throw mutation;
        return mutation;
      },
    },
  };
}
async function optedIn(run) {
  const keys = ["EVENER_CREDENTIAL_MUTATION", "EVENER_RPC_URL"];
  const previous = keys.map((key) => process.env[key]);
  process.env.EVENER_CREDENTIAL_MUTATION = "1";
  process.env.EVENER_RPC_URL = url;
  try {
    await run();
  } finally {
    keys.forEach((key, index) => {
      if (previous[index] === undefined) delete process.env[key];
      else process.env[key] = previous[index];
    });
  }
}

test("credential list is read-only and validates structured rows", async () => {
  const f = fixture();
  const result = await runCredentials(f.hub);
  assert.equal(result.outcome, "read");
  assert.equal(result.readback.length, 1);
  assert.deepEqual(f.calls, [{ method: "connect" }, { method: "evener/auth/list", params: {} }]);
});

test("credential list preserves unknown fields and rejects duplicate providers", async () => {
  const f = fixture();
  f.hub.request = async (method, params) => {
    f.calls.push({ method, params });
    if (method === "evener/auth/list")
      return {
        providers: [status({ provider: "anthropic", futureField: { retained: true } }), status({ provider: "openai" })],
        futureListField: "ignored by decoder",
      };
    return undefined;
  };
  const result = await runCredentials(f.hub);
  assert.deepEqual(result.readback[0].futureField, { retained: true });

  const duplicate = fixture();
  duplicate.hub.request = async (method, params) => {
    duplicate.calls.push({ method, params });
    if (method === "evener/auth/list") return { providers: [status(), status()] };
    return undefined;
  };
  await assert.rejects(runCredentials(duplicate.hub), /duplicate credential provider/);

  const empty = fixture();
  empty.hub.request = async (method, params) => {
    empty.calls.push({ method, params });
    if (method === "evener/auth/list") return { providers: null };
    return undefined;
  };
  assert.deepEqual((await runCredentials(empty.hub)).readback, []);

  for (const malformed of [status({ needsLogin: "yes" }), status({ authModes: ["apiKey", ""] })]) {
    const bad = fixture();
    bad.hub.request = async (method, params) => {
      bad.calls.push({ method, params });
      if (method === "evener/auth/list") return { providers: [malformed] };
      return undefined;
    };
    await assert.rejects(runCredentials(bad.hub), /Invalid credential/);
  }
});

test("reviewed mutations require a complete current snapshot and capture it before connect", async () =>
  await optedIn(async () => {
    const authored = status({ futureField: { reviewed: true } });
    const params = { provider: "anthropic", value: "secret-value", reviewed: structuredClone(authored) };
    const f = fixture({
      before: authored,
      onConnect: () => {
        params.reviewed.signedIn = false;
      },
    });
    const result = await runCredentials(f.hub, { action: "apiKey/set", params, ownedHub: url });
    assert.equal(result.outcome, "acknowledged");

    const incomplete = fixture({ before: authored });
    await assert.rejects(
      runCredentials(incomplete.hub, {
        action: "apiKey/set",
        params: { provider: "anthropic", value: "x", reviewed: { provider: "anthropic" } },
        ownedHub: url,
      }),
      /Invalid credential/,
    );
    assert.deepEqual(
      incomplete.calls.map((call) => call.method),
      [],
    );
  }));
test("operation params reject fields belonging to another operation", async () =>
  await optedIn(async () => {
    await assert.rejects(
      runCredentials(fixture().hub, { action: "status", params: { provider: "anthropic", reviewed: {} } }),
    );
    await assert.rejects(
      runCredentials(fixture().hub, {
        action: "apiKey/clear",
        params: { provider: "anthropic", value: "x" },
        ownedHub: url,
      }),
    );
    await assert.rejects(
      runCredentials(fixture().hub, { action: "logout", params: { provider: "anthropic", value: "x" }, ownedHub: url }),
    );
  }));

test("every mutation requires a complete reviewed snapshot", async () =>
  await optedIn(async () => {
    for (const [action, params] of [
      ["apiKey/set", { provider: "anthropic", value: "x" }],
      ["apiKey/clear", { provider: "anthropic" }],
      ["credentialJson/set", { provider: "anthropic", value: "{}" }],
      ["logout", { provider: "anthropic" }],
    ]) {
      const f = fixture({
        before: status({ authModes: action === "credentialJson/set" ? ["credentialJson"] : ["apiKey"] }),
      });
      await assert.rejects(runCredentials(f.hub, { action, params, ownedHub: url }), /reviewed/);
      assert.deepEqual(f.calls, []);
    }
  }));

test("credential mutations preserve authored params and dispatch once", async () =>
  await optedIn(async () => {
    for (const [action, initialParams, method] of [
      ["status", { provider: "anthropic" }, "evener/auth/status"],
      ["apiKey/set", { provider: "anthropic", value: "secret-value" }, "evener/auth/apiKey/set"],
      ["apiKey/clear", { provider: "anthropic" }, "evener/auth/apiKey/clear"],
      [
        "credentialJson/set",
        { provider: "anthropic", value: '{"type":"service_account"}' },
        "evener/auth/credentialJson/set",
      ],
      ["logout", { provider: "anthropic" }, "evener/auth/logout"],
    ]) {
      const before = status({
        authModes: action === "credentialJson/set" ? ["credentialJson"] : ["apiKey", "credentialJson"],
      });
      const f = fixture({
        before,
        mutation:
          action === "logout"
            ? { removed: true, status: status({ signedIn: false, activeSource: "none", hasStoredFile: false }) }
            : status({
                authModes: action === "credentialJson/set" ? ["credentialJson"] : ["apiKey", "credentialJson"],
              }),
      });
      const params = action === "status" ? initialParams : { ...initialParams, reviewed: structuredClone(before) };
      const result = await runCredentials(f.hub, { action, params, ownedHub: url });
      assert.equal(result.outcome, action === "status" ? "read" : "acknowledged");
      assert.equal(f.calls.filter((call) => call.method === method).length, 1);
      if (action !== "status") {
        const expected = { provider: params.provider, ...(params.value ? { value: params.value } : {}) };
        assert.deepEqual(f.calls.find((call) => call.method === method).params, expected);
      }
    }
  }));

test("mutation rejects missing ownership, unsupported mode, malformed payload, and stale review before dispatch", async () =>
  await optedIn(async () => {
    for (const change of [
      { ownedHub: undefined },
      { params: { provider: "anthropic", value: "x", reviewed: { provider: "other" } } },
      {
        action: "apiKey/set",
        params: { provider: "anthropic", value: "x", reviewed: status({ authModes: ["credentialJson"] }) },
        before: status({ authModes: ["credentialJson"] }),
      },
    ]) {
      const f = fixture(change.before ? { before: change.before } : {});
      const options = {
        action: change.action ?? "apiKey/set",
        params: change.params ?? { provider: "anthropic", value: "x" },
        ownedHub: Object.hasOwn(change, "ownedHub") ? change.ownedHub : url,
      };
      await assert.rejects(runCredentials(f.hub, options));
      assert.deepEqual(
        f.calls.map((call) => call.method),
        change.before ? ["connect", "evener/auth/status"] : [],
      );
    }
  }));

test("captured params survive connect, malformed or lost replies stay uncertain, and readback failures never replay", async () =>
  await optedIn(async () => {
    const params = { provider: "anthropic", value: "secret-value", reviewed: status() };
    const captured = fixture({
      onConnect: () => {
        params.value = "changed-after-capture";
      },
    });
    const capturedResult = await runCredentials(captured.hub, { action: "apiKey/set", params, ownedHub: url });
    assert.deepEqual(captured.calls.find((call) => call.method === "evener/auth/apiKey/set").params, {
      provider: "anthropic",
      value: "secret-value",
    });
    assert.equal(capturedResult.outcome, "acknowledged");
    const malformed = fixture({ mutation: {} });
    assert.equal(
      (
        await runCredentials(malformed.hub, {
          action: "apiKey/set",
          params: { provider: "anthropic", value: "x", reviewed: status() },
          ownedHub: url,
        })
      ).outcome,
      "uncertain",
    );
    const uncertain = fixture({ mutation: new Error("private") });
    assert.equal(
      (
        await runCredentials(uncertain.hub, {
          action: "apiKey/set",
          params: { provider: "anthropic", value: "x", reviewed: status() },
          ownedHub: url,
        })
      ).outcome,
      "uncertain",
    );
    assert.equal(uncertain.calls.filter((call) => call.method === "evener/auth/apiKey/set").length, 1);
    const failed = fixture({ readback: new Error("private read") });
    await assert.rejects(
      runCredentials(failed.hub, {
        action: "apiKey/set",
        params: { provider: "anthropic", value: "x", reviewed: status() },
        ownedHub: url,
      }),
      (error) => error instanceof AcknowledgedReadbackError,
    );
    assert.equal(failed.calls.filter((call) => call.method === "evener/auth/apiKey/set").length, 1);
  }));

test("wrong-provider acknowledgments are uncertain and mutation/readback failures do not replay", async () =>
  await optedIn(async () => {
    const wrongAck = fixture({ mutation: status({ provider: "openai" }) });
    const wrongResult = await runCredentials(wrongAck.hub, {
      action: "apiKey/set",
      params: { provider: "anthropic", value: "x", reviewed: status() },
      ownedHub: url,
    });
    assert.equal(wrongResult.outcome, "uncertain");
    assert.equal(wrongAck.calls.filter((call) => call.method === "evener/auth/apiKey/set").length, 1);

    const bothFailed = fixture({ mutation: new Error("private mutation"), readback: new Error("private read") });
    await assert.rejects(
      runCredentials(bothFailed.hub, {
        action: "apiKey/set",
        params: { provider: "anthropic", value: "x", reviewed: status() },
        ownedHub: url,
      }),
      AggregateError,
    );
    assert.equal(bothFailed.calls.filter((call) => call.method === "evener/auth/apiKey/set").length, 1);

    const wrongReadback = fixture({ readback: status({ provider: "openai" }) });
    await assert.rejects(
      runCredentials(wrongReadback.hub, {
        action: "apiKey/clear",
        params: { provider: "anthropic", reviewed: status() },
        ownedHub: url,
      }),
      AcknowledgedReadbackError,
    );
    assert.equal(wrongReadback.calls.filter((call) => call.method === "evener/auth/apiKey/clear").length, 1);
  }));

test("every mutation class treats malformed and lost acknowledgments as uncertain", async () =>
  await optedIn(async () => {
    for (const [action, initialParams, method] of [
      ["apiKey/set", { provider: "anthropic", value: "x" }, "evener/auth/apiKey/set"],
      ["apiKey/clear", { provider: "anthropic" }, "evener/auth/apiKey/clear"],
      ["credentialJson/set", { provider: "anthropic", value: "{}" }, "evener/auth/credentialJson/set"],
      ["logout", { provider: "anthropic" }, "evener/auth/logout"],
    ]) {
      for (const mutation of [{}, new Error("private acknowledgment")]) {
        const f = fixture({
          mutation,
          before: status({ authModes: action === "credentialJson/set" ? ["credentialJson"] : ["apiKey"] }),
        });
        const params = {
          ...initialParams,
          reviewed: status({ authModes: action === "credentialJson/set" ? ["credentialJson"] : ["apiKey"] }),
        };
        const result = await runCredentials(f.hub, { action, params, ownedHub: url });
        assert.equal(result.outcome, "uncertain", `${action} should be uncertain`);
        assert.equal(f.calls.filter((call) => call.method === method).length, 1);
      }
    }
  }));

test("CLI summary contains no credential values", () => {
  const summary = safeCredentialSummary({
    action: "apiKey/set",
    outcome: "acknowledged",
    execution: "unverified",
    readback: status(),
  });
  assert.deepEqual(summary, {
    action: "apiKey/set",
    outcome: "acknowledged",
    execution: "unverified",
    supported: true,
    signedIn: true,
  });
  assert.equal(JSON.stringify(summary).includes("secret"), false);
});

test("CLI summary reads only validated top-level status fields", async () => {
  const f = fixture({
    before: status({ status: { supported: "private", signedIn: { secret: true } }, email: "private@example.test" }),
  });
  const result = await runCredentials(f.hub, { action: "status", params: { provider: "anthropic" } });
  assert.deepEqual(safeCredentialSummary(result), {
    action: "status",
    outcome: "read",
    execution: "unverified",
    supported: true,
    signedIn: true,
  });
});
