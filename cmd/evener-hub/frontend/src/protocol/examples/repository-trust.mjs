import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { WireError } from "@evener/appwire-client";
import { clientFromEnvironment } from "./connection.mjs";

if (process.env.EVENER_EXAMPLE_WRITE_REPO !== "1") {
  throw new Error("Set EVENER_EXAMPLE_WRITE_REPO=1 for an isolated hub sharing this filesystem.");
}
const { hub, cwd: parent } = clientFromEnvironment();
let cwd;
const failures = [];
try {
  // The hub must see this same absolute directory. This is a test fixture,
  // not a remote-file API or an automatic trust policy for application clients.
  cwd = await mkdtemp(join(parent, "appwire-repository-"));
  await mkdir(join(cwd, ".evener"));
  const file = join(cwd, ".evener", "launch.toml");
  await writeFile(file, "max_rounds = 11\n");
  await hub.connect();
  const reviewed = await hub.request("evener/launch/resolve", { cwd });
  if (reviewed.repo?.trust !== "untrusted" || !reviewed.repo.hash || !reviewed.repo.preview?.includes("11"))
    throw new Error("The hub did not resolve the new untrusted fixture; check shared filesystem configuration.");

  await writeFile(file, "max_rounds = 12\n");
  let rejected = false;
  try {
    await hub.request("evener/launch/trustRepo", { cwd, hash: reviewed.repo.hash });
  } catch (error) {
    if (!(error instanceof WireError) || error.code !== -32009) throw error;
    rejected = true;
  }
  if (!rejected) throw new Error("The hub accepted a stale reviewed hash.");
  const changed = await hub.request("evener/launch/resolve", { cwd });
  if (!changed.repo?.hash || changed.repo.hash === reviewed.repo.hash || changed.repo.trust === "trusted")
    throw new Error("The changed file was not left untrusted.");
  if (!changed.repo.preview?.includes("12")) throw new Error("The new preview does not describe the fixture.");

  // In an application, the user must review this new preview before this call.
  await hub.request("evener/launch/trustRepo", { cwd, hash: changed.repo.hash });
  const confirmed = await hub.request("evener/launch/resolve", { cwd });
  if (
    confirmed.repo?.hash !== changed.repo.hash ||
    confirmed.repo.trust !== "trusted" ||
    confirmed.effective.maxRounds !== 12
  )
    throw new Error("The reviewed revision and its effective setting were not confirmed.");
  console.log("Stale-hash rejection, fresh review, trust and independent effective-value readback verified.");
} catch (error) {
  failures.push(error);
} finally {
  hub.close();
}
try {
  if (cwd) {
    await rm(cwd, { recursive: true });
    console.log("Owned fixture directory removed. Trust metadata remains in the isolated hub state.");
  }
} catch (error) {
  failures.push(error);
}
if (failures.length > 0) throw new AggregateError(failures, "Repository-trust example failed.");
