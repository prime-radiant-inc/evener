import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { assertAllowedModule } from "./boundary-plugin.mjs";

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const mobileRoot = path.resolve(scriptDirectory, "..");

export { assertAllowedModule };

async function main() {
  const { build } = await import("vite");
  await build({
    configFile: path.join(mobileRoot, "vite.config.ts"),
    logLevel: "warn",
  });
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  await main();
}
