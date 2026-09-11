import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";

// startBrowserGuard may pass a guard-specific config file (the editorial
// preview's fixture-only config) as the wrapper's one argument; it resolves
// against the frontend cwd startBrowserGuard spawns the wrapper with. The
// default stays the hermetic browser-guard config.
const configFile = process.argv[2]
  ? path.resolve(process.argv[2])
  : fileURLToPath(new URL("./browserguard.vite.config.mjs", import.meta.url));
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
