import assert from "node:assert/strict";
import { decodeStatus } from "./credentials-status.mjs";
import { AcknowledgedReadbackError } from "./management-recovery.mjs";

export { AcknowledgedReadbackError } from "./management-recovery.mjs";

export class OAuthReadbackError extends AcknowledgedReadbackError {
  constructor(method, acknowledged, cause) {
    super(method, cause);
    this.acknowledged = acknowledged;
  }
}

const actions = ["device/start", "device/poll", "login/start", "login/complete"];
const isRecord = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const nonempty = (value) => typeof value === "string" && value.trim().length > 0;

function safeURL(value) {
  assert.ok(nonempty(value), "Provide an OAuth URL.");
  let url;
  try {
    url = new URL(value);
  } catch {
    throw new Error("Invalid OAuth URL.");
  }
  assert.ok(["http:", "https:"].includes(url.protocol) && !url.username && !url.password, "Invalid OAuth URL.");
  return value;
}

// Capture inputs before connecting so edits cannot retarget an in-flight operation.
export function captureOAuthInput({ action, params, ownedHub, environment = process.env }) {
  assert.ok(actions.includes(action), "Unknown OAuth action.");
  const endpoint = environment.EVENER_RPC_URL;
  assert.equal(environment.EVENER_OAUTH_MUTATION, "1", "OAuth mutation requires explicit opt-in.");
  assert.ok(nonempty(endpoint) && ownedHub === endpoint, "Confirm ownership of the connected hub.");
  assert.ok(isRecord(params), "Provide OAuth parameters.");
  const allowed = action.endsWith("/start")
    ? ["provider", "reviewed"]
    : action === "device/poll"
      ? ["provider", "flow"]
      : ["provider", "flow", "redirectUrl"];
  assert.ok(
    Object.keys(params).every((key) => allowed.includes(key)),
    "Unknown OAuth parameter.",
  );
  assert.ok(nonempty(params.provider), "Provide an OAuth provider.");
  if (action.endsWith("/start")) {
    decodeStatus(params.reviewed, params.provider);
  } else {
    const flow = params.flow;
    assert.ok(isRecord(flow) && nonempty(flow.flowId), "Provide an OAuth flow.");
    assert.equal(flow.provider, params.provider, "OAuth flow provider changed.");
    assert.equal(flow.rpcUrl, endpoint, "OAuth flow endpoint changed.");
    assert.equal(flow.kind, action === "device/poll" ? "device" : "login", "OAuth flow kind changed.");
    if (action === "login/complete") safeURL(params.redirectUrl);
  }
  return { action, endpoint, input: structuredClone(params) };
}

function decodeStart(value, provider, kind, endpoint) {
  assert.ok(isRecord(value) && value.provider === provider, "Invalid OAuth start provider.");
  if (kind === "device" && value.fallback === true) return { outcome: "fallback", provider };
  assert.ok(value.fallback === undefined || value.fallback === false, "Invalid OAuth fallback.");
  assert.ok(nonempty(value.flowId), "Invalid OAuth flow.");
  const flow = {
    outcome: "started",
    rpcUrl: endpoint,
    provider,
    kind,
    flowId: value.flowId,
    url: safeURL(kind === "device" ? value.verificationUrl : value.url),
  };
  if (kind === "device") {
    assert.ok(nonempty(value.userCode), "Invalid OAuth user code.");
    assert.ok(Number.isSafeInteger(value.intervalSeconds) && value.intervalSeconds >= 0, "Invalid OAuth interval.");
    flow.userCode = value.userCode;
    flow.intervalSeconds = value.intervalSeconds;
  }
  return flow;
}

function decodeAuthorized(value, provider) {
  const status = decodeStatus(value, provider);
  assert.ok(status.supported && status.signedIn && status.hasStoredOAuth, "Invalid OAuth authorized status.");
  return status;
}

function decodeReply(action, reply, provider, endpoint) {
  if (action.endsWith("/start")) return decodeStart(reply, provider, action.split("/")[0], endpoint);
  assert.ok(isRecord(reply), "Invalid OAuth reply.");
  if (action === "device/poll") {
    assert.ok(["pending", "expired", "authorized"].includes(reply.state), "Invalid OAuth poll state.");
    if (reply.state !== "authorized") return { outcome: reply.state, state: reply.state };
  }
  return { outcome: "authorized", acknowledged: decodeAuthorized(reply.status, provider) };
}

export async function runOAuth(hub, options) {
  const { action, endpoint, input } = captureOAuthInput(options);
  const provider = input.provider;
  const readStatus = async () => decodeStatus(await hub.request("evener/auth/status", { provider }), provider);
  await hub.connect();
  if (action.endsWith("/start")) {
    const before = await readStatus();
    assert.ok(before.supported && before.authModes?.includes("oauth"), "Provider does not support OAuth.");
    assert.deepEqual(before, input.reviewed, "Credential state changed; review current state.");
  }
  const method = `evener/auth/${action}`;
  const params = action.endsWith("/start")
    ? { provider }
    : {
        provider,
        flowId: input.flow.flowId,
        ...(action === "login/complete" ? { redirectUrl: input.redirectUrl } : {}),
      };
  let decoded;
  try {
    decoded = decodeReply(action, await hub.request(method, params), provider, endpoint);
  } catch (mutationError) {
    // Current credentials cannot prove whether this particular attempt committed.
    try {
      return { action, outcome: "uncertain", readback: await readStatus() };
    } catch (readbackError) {
      throw new AggregateError([mutationError, readbackError], "OAuth mutation and readback failed.");
    }
  }
  if (decoded.outcome !== "authorized") return { action, ...decoded };
  try {
    return { action, ...decoded, readback: await readStatus() };
  } catch (readbackError) {
    throw new OAuthReadbackError(method, decoded.acknowledged, readbackError);
  }
}

export function safeOAuthSummary(result) {
  return {
    action: result.action,
    outcome: result.outcome,
    ...(result.readback ? { signedIn: result.readback.signedIn } : {}),
  };
}
