import { fileURLToPath } from "node:url";
import { createServer } from "vite";

const server = await createServer({
  configFile: fileURLToPath(new URL("./browserguard.vite.config.mjs", import.meta.url)),
  server: { host: "127.0.0.1", port: 0, strictPort: true },
});
// Vite's CLI normalizes port 0 to its default; bind the already-created
// HTTP server directly so the OS owns one random port for this child.
// Vite normalizes port zero to its default in server.listen(). Its HTTP
// listener retains the initialization wrapper and lets the OS own allocation.
await new Promise((resolve, reject) => {
  server.httpServer.once("error", reject);
  server.httpServer.listen({ host: "127.0.0.1", port: 0 }, resolve);
});
const address = server.httpServer?.address();
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
