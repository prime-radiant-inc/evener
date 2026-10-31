import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import test from "node:test";

const frontend = new URL("../", import.meta.url);
const digest = (bytes) => createHash("sha256").update(bytes).digest("hex");

// Independent originals from Microsoft's f53e4b6 open-source release.
test("Comic font faces resolve to the distributable upstream font bytes", () => {
  const css = readFileSync(new URL("src/comic/comic.module.css", frontend), "utf8");
  const faces = [...css.matchAll(/@font-face\s*\{([^}]+)\}/g)];
  assert.ok(faces.length > 0);
  for (const [, face] of faces) {
    const sources = [...face.matchAll(/url\(["']?([^"')]+)["']?\)/g)];
    assert.ok(sources.length > 0);
    for (const [, source] of sources) {
      const asset = new URL(`public${source}`, frontend);
      assert.ok(existsSync(asset), `Missing bundled font: ${fileURLToPath(asset)}`);
      assert.equal(digest(readFileSync(asset)), "08e336a641ef44f0a6c745a52c64ba10ad58a8aad631e3db79c98cb703b9893f");
    }
  }
});

test("the bundled font retains Microsoft's original MIT grant", () => {
  const license = new URL("public/webassets/comic-fonts/LICENSE", frontend);
  assert.ok(existsSync(license));
  assert.equal(digest(readFileSync(license)), "722262cd04696639f0f65df38d792fb4e08a28309ff5cee15f10bdd9b66411a3");
});
