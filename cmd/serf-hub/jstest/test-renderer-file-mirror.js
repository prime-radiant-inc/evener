// The no-bundler renderer bundle must load in the same dependency order in the
// browser (templates/app.html) and in tests (load-renderer.js RENDERER_FILES).
// A module added to one list but not the other breaks either the app or every
// renderer test — this mirror makes the drift a loud failure.
const fs = require("fs");
const path = require("path");
const assert = require("assert");
const { RENDERER_FILES } = require("./load-renderer");

const appHtml = fs.readFileSync(path.resolve(__dirname, "../templates/app.html"), "utf8");
const srcs = [...appHtml.matchAll(/<script[^>]+src="([^"]+)"/g)].map((m) => m[1]);
const scriptNames = srcs.map((s) => s.split("/").pop().replace("{{assetv}}", ""));
const mirrored = scriptNames.filter((n) => RENDERER_FILES.includes(n));
assert.deepStrictEqual(
  mirrored,
  RENDERER_FILES,
  "templates/app.html must load exactly the RENDERER_FILES, in the same order: " +
    JSON.stringify({ mirrored, RENDERER_FILES })
);
console.log("PASS: app.html script order mirrors RENDERER_FILES");
