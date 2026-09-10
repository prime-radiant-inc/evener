import assert from "node:assert/strict";

export const ownedHub = "ws://127.0.0.1:1/rpc";

export async function withMutationEnv(key, run) {
  const keys = [key, "EVENER_RPC_URL"];
  const previous = keys.map((name) => process.env[name]);
  try {
    process.env[key] = "1";
    process.env.EVENER_RPC_URL = ownedHub;
    return await run();
  } finally {
    keys.forEach((name, index) => {
      if (previous[index] === undefined) delete process.env[name];
      else process.env[name] = previous[index];
    });
  }
}

// Responses are callbacks so throwing undefined remains distinct from returning it.
export function scriptedHub(responses) {
  const calls = [];
  let connected = false;
  let index = 0;
  return {
    calls,
    hub: {
      async connect() {
        assert.equal(connected, false);
        connected = true;
        calls.push({ method: "connect" });
      },
      async request(method, params) {
        assert.equal(connected, true);
        assert.ok(index < responses.length, "Unexpected request");
        calls.push({ method, params });
        return responses[index++](method, params);
      },
    },
  };
}
