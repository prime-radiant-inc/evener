import assert from "node:assert/strict";
import test from "node:test";

import { assembleCaseStabilization } from "../concept-browser.mjs";

test("wired case stabilization retains ancestor evidence and post-focus failure once", () => {
  const ancestorEvidence = {
    identity: "visibility-loop",
    affectedElement: "control",
    chosenTime: 50,
    after: {
      effectiveOpacity: 0.5,
      effectiveVisible: true,
      ancestorStyles: [
        {
          element: "shell",
          display: "block",
          visibility: "visible",
          opacity: 0.5,
        },
      ],
    },
  };
  const failure = {
    code: "infinite-animation-no-visible-representative",
    identity: "post-loop",
    affectedElement: "post-control",
  };
  const result = assembleCaseStabilization(
    { capabilityFailures: [], infiniteStabilized: [ancestorEvidence] },
    [{ capabilityFailures: [failure], infiniteStabilized: [] }],
    { capabilityFailures: [failure], infiniteStabilized: [] },
  );
  assert.equal(
    result.stabilization.preCollection.infiniteStabilized[0],
    ancestorEvidence,
  );
  assert.equal(result.stabilization.postFocus.capabilityFailures[0], failure);
  assert.deepEqual(result.capabilityFailures, [failure]);
});
