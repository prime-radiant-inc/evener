#!/usr/bin/env node
// Regenerates src/widgets/mermaid/testdata/*.svg from testdata/sources/*.mmd
// by rendering each with the REAL mermaid in jsdom (structure is real;
// geometry is not - jsdom has no SVG layout). Run after any mermaid version
// bump: npm run generate:mermaid-fixtures
import { spawnSync } from "node:child_process";
import { readdirSync, writeFileSync } from "node:fs";

const SOURCES = new URL("../src/widgets/mermaid/testdata/sources/", import.meta.url);
const OUT = new URL("../src/widgets/mermaid/testdata/", import.meta.url);

for (const file of readdirSync(SOURCES).filter((f) => f.endsWith(".mmd"))) {
  const result = spawnSync(
    process.execPath,
    [new URL("./render-one-mermaid-fixture.mjs", import.meta.url).pathname, file],
    {
      encoding: "utf8",
    },
  );
  if (result.status !== 0) throw new Error(`${file}: ${result.stderr || result.stdout}`);
  writeFileSync(new URL(`${file.replace(/\.mmd$/, ".svg")}`, OUT), result.stdout);
  console.log(`${file} -> ${file.replace(/\.mmd$/, ".svg")}`);
}
