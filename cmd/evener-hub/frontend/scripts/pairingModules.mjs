import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer as createViteServer } from "vite";

// These host checks load pure protocol modules in Node. Keep the CommonJS
// crypto dependency on Node's loader; the browser guard separately exercises
// the application's Vite configuration and browser dependency transforms.
//
// Vite otherwise caches dep optimizations in <root>/node_modules/.vite. A
// fleet worktree symlinks node_modules to one shared install, so that write
// follows the link and races other lanes; a read-only checkout cannot create
// it at all (issue #4482). Give each server its own writable run directory and
// remove it when the server closes.
export async function loadPairingModules(createServer = createViteServer) {
  const cacheDir = mkdtempSync(path.join(tmpdir(), "evener-pairing-vite-"));
  let server;
  try {
    server = await createServer({
      cacheDir,
      configFile: false,
      root: fileURLToPath(new URL("..", import.meta.url)),
      server: { middlewareMode: true },
      appType: "custom",
      resolve: {
        dedupe: ["tweetnacl"],
        alias: {
          "@evener/appwire-client/pairing": fileURLToPath(
            new URL("../../../../appwire-client/typescript/pairing.ts", import.meta.url),
          ),
        },
      },
    });
  } catch (error) {
    rmSync(cacheDir, { recursive: true, force: true });
    throw error;
  }
  const close = server.close.bind(server);
  server.close = async () => {
    try {
      await close();
    } finally {
      rmSync(cacheDir, { recursive: true, force: true });
    }
  };
  return server;
}
