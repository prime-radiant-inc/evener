import assert from "node:assert/strict";
import WebSocket from "ws";
import type { WebSocketLike } from "../../cmd/evener-hub/frontend/src/protocol/transport";
import { createHubClient } from "../src/connection";
import { LaunchSettings } from "../src/launchSettings";

// Guard this mutation exercise by the owned hub's exact state root.
const client = createHubClient(
  "http://127.0.0.1:9200",
  "",
  (url, options) => new WebSocket(url, options) as unknown as WebSocketLike,
);
const model = new LaunchSettings(client, "/", "global");
try {
  await client.connect();
  const overview = await client.request("evener/settings/overview", {});
  assert.equal(
    overview.storage?.stateDir,
    "/private/tmp/evener-native-second-v22jd1ct/state/evener",
  );
  model.start();
  await model.refresh();
  const original = model.getSnapshot().current;
  assert.ok(original);
  assert.ok(
    model
      .getSnapshot()
      .options?.some(
        (option) =>
          option.wireField === "maxRounds" &&
          option.defaultableLayers?.includes("global"),
      ),
  );
  const next = original.maxRounds === 7 ? 8 : 7;
  try {
    model.edit("maxRounds", next);
    assert.equal(
      await model.save(),
      true,
      model.getSnapshot().error ?? "Save was not confirmed",
    );
    const saved = await client.request("evener/launch/getLayer", {
      cwd: "/",
      layer: "global",
    });
    assert.equal(saved.maxRounds, next);
    assert.deepEqual(
      { ...saved, maxRounds: original.maxRounds },
      { ...original, maxRounds: original.maxRounds },
    );
    console.log(
      "Isolated hub launch save and unrelated-field preservation passed.",
    );
  } finally {
    await model.refresh(true);
    model.edit("maxRounds", original.maxRounds);
    if (model.getSnapshot().dirty)
      assert.equal(
        await model.save(),
        true,
        "Fixture launch default restoration failed",
      );
    const restored = await client.request("evener/launch/getLayer", {
      cwd: "/",
      layer: "global",
    });
    assert.deepEqual(restored, original);
    console.log("Original isolated hub launch layer restored.");
  }
} finally {
  model.dispose();
  client.close();
}
