// hubWireFixtures reads the hub's own recorded credential-wire answers.
//
// The credentials pane keys on the registry's vocabulary — activeSource
// (api_key | credential_headers | store | env:<VAR> | oauth | adc | none),
// authModes, credentialRequired — and a pane test that hand-builds those
// values pins a vocabulary the hub may no longer send. That is exactly how
// the TUI came to render "signed out" for an instance the hub was reporting
// as env:OPENAI_API_KEY, with a green suite the whole time.
//
// cmd/evener-hub's TestAuthWireFixturesMatchTheHubHandler produces this file
// by driving the real registered evener/instance/list handler over a
// hermetic registry, and re-verifies it on every Go test run; regenerate it
// with `make fuzz-goldens`.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import type { InstanceEntry } from "../types.gen";

interface AuthWireFixture {
  case: string;
  method: string;
  field?: string;
  response: unknown;
}

// Relative to the frontend package root, which is what vitest sets the
// working directory to.
const FIXTURE_PATH = join("..", "testdata", "authwire", "responses.json");

/** hubInstanceEntries returns the instance rows the hub actually sends. */
export function hubInstanceEntries(): InstanceEntry[] {
  const records = JSON.parse(readFileSync(FIXTURE_PATH, "utf8")) as AuthWireFixture[];
  const listing = records.find((rec) => rec.method === "evener/instance/list" && rec.field === "instances");
  if (!listing) throw new Error(`no evener/instance/list fixture in ${FIXTURE_PATH}`);
  const entries = listing.response as InstanceEntry[];
  if (entries.length === 0) throw new Error(`fixture ${listing.case} carries no instances`);
  return entries;
}

/** hubInstance returns one recorded row by instance name. */
export function hubInstance(name: string): InstanceEntry {
  const found = hubInstanceEntries().find((entry) => entry.name === name);
  if (!found) throw new Error(`no instance ${name} in ${FIXTURE_PATH}`);
  return found;
}
