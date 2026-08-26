import assert from "node:assert/strict";
import test from "node:test";

import { captureFocusDifferential } from "../concept-browser.mjs";

test("focus differential capture stabilizes every intentional paint state", async () => {
  const model = {
    state: "unfocused",
    transitioning: false,
    nonProbePixel: 7,
    events: [],
  };
  const begin = (state) => {
    model.state = state;
    model.transitioning = true;
    model.nonProbePixel += 1;
    model.events.push(`mutate:${state}`);
  };
  const result = await captureFocusDifferential(
    {},
    {
      id: "Settings focus indicator",
      raw: { oracleId: "focus-settings" },
    },
    {
      focus: async () => begin("original"),
      mutate: async (_client, _candidate, state) => begin(state),
      restore: async () => begin("restored"),
      stabilize: async () => {
        model.events.push(`stabilize:${model.state}`);
        model.transitioning = false;
        model.nonProbePixel = 42;
        return { state: model.state, eventDriven: true };
      },
      geometry: async () => ({
        left: 10,
        top: 10,
        right: 30,
        bottom: 30,
        width: 20,
        height: 20,
        geometryCollectedAt: 1,
      }),
      capture: async () => {
        assert.equal(model.transitioning, false, `captured ${model.state} in flight`);
        return {
          pixels: { probe: model.state, nonProbe: model.nonProbePixel },
          metadata: { capturedAt: model.events.length },
        };
      },
    },
  );

  assert.deepEqual(
    Object.values(result.captures).map(({ pixels }) => pixels.nonProbe),
    [42, 42, 42, 42],
  );
  assert.deepEqual(Object.keys(result.stabilization), [
    "original",
    "hidden",
    "black",
    "white",
    "restore",
  ]);
  assert.deepEqual(model.events, [
    "mutate:original",
    "stabilize:original",
    "mutate:hidden",
    "stabilize:hidden",
    "mutate:black",
    "stabilize:black",
    "mutate:white",
    "stabilize:white",
    "mutate:restored",
    "stabilize:restored",
  ]);
});
