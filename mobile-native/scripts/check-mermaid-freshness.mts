// check-mermaid-freshness.mts - fail if the committed mermaid bundle is stale.
//
// The bundle is `src/generated/mermaidPage.ts`, a 5 MB two-line module that
// makes the WebView work offline. Rebuilding it is an esbuild run over
// mermaid and DOMPurify, which is why this is its own check and not one of
// `npm test`'s cases: every test run paid that rebuild, and the freshness
// question only changes when the generator, its inputs, or the package's
// shared sanitize constants do.
//
//   npx tsx scripts/check-mermaid-freshness.mts
//
// Exit 0 when the committed module matches a rebuild, 1 when it does not.
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { buildMermaidPageHtml, generatedModule } from "./build-mermaid-page.mjs";

const nativeRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const generated = readFileSync(path.join(nativeRoot, "src", "generated", "mermaidPage.ts"), "utf8");
// Ask the generator itself what a fresh module looks like, so a change to its
// header or serialization cannot make a regenerated file fail this check.
const want = generatedModule(await buildMermaidPageHtml());

if (generated !== want) {
	process.stderr.write(
		"mermaidPage.ts is stale: run `npx tsx scripts/build-mermaid-page.mts` from mobile-native and commit the result.\n",
	);
	process.exit(1);
}
process.stdout.write("mermaidPage.ts is fresh\n");
