import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";

const frontend = path.dirname(fileURLToPath(import.meta.url));
const frontendRoot = path.dirname(frontend);

// startBrowserGuard may pass a guard-specific config file (the editorial
// preview's fixture-only config) as the wrapper's one argument. Resolve it
// against the frontend directory (the wrapper's own location), never
// process.cwd(), and reject absolute or traversal paths that escape it so
// an injected argument cannot load an arbitrary Vite config. The default
// stays the hermetic browser-guard config.
const defaultConfig = path.join(frontend, "browserguard.vite.config.mjs");
const configFile = (() => {
  const arg = process.argv[2];
  if (!arg) return defaultConfig;
  // startBrowserGuard passes config paths relative to the frontend root
  // (e.g. "scripts/editorial-preview.vite.config.mjs"), not relative to
  // this wrapper's own location. Resolve against the frontend root and
  // reject absolute or traversal paths that escape it so an injected
  // argument cannot load an arbitrary Vite config.
  const resolved = path.resolve(frontendRoot, arg);
  if (resolved !== frontendRoot && !resolved.startsWith(frontendRoot + path.sep)) {
    throw new Error(`browserguard-vite: config path "${arg}" resolves outside the frontend directory`);
  }
  return resolved;
})();
const server = await createServer({
  configFile,
  server: { host: "127.0.0.1", port: 0, strictPort: true },
});
// Vite normalizes port zero to its default in server.listen(). Its HTTP
// listener retains the initialization wrapper and lets the OS own allocation.
const httpServer = server.httpServer;
if (!httpServer) throw new Error("Vite did not create an HTTP server");
await new Promise((resolve, reject) => {
  const onError = (error) => reject(error);
  httpServer.once("error", onError);
  httpServer.listen({ host: "127.0.0.1", port: 0 }, () => {
    httpServer.removeListener("error", onError);
    resolve();
  });
});
const address = httpServer.address();
if (!address || typeof address === "string") {
  await server.close();
  throw new Error("Vite did not expose a bound TCP address");
}
process.stdout.write(`Local: http://127.0.0.1:${address.port}/\n`);

let closing;
const close = () => {
  closing ??= server.close();
  return closing;
};
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.once(signal, async () => {
    try {
      await close();
    } finally {
      process.exit(0);
    }
  });
}
