// hubWireFixtures reads the hub's own recorded credential-wire answers.
//
// The credential labels key on the registry's vocabulary — activeSource
// (api_key | credential_headers | store | env:<VAR> | oauth | adc | none),
// authModes, credentialRequired — and a test that hand-builds those values
// pins a vocabulary the hub may no longer send. That is exactly how the TUI
// came to render "signed out" for an instance the hub was reporting as
// env:OPENAI_API_KEY, with a green suite the whole time.
//
// cmd/evener-hub's TestAuthWireFixturesMatchTheHubHandler produces the
// fixture by driving the real registered evener/instance/list handler over a
// hermetic registry, and re-verifies it on every Go test run; regenerate it
// with `make fuzz-goldens`.
//
// The fixture is loaded through Vite's `?raw` import, the same way the
// reducer tests load their .jsonl fixtures: an import resolves against this
// file, where a filesystem read would resolve against whichever working
// directory the consumer's test runner happens to use (the package's tests
// run under the web app's Vitest, and package-test-files.mjs refuses a
// working-directory-relative read).

import authWireResponses from "../../../cmd/evener-hub/testdata/authwire/responses.json?raw";
import type { InstanceEntry } from "../types.gen";

const FIXTURE_PATH = "cmd/evener-hub/testdata/authwire/responses.json";

interface AuthWireFixture {
  case: string;
  method: string;
  field?: string;
  response: unknown;
}

/** hubInstanceEntries returns the instance rows the hub actually sends. */
export function hubInstanceEntries(): InstanceEntry[] {
  const records = JSON.parse(authWireResponses) as AuthWireFixture[];
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
