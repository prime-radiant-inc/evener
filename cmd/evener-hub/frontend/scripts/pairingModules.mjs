import { fileURLToPath } from "node:url";
import { createServer } from "vite";

// These host checks load pure protocol modules in Node. Keep the CommonJS
// crypto dependency on Node's loader; the browser guard separately exercises
// the application's Vite configuration and browser dependency transforms.
export function loadPairingModules() {
  return createServer({
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
}
