import assert from "node:assert/strict";

export class AcknowledgedReadbackError extends Error {
  constructor(method, cause) {
    super("The change was acknowledged, but current state could not be read.", { cause });
    this.name = "AcknowledgedReadbackError";
    this.method = method;
    this.outcome = "acknowledged";
    this.execution = "unverified";
  }
}

// CLI diagnostics expose the outcome without private response or error bodies.
export function managementErrorMessage(error, fallback) {
  return error instanceof AcknowledgedReadbackError
    ? JSON.stringify({ outcome: "acknowledged", execution: "unverified", readback: "unavailable" })
    : fallback;
}

export function requireOwnedHub(optIn, ownedHub) {
  assert.equal(optIn, "1", "Management mutation requires explicit opt-in.");
  assert.ok(typeof ownedHub === "string" && ownedHub.trim().length > 0, "Confirm ownership of the target hub.");
  assert.equal(ownedHub, process.env.EVENER_RPC_URL, "Owned hub must match the connected endpoint.");
}

// A fresh list describes current state; it cannot prove who made a change.
export async function mutateAndReadback(hub, method, params, decode, read) {
  let mutationFailed = false;
  let mutationError;
  try {
    decode(await hub.request(method, params));
  } catch (error) {
    mutationFailed = true;
    mutationError = error;
  }
  let readback;
  try {
    readback = await read();
  } catch (readError) {
    if (mutationFailed)
      throw new AggregateError([mutationError, readError], "Management mutation and readback failed.");
    throw new AcknowledgedReadbackError(method, readError);
  }
  return { outcome: mutationFailed ? "uncertain" : "acknowledged", readback };
}
