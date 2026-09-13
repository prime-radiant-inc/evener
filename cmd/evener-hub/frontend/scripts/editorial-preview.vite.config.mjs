import { fileURLToPath } from "node:url";
import { mergeConfig } from "vite";
import { isSharedNodeModules } from "./editorial-preview-install.mjs";
import base from "../vite.config.ts";

const frontend = fileURLToPath(new URL("../", import.meta.url));
// The AppWire package lives outside the frontend, so both fs.allow lists below
// have to name it: this config REPLACES the base config's allow list twice
// rather than extending it, and a /@fs/ request for a path outside the list is
// a 403 with a green typecheck and a green build.
const appwirePackage = fileURLToPath(new URL("../../../../appwire-client/typescript/", import.meta.url));
const isolated = mergeConfig(base, {
  plugins: [{
    name: "editorial-fixture-only",
    configResolved(config) {
      // mergeConfig merges proxy dictionaries. Delete the RESOLVED routes,
      // rather than assuming proxy:{} removes the inherited live :9180 hub.
      config.server.proxy = undefined;
      config.server.fs.allow = [frontend, appwirePackage];
      if (config.server.proxy !== undefined) throw new Error("Preview proxy must be absent");
    },
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const pathname = new URL(req.url ?? "/", "http://fixture.invalid").pathname;
        if (/^\/(rpc|api|auth|doc)(\/|$)/.test(pathname) || pathname.includes("/images/")) {
          res.statusCode = 403;
          res.end("Fixture preview: live backend routes are disabled");
          return;
        }
        if (req.headers.accept?.includes("text/html") && !pathname.startsWith("/@") && !pathname.startsWith("/src/") && !pathname.startsWith("/node_modules/")) {
          req.url = "/editorial-preview.html";
        }
        next();
      });
    },
  }],
  server: {
    host: "0.0.0.0", allowedHosts: ["m5"], strictPort: true, hmr: false,
    watch: { ignored: ["**/*"] }, fs: { strict: true, allow: [frontend, appwirePackage], deny: [".env", ".env.*", "**/.git/**", "**/.superpowers/**", "**/*.{crt,pem}"] },
  },
});
// No broad workspace root or shared install is required: run npm ci locally.
if (isSharedNodeModules(frontend)) {
  throw new Error("Editorial preview requires its own npm ci, not a shared writable install");
}
export default isolated;
