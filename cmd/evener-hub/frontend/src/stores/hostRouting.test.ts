// @vitest-environment node

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, test } from "vitest";
import { HOST_DEPENDENT_DISCOVERY_METHODS } from "./hostRouting";

// The CROSS-LANGUAGE half of this contract. The shipped set above and the Go
// proxy's allow-list (appwire/host_request.go's hostRequestMethods)
// are both pinned to the checked-in list at cmd/evener-hub/
// host_request_methods.txt, read here and by app_host_admin_test.go. That file
// carries the rationale for what belongs on the list; this test's job is to
// prove the shipped set still IS it, in both directions, so a method dropped
// from the product set (with or without the literal list it used to be spelled
// against here) fails rather than silently narrowing what the pane forwards.
const here = dirname(fileURLToPath(import.meta.url));
const SHARED_LIST_PATH = join(here, "../../../host_request_methods.txt");

function sharedForwardedMethods(): string[] {
  return readFileSync(SHARED_LIST_PATH, "utf8")
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "" && !line.startsWith("#"));
}

describe("hostRouting discovery inventory", () => {
  test("names exactly the shared forwarded-method list", () => {
    const shared = sharedForwardedMethods();
    // An empty or missing list would make this assertion vacuous, so it is
    // checked rather than assumed.
    expect(shared.length).toBeGreaterThan(0);
    expect([...HOST_DEPENDENT_DISCOVERY_METHODS].sort()).toEqual([...shared].sort());
  });
});
