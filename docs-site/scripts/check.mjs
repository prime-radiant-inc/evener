import { prepare } from "./prepare.mjs";
import { readFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { validateCandidate, fullMatrix, repositoryRoot } from "./tours/artifacts.mjs";

export async function checkTutorialAssets({root=repositoryRoot, assetRoot=join(root, "docs/assets/tutorials"), publication}={}) {
  publication ??= JSON.parse(await readFile(join(root, "docs-site/publication.json"), "utf8"));
  const manifest = await validateCandidate(assetRoot, fullMatrix, {sourceRoot:root, historicalProvenance:true});
  const selected = ["manifest.json", ...manifest.rows.flatMap(row => [row.file, ...(row.text ? [row.text.file] : [])])];
  for (const file of selected) {
    if (!publication.assets?.includes(`docs/assets/tutorials/${file}`)) {
      throw new Error(`Tutorial asset absent from publication.json.assets: ${file}`);
    }
  }
  return manifest;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  await checkTutorialAssets();
  const result = await prepare();
  console.log(`Validated and staged ${result.pages} pages and ${result.assets} assets, including Markdown exports and llms.txt.`);
}
