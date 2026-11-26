import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import path from "node:path";

// The published SDK only starts its renderer from blob: or data: URLs. Serve
// its unchanged module as a hub asset so the worker obeys the page's CSP.
const sdk = "@glass-sdk/liquid-glass";
const client = sdk + "/dist/chunks/client--P39HLKL.js";

export function glassWorker(frontend) {
  let clientSource;
  try {
    clientSource = readFileSync(path.join(frontend, "node_modules", client), "utf8");
  } catch (cause) {
    throw new Error("Cannot find the pinned Glass SDK renderer client. Check its worker packaging before upgrading.", { cause });
  }
  const literal = /^var e = ("(?:\\.|[^"\\])*"), t = typeof self/m.exec(clientSource);
  const clientStart = clientSource.indexOf("// src/render/client.ts");
  if (!literal || clientStart < literal.index + literal[0].length) {
    throw new Error("Unrecognized Glass SDK inline worker. Check the pinned package's renderer client.");
  }
  const worker = JSON.parse(literal[1]);
  const hash = createHash("sha256").update(worker).digest("hex").slice(0, 16);
  const asset = `webassets/glass-worker-${hash}.js`;
  const licenseAsset = `webassets/glass-worker-${hash}.LICENSE.txt`;
  const license = readFileSync(path.join(frontend, "node_modules", sdk, "LICENSE"), "utf8");
  const url = "/" + asset;
  let build = false;

  return {
    name: "same-origin-glass-worker",
    enforce: "pre",
    // The dev optimizer would copy the original worker factory into its cache.
    config: () => ({ optimizeDeps: { exclude: [sdk] } }),
    configResolved(config) {
      build = config.command === "build";
    },
    configureServer(server) {
      server.middlewares.use(url, (_request, response) => {
        response.setHeader("Content-Type", "text/javascript; charset=utf-8");
        response.end(worker);
      });
      server.middlewares.use("/" + licenseAsset, (_request, response) => {
        response.setHeader("Content-Type", "text/plain; charset=utf-8");
        response.end(license);
      });
    },
    load(id) {
      if (!id.split("?", 1)[0].endsWith("/" + client)) return;
      if (build) {
        this.emitFile({ type: "asset", fileName: asset, source: worker });
        this.emitFile({ type: "asset", fileName: licenseAsset, source: license });
      }
      const factory = `function n(options) { return new Worker(${JSON.stringify(url)}, { type: "module", name: options?.name }); }\n`;
      return factory + clientSource.slice(clientStart);
    },
  };
}
