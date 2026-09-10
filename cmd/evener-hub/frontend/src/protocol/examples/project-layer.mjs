import { isDeepStrictEqual } from "node:util";
import { clientFromEnvironment } from "./connection.mjs";

if (process.env.EVENER_EXAMPLE_WRITE_PROJECT !== "1") {
  throw new Error("This example writes settings. Set EVENER_EXAMPLE_WRITE_PROJECT=1 for an isolated project.");
}
const { hub, cwd } = clientFromEnvironment();
const target = { cwd, layer: "project" };
let before;
let globalBefore;
let candidate;
let writeStarted = false;
let unsubscribe = () => {};
let timer;
const failures = [];
try {
  await hub.connect();
  const schema = await hub.request("evener/launch/schema", {});
  if (
    !schema.options.some((option) => option.wireField === "maxRounds" && option.defaultableLayers?.includes("project"))
  ) {
    throw new Error("This hub does not advertise project maxRounds editing.");
  }
  before = await hub.request("evener/launch/getLayer", target);
  globalBefore = await hub.request("evener/launch/getLayer", { cwd, layer: "global" });
  candidate = { ...before, maxRounds: before.maxRounds === 7 ? 8 : 7 };
  const current = await hub.request("evener/launch/getLayer", target);
  if (!isDeepStrictEqual(current, before)) throw new Error("Project layer changed before the example could write.");
  const updated = new Promise((resolve, reject) => {
    timer = setTimeout(() => reject(new Error("Launch update notification was not observed.")), 10000);
    unsubscribe = hub.onNotification((event) => {
      if (event.method === "evener/launch/updated" && event.params.cwd === cwd && event.params.layer === "project")
        resolve();
    });
  });
  writeStarted = true;
  await Promise.all([hub.request("evener/launch/setLayer", { ...target, config: candidate }), updated]);
  const saved = await hub.request("evener/launch/getLayer", target);
  if (!isDeepStrictEqual(saved, candidate))
    throw new Error("Saved project layer differs from the example's candidate.");
  const resolved = await hub.request("evener/launch/resolve", { cwd });
  if (resolved.effective.maxRounds !== candidate.maxRounds)
    throw new Error("Effective maxRounds does not match the project override.");
  console.log("Project write, readback, resolution and launch-update notification verified.");
} catch (error) {
  failures.push(error);
}
clearTimeout(timer);
unsubscribe();
try {
  if (writeStarted) {
    const current = await hub.request("evener/launch/getLayer", target);
    if (isDeepStrictEqual(current, candidate)) {
      await hub.request("evener/launch/setLayer", { ...target, config: before });
      const restored = await hub.request("evener/launch/getLayer", target);
      if (!isDeepStrictEqual(restored, before))
        throw new Error("Restoration could not be confirmed; inspect this project layer.");
    } else if (!isDeepStrictEqual(current, before)) {
      throw new Error("Project layer changed elsewhere; refusing automatic restoration. Inspect it manually.");
    }
    const globalAfter = await hub.request("evener/launch/getLayer", { cwd, layer: "global" });
    if (!isDeepStrictEqual(globalAfter, globalBefore))
      throw new Error("Global layer changed during the example; inspect it manually.");
    console.log("Original project layer restored; global layer unchanged.");
  }
} catch (error) {
  failures.push(error);
} finally {
  hub.close();
}
if (failures.length > 0)
  throw new AggregateError(failures, "Project-layer example failed; inspect the reported errors.");
