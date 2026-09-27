import assert from "node:assert/strict";
import { test } from "node:test";

import { resolveConfig, stateRoot } from "../src/config.js";

test("stateRoot honors the explicit override, then XDG, then HOME", () => {
  assert.equal(stateRoot({ EVENER_HUB_STATE_ROOT: "/srv/evener" }), "/srv/evener");
  assert.equal(stateRoot({ XDG_STATE_HOME: "/xdg" }), "/xdg/evener");
  assert.equal(stateRoot({ HOME: "/home/jesse" }), "/home/jesse/.local/state/evener");
});

test("an explicit token wins over the token file", () => {
  const config = resolveConfig({ EVENER_HUB_TOKEN: "  abc  " }, () => {
    throw new Error("must not read a file when EVENER_HUB_TOKEN is set");
  });
  assert.equal(config.token, "abc");
  assert.equal(config.tokenSource, "EVENER_HUB_TOKEN");
});

test("the token file defaults to the state root and is trimmed", () => {
  let readPath = "";
  const config = resolveConfig({ HOME: "/home/jesse" }, (file) => {
    readPath = file;
    return "\n  token-from-file\n";
  });
  assert.equal(readPath, "/home/jesse/.local/state/evener/auth-token");
  assert.equal(config.token, "token-from-file");
  assert.match(config.tokenSource, /auth-token/);
});

test("EVENER_HUB_TOKEN_FILE names the file to read", () => {
  const config = resolveConfig({ EVENER_HUB_TOKEN_FILE: "/secrets/hub-token", HOME: "/h" }, () => "t");
  assert.equal(config.token, "t");
});

test("a missing token file is an actionable error", () => {
  assert.throws(
    () =>
      resolveConfig({ HOME: "/home/jesse" }, () => {
        throw new Error("ENOENT: no such file");
      }),
    /cannot read the hub token file .*auth-token.*Set EVENER_HUB_TOKEN/,
  );
});

test("an empty token file is refused, not silently accepted", () => {
  assert.throws(() => resolveConfig({ HOME: "/h" }, () => "   \n"), /auth-token.* is empty/);
});

test("the default URL is the hub's default listener, overridable", () => {
  assert.equal(resolveConfig({ EVENER_HUB_TOKEN: "t" }).url, "ws://127.0.0.1:9180/rpc");
  assert.equal(resolveConfig({ EVENER_HUB_TOKEN: "t", EVENER_HUB_RPC_URL: "ws://h:1/rpc" }).url, "ws://h:1/rpc");
});
